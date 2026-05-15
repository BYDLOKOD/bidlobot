// Package gracekick is the daily inactive-member lifecycle: tag publicly,
// give a grace window, then kick if still silent. It is the humane
// alternative to a silent purge - a tagged member who writes OR reacts
// before the deadline is spared.
//
// Hard safety invariant: only members with PROVEN inactivity evidence
// (cleanup.Preview.Candidates - the bot actually observed them go quiet)
// are ever tagged or kicked here. The "no recorded activity at all"
// bucket (cleanup.Preview.NoEvidence) is a data gap, not silence, and is
// NEVER touched by this automatic, public path. Publicly @-tagging a
// member the bot has no evidence against would be unacceptable.
package gracekick

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/mymmrac/telego"

	"github.com/veschin/bidlobot/internal/domain/cleanup"
	"github.com/veschin/bidlobot/internal/domain/membership"
	"github.com/veschin/bidlobot/internal/shared"
)

// DefaultGrace is the time between being tagged and being kicked if the
// member never reappears. Owner decision (2026-05-15): 3 days.
const DefaultGrace = 72 * time.Hour

// DefaultBatch caps how many members one chat tags per daily run, so a
// chat with 300 stale members is worked down ~15/day instead of a single
// 300-mention wall that reads as spam and hits Telegram's message limit.
const DefaultBatch = 15

// Record is one member's open grace ticket. Its mere existence means
// "tagged, awaiting reappearance"; it is deleted on save or on kick.
type Record struct {
	AbsChatID     int64     `json:"abs_chat_id"`
	UserID        int64     `json:"user_id"`
	Username      string    `json:"username,omitempty"`
	FirstName     string    `json:"first_name,omitempty"`
	TaggedAt      time.Time `json:"tagged_at"`
	GraceDeadline time.Time `json:"grace_deadline"`
}

// Store persists open grace tickets. A real bbolt repo and an in-memory
// test double both satisfy it.
type Store interface {
	Put(ctx context.Context, r Record) error
	ListByChat(ctx context.Context, absChatID int64) ([]Record, error)
	Delete(ctx context.Context, absChatID, userID int64) error
}

// Previewer is the slice of *cleanup.Service this package needs: the
// evidence-graded candidate list plus identity resolution for the public
// mention.
type Previewer interface {
	PreviewInactive(ctx context.Context, absChatID int64, threshold time.Duration, now time.Time) (*cleanup.Preview, error)
	ResolveIdentities(ctx context.Context, absChatID int64, in []membership.Member, maxAPILookups int) []cleanup.ResolvedMember
}

// Kicker is the ban+unban executor (satisfied by *cleanup.Service). It
// re-checks each target's live status before acting, so a member who
// became admin or already left between tag and deadline is skipped.
type Kicker interface {
	ExecuteCleanup(ctx context.Context, signedChatID int64, candidates []membership.Member, progress func(done, total int, last cleanup.ExecutionEntry)) (*cleanup.Report, error)
}

// MemberLookup reads the live membership record so the sweep can tell
// whether a tagged member wrote or reacted after being tagged.
type MemberLookup interface {
	GetMember(ctx context.Context, userID, absChatID int64) (*membership.Member, error)
}

// Announcer posts the single public tag message.
type Announcer interface {
	SendMessage(ctx context.Context, params *telego.SendMessageParams) (*telego.Message, error)
}

type Config struct {
	Threshold time.Duration // inactivity window fed to PreviewInactive
	Grace     time.Duration // tag -> kick delay; DefaultGrace if zero
	Batch     int           // max tags/chat/run; DefaultBatch if <=0
}

func (c Config) normalized() Config {
	if c.Grace <= 0 {
		c.Grace = DefaultGrace
	}
	if c.Batch <= 0 {
		c.Batch = DefaultBatch
	}
	return c
}

type Service struct {
	store   Store
	prev    Previewer
	kick    Kicker
	members MemberLookup
	out     Announcer
	cfg     Config
	log     *slog.Logger
}

func NewService(store Store, prev Previewer, kick Kicker, members MemberLookup, out Announcer, cfg Config, log *slog.Logger) *Service {
	return &Service{
		store:   store,
		prev:    prev,
		kick:    kick,
		members: members,
		out:     out,
		cfg:     cfg.normalized(),
		log:     log,
	}
}

// Summary is the per-chat outcome of one daily run, for logs/metrics.
type Summary struct {
	Tagged int
	Saved  int
	Kicked int
	Failed int
}

// RunDaily runs one chat's lifecycle tick: first sweep open tickets whose
// grace expired (spare the reappeared, kick the still-silent), then tag a
// fresh batch of proven-inactive members. absChatID is the membership
// absolute id; the signed id (-abs) is what Telegram APIs receive.
func (s *Service) RunDaily(ctx context.Context, absChatID int64, now time.Time) (Summary, error) {
	var sum Summary
	signed := -absChatID

	recs, err := s.store.ListByChat(ctx, absChatID)
	if err != nil {
		return sum, fmt.Errorf("gracekick: list tickets: %w", err)
	}

	// --- sweep -----------------------------------------------------------
	stillOpen := make(map[int64]struct{}, len(recs))
	var dueKick []membership.Member
	for _, r := range recs {
		if now.Before(r.GraceDeadline) {
			stillOpen[r.UserID] = struct{}{} // in grace, do not re-tag below
			continue
		}
		saved, determined := s.reappeared(ctx, r)
		if !determined {
			// Could not read the member's live record. Never kick on
			// uncertainty: keep the ticket, keep them out of the re-tag
			// set, and re-evaluate on a later run.
			stillOpen[r.UserID] = struct{}{}
			s.log.Warn("gracekick: reappearance undetermined, deferring kick",
				"chat", absChatID, "user", r.UserID)
			continue
		}
		if saved {
			_ = s.store.Delete(ctx, absChatID, r.UserID)
			sum.Saved++
			continue
		}
		dueKick = append(dueKick, membership.Member{
			AbsChatID: absChatID, UserID: r.UserID,
			Username: r.Username, FirstName: r.FirstName,
		})
	}
	if len(dueKick) > 0 {
		rep, kerr := s.kick.ExecuteCleanup(ctx, signed, dueKick, nil)
		// Tickets are terminal once the deadline passed AND we attempted
		// the kick: a transient kick failure must not leave a stuck
		// ticket - the member, if still stale, re-enters via the tag
		// phase on a later run.
		for _, m := range dueKick {
			_ = s.store.Delete(ctx, absChatID, m.UserID)
		}
		if kerr != nil {
			s.log.Warn("gracekick: kick batch returned error", "chat", absChatID, "error", kerr)
		}
		if rep != nil {
			sum.Kicked = rep.Kicked
			sum.Failed = rep.Failed
		}
	}

	// Do not start a fresh public tag round while shutting down: a
	// cancelled context here would otherwise still post to the chat.
	if cerr := ctx.Err(); cerr != nil {
		return sum, cerr
	}

	// --- tag -------------------------------------------------------------
	p, err := s.prev.PreviewInactive(ctx, absChatID, s.cfg.Threshold, now)
	if err != nil {
		return sum, fmt.Errorf("gracekick: preview: %w", err)
	}
	// Candidates are the PROVEN-stale set only. NoEvidence is deliberately
	// ignored here - never auto-tag a member the bot has no evidence on.
	fresh := make([]membership.Member, 0, len(p.Candidates))
	for _, m := range p.Candidates {
		if _, open := stillOpen[m.UserID]; open {
			continue // already under an unexpired grace ticket
		}
		fresh = append(fresh, m)
		if len(fresh) >= s.cfg.Batch {
			break
		}
	}
	if len(fresh) == 0 {
		return sum, nil
	}

	resolved := s.prev.ResolveIdentities(ctx, absChatID, fresh, s.cfg.Batch)
	pick := make([]cleanup.ResolvedMember, 0, len(resolved))
	for _, rm := range resolved {
		// Affirmative safety ONLY. Never publicly tag / auto-kick a
		// member we could not identify (failed live lookup -> Resolved
		// false), who is not present (left/kicked), or who is protected
		// (admin/bot). An unresolved member is precisely the "no evidence
		// to act on, let alone publicly" case.
		if !rm.Resolved || !rm.Present || rm.Protected {
			continue
		}
		pick = append(pick, rm)
	}
	// Cap the batch to what fits in one Telegram message (4096 chars) so
	// an oversized run can never 400-loop forever and stall the chat.
	pick = fitOneMessage(pick, s.cfg.Grace)
	if len(pick) == 0 {
		return sum, nil
	}

	if err := s.announce(ctx, signed, pick); err != nil {
		// Nothing persisted yet - safe to retry next run. Do NOT write
		// tickets for an announcement that never reached the chat, or the
		// member would be kicked after the grace window without ever
		// being warned.
		return sum, fmt.Errorf("gracekick: announce: %w", err)
	}
	taggedAt := now.Truncate(time.Second) // match second-granular activity ts
	deadline := taggedAt.Add(s.cfg.Grace)
	for _, rm := range pick {
		if perr := s.store.Put(ctx, Record{
			AbsChatID: absChatID, UserID: rm.UserID,
			Username: rm.Username, FirstName: rm.FirstName,
			TaggedAt: taggedAt, GraceDeadline: deadline,
		}); perr != nil {
			s.log.Warn("gracekick: persist ticket failed", "chat", absChatID, "user", rm.UserID, "error", perr)
			continue
		}
		sum.Tagged++
	}
	return sum, nil
}

// reappeared reports whether the member wrote OR reacted after being
// tagged. Owner decision: a message or a reaction both count as "alive".
// It returns (saved, determined): determined is false when the live
// membership record could not be read (store error, or no record at
// all). The caller must NEVER kick on determined=false - a read fault
// must not translate into removing a member who may well have written.
func (s *Service) reappeared(ctx context.Context, r Record) (saved, determined bool) {
	m, err := s.members.GetMember(ctx, r.UserID, r.AbsChatID)
	if err != nil || m == nil {
		return false, false
	}
	return m.LastMessageAt.After(r.TaggedAt) || m.LastReactionAt.After(r.TaggedAt), true
}

// utf16Len returns len(s) in UTF-16 code units - the unit Telegram uses
// for its 4096-char message cap. An astral character (emoji) is one rune
// but TWO UTF-16 units, so a rune count would undercount an adversarial
// display name by ~2x and let an oversized message through.
func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		if r > 0xFFFF {
			n += 2
		} else {
			n++
		}
	}
	return n
}

// fitOneMessage trims pick so the rendered announcement stays well under
// Telegram's 4096 UTF-16-unit message limit. Without this, a large
// CLEANUP_DAILY_BATCH of nameless (inline-link) members could exceed the
// limit, make SendMessage 400 forever, and silently freeze the lifecycle
// for that chat. Members that do not fit are tagged on a later run. At
// least one is always kept; mention() is measured already HTML-escaped,
// so per-key escape expansion (`<` -> `&lt;`) is counted accurately.
func fitOneMessage(pick []cleanup.ResolvedMember, _ time.Duration) []cleanup.ResolvedMember {
	const budget = 3000 // generous headroom under 4096 for preamble+footer
	used := 0
	out := make([]cleanup.ResolvedMember, 0, len(pick))
	for _, rm := range pick {
		c := utf16Len(mention(rm)) + 2 // ", "
		if len(out) > 0 && used+c > budget {
			break
		}
		used += c
		out = append(out, rm)
	}
	return out
}

// truncUTF16 trims s to at most maxUnits UTF-16 code units, only ever
// cutting on a rune boundary so a surrogate pair is never split.
func truncUTF16(s string, maxUnits int) string {
	units := 0
	for i, r := range s {
		w := 1
		if r > 0xFFFF {
			w = 2
		}
		if units+w > maxUnits {
			return s[:i]
		}
		units += w
	}
	return s
}

// announce posts ONE public message that @-mentions the whole batch and
// states the rule. A member with a username is mentioned as @name; one
// without is mentioned via an inline tg://user link so the ping still
// fires. Reply-or-react is requested explicitly because, under BotFather
// privacy ON, an ordinary message is invisible to the bot while a reply
// to the bot and any reaction are not - so the instruction keeps the
// "saved" signal reliable in both privacy models.
func (s *Service) announce(ctx context.Context, signedChatID int64, pick []cleanup.ResolvedMember) error {
	var b strings.Builder
	fmt.Fprintf(&b, "🧹 <b>Чистка неактивных</b>\n\n"+
		"Эти участники давно не пишут и не реагируют. Чтобы остаться - "+
		"<b>напишите в чат (ответом на это сообщение) или поставьте любую реакцию</b> "+
		"в течение %s. Иначе бот удалит вас из чата.\n\n",
		formatDur(s.cfg.Grace))
	for i, rm := range pick {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(mention(rm))
	}
	b.WriteString("\n\n<i>Любое сообщение или реакция в чате снимает из списка.</i>")

	_, err := s.out.SendMessage(ctx, &telego.SendMessageParams{
		ChatID:    telego.ChatID{ID: signedChatID},
		Text:      b.String(),
		ParseMode: telego.ModeHTML,
	})
	return err
}

// mention renders a single ping. @username when known (no HTML needed -
// usernames are [A-Za-z0-9_]); otherwise an HTML inline mention by id
// with an HTML-escaped visible name so the tag still notifies a user who
// has no public @handle.
func mention(rm cleanup.ResolvedMember) string {
	if rm.Username != "" {
		return "@" + rm.Username
	}
	name := strings.TrimSpace(rm.FirstName)
	if name == "" {
		name = "участник"
	}
	name = truncUTF16(name, 32) // bound a single mention's length (UTF-16)
	return fmt.Sprintf(`<a href="tg://user?id=%d">%s</a>`, rm.UserID, shared.EscapeHTML(name))
}

// formatDur renders a grace duration in plain Russian ("3 дня", "12
// часов"). Kept local so the domain has no dependency on the bot layer's
// formatter.
func formatDur(d time.Duration) string {
	if d%(24*time.Hour) == 0 {
		days := int(d / (24 * time.Hour))
		return fmt.Sprintf("%d %s", days, plural(days, "день", "дня", "дней"))
	}
	hours := int(d / time.Hour)
	return fmt.Sprintf("%d %s", hours, plural(hours, "час", "часа", "часов"))
}

func plural(n int, one, few, many string) string {
	n = n % 100
	if n >= 11 && n <= 14 {
		return many
	}
	switch n % 10 {
	case 1:
		return one
	case 2, 3, 4:
		return few
	default:
		return many
	}
}

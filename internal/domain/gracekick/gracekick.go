// Package gracekick is the inactive-member campaign: an admin starts it
// from the DM /cleanup command with a proven-stale list; the bot then,
// once a day, publicly @-tags the next batch with a grace deadline,
// spares anyone who writes or reacts in time, and kicks the rest - until
// the seeded list is exhausted.
//
// This package does NOT decide who is inactive. The /cleanup command
// computes the evidence-graded candidate list (cleanup.Preview.Candidates
// - members the bot actually observed go quiet, NEVER the NoEvidence
// data-gap bucket) and passes only that into Seed. gracekick just drives
// the day-by-day public lifecycle over whatever it was seeded with.
package gracekick

import (
	"html"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/mymmrac/telego"

	"github.com/veschin/bidlobot/internal/domain/cleanup"
	"github.com/veschin/bidlobot/internal/domain/membership"
	"github.com/veschin/bidlobot/internal/shared"
)

// DefaultGrace is the time between being publicly tagged and being
// kicked if the member never reappears. Owner decision: 3 days.
const DefaultGrace = 72 * time.Hour

// DefaultBatch caps how many members one chat tags per daily run, so a
// 300-person campaign is worked down ~15/day instead of a single
// 300-mention wall (spam + Telegram's 4096-char message limit).
const DefaultBatch = 15

// Record states. A campaign member is queued (seeded by /cleanup,
// awaiting its day) then tagged (publicly pinged, grace clock running).
// A record is deleted when the member is saved, kicked, or dropped.
const (
	StateQueued = "queued"
	StateTagged = "tagged"
)

// Record is one member's slot in a chat's campaign.
type Record struct {
	AbsChatID int64  `json:"abs_chat_id"`
	UserID    int64  `json:"user_id"`
	Username  string `json:"username,omitempty"`
	FirstName string `json:"first_name,omitempty"`

	State    string    `json:"state"`     // queued | tagged
	SeededAt time.Time `json:"seeded_at"` // when /cleanup confirm enqueued them

	// Set only once promoted to tagged (zero while queued):
	TaggedAt      time.Time `json:"tagged_at"`
	GraceDeadline time.Time `json:"grace_deadline"`
}

// Store persists campaign records. A bbolt repo and an in-memory test
// double both satisfy it.
type Store interface {
	Put(ctx context.Context, r Record) error
	ListByChat(ctx context.Context, absChatID int64) ([]Record, error)
	Delete(ctx context.Context, absChatID, userID int64) error
}

// Kicker is the ban+unban executor (satisfied by *cleanup.Service). It
// re-checks each target's live status before acting, so a member who
// became admin or already left between tag and deadline is skipped.
type Kicker interface {
	ExecuteCleanup(ctx context.Context, signedChatID int64, candidates []membership.Member, progress func(done, total int, last cleanup.ExecutionEntry)) (*cleanup.Report, error)
}

// MemberLookup reads the live membership record so the bot can tell
// whether a member wrote or reacted (and is still a normal member).
type MemberLookup interface {
	GetMember(ctx context.Context, userID, absChatID int64) (*membership.Member, error)
}

// Announcer posts the single public tag message.
type Announcer interface {
	SendMessage(ctx context.Context, params *telego.SendMessageParams) (*telego.Message, error)
}

type Config struct {
	Grace time.Duration // tag -> kick delay; DefaultGrace if zero
	Batch int           // max tags/chat/run; DefaultBatch if <=0
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
	kick    Kicker
	members MemberLookup
	out     Announcer
	cfg     Config
	log     *slog.Logger

	// locks serializes the record-mutation phases of RunDaily/Seed/Cancel
	// per chat so a `/cleanup stop` cannot race a daily tick into
	// resurrecting cancelled records (publicly tagging + kicking people
	// for a stopped campaign). One mutex per absChatID, created lazily.
	locks sync.Map // absChatID -> *sync.Mutex
}

// chatLock takes the per-chat mutex and returns its unlock. The throttled
// kick loop is deliberately run OUTSIDE this lock (see RunDaily) so a
// stop/seed stays responsive even during a long purge.
func (s *Service) chatLock(absChatID int64) func() {
	mi, _ := s.locks.LoadOrStore(absChatID, &sync.Mutex{})
	m := mi.(*sync.Mutex)
	m.Lock()
	return m.Unlock
}

// seenAtOrAfter reports activity at or after `since`. The boundary is
// inclusive (>=, not >): Telegram message/reaction timestamps are
// second-granular, so a member who acts in the very second they were
// tagged/seeded must count as active - a strict `>` there would kick a
// member who did exactly what the warning asked.
func seenAtOrAfter(ts, since time.Time) bool {
	return !ts.IsZero() && !ts.Before(since)
}

func NewService(store Store, kick Kicker, members MemberLookup, out Announcer, cfg Config, log *slog.Logger) *Service {
	return &Service{
		store:   store,
		kick:    kick,
		members: members,
		out:     out,
		cfg:     cfg.normalized(),
		log:     log,
	}
}

// Summary is the per-chat outcome of one daily run, for logs.
type Summary struct {
	Tagged int
	Saved  int
	Kicked int
	Failed int
}

// CampaignSize returns how many records (queued + tagged) the chat has.
// Zero means no active campaign. Used by /cleanup to refuse a re-start
// and by the scheduler to skip idle chats.
func (s *Service) CampaignSize(ctx context.Context, absChatID int64) (int, error) {
	recs, err := s.store.ListByChat(ctx, absChatID)
	return len(recs), err
}

// Seed enqueues members into the chat's campaign as `queued`, skipping
// anyone already in it (idempotent). The caller (/cleanup confirm) must
// pass ONLY proven-stale, name-resolved candidates - gracekick trusts
// this and never re-derives who is inactive. Returns count newly queued.
func (s *Service) Seed(ctx context.Context, absChatID int64, members []membership.Member, now time.Time) (int, error) {
	defer s.chatLock(absChatID)()
	existing, err := s.store.ListByChat(ctx, absChatID)
	if err != nil {
		return 0, fmt.Errorf("gracekick seed: list: %w", err)
	}
	have := make(map[int64]struct{}, len(existing))
	for _, r := range existing {
		have[r.UserID] = struct{}{}
	}
	seededAt := now.Truncate(time.Second)
	seeded := 0
	for _, m := range members {
		if _, dup := have[m.UserID]; dup {
			continue
		}
		if err := s.store.Put(ctx, Record{
			AbsChatID: absChatID, UserID: m.UserID,
			Username: m.Username, FirstName: m.FirstName,
			State: StateQueued, SeededAt: seededAt,
		}); err != nil {
			return seeded, fmt.Errorf("gracekick seed: put %d: %w", m.UserID, err)
		}
		seeded++
	}
	return seeded, nil
}

// Cancel drops the whole campaign for a chat (the /cleanup stop path).
// Returns how many records were removed.
func (s *Service) Cancel(ctx context.Context, absChatID int64) (int, error) {
	defer s.chatLock(absChatID)()
	recs, err := s.store.ListByChat(ctx, absChatID)
	if err != nil {
		return 0, fmt.Errorf("gracekick cancel: list: %w", err)
	}
	for _, r := range recs {
		_ = s.store.Delete(ctx, absChatID, r.UserID)
	}
	return len(recs), nil
}

// RunDaily is one chat's daily tick over an existing campaign:
//  1. sweep `tagged` records whose grace expired - spare the reappeared,
//     kick the still-silent;
//  2. promote up to Batch `queued` records to `tagged` - dropping anyone
//     who came back (or stopped being a normal member) since seeding,
//     publicly tagging the rest with a fresh grace deadline.
//
// No campaign -> no-op. The campaign ends naturally when the last record
// is removed.
func (s *Service) RunDaily(ctx context.Context, absChatID int64, now time.Time) (Summary, error) {
	var sum Summary
	signed := -absChatID

	// --- phase A: under the chat lock, snapshot + decide the kick set ----
	unlock := s.chatLock(absChatID)
	recs, err := s.store.ListByChat(ctx, absChatID)
	if err != nil {
		unlock()
		return sum, fmt.Errorf("gracekick: list: %w", err)
	}
	if len(recs) == 0 {
		unlock()
		return sum, nil // no campaign for this chat
	}
	var dueKick []membership.Member
	for _, r := range recs {
		if r.State != StateTagged || now.Before(r.GraceDeadline) {
			continue
		}
		active, determined := s.activeSince(ctx, r.UserID, absChatID, r.TaggedAt)
		if !determined {
			// Can't read the live record. Never kick on uncertainty:
			// leave the ticket, re-evaluate next run.
			s.log.Warn("gracekick: reappearance undetermined, deferring kick",
				"chat", absChatID, "user", r.UserID)
			continue
		}
		if active {
			_ = s.store.Delete(ctx, absChatID, r.UserID)
			sum.Saved++
			continue
		}
		dueKick = append(dueKick, membership.Member{
			AbsChatID: absChatID, UserID: r.UserID,
			Username: r.Username, FirstName: r.FirstName,
		})
	}
	unlock()

	// --- phase B: kick OUTSIDE the lock ---------------------------------
	// ExecuteCleanup is throttled (~2s/kick); holding the per-chat lock
	// across it would freeze /cleanup stop for minutes. kickOne pre-checks
	// already-left/admin, so a concurrent Cancel deleting these records
	// mid-loop is harmless.
	if len(dueKick) > 0 {
		rep, kerr := s.kick.ExecuteCleanup(ctx, signed, dueKick, nil)
		if kerr != nil {
			s.log.Warn("gracekick: kick batch error", "chat", absChatID, "error", kerr)
		}
		if rep != nil {
			sum.Kicked = rep.Kicked
			sum.Failed = rep.Failed
			for _, e := range rep.Entries {
				if e.Outcome == cleanup.OutcomeFailed {
					s.log.Warn("gracekick: kick failed for member",
						"chat", absChatID, "user", e.UserID, "error", e.APIError)
				}
			}
		}
		unlock = s.chatLock(absChatID)
		for _, m := range dueKick {
			// Terminal once attempted: a transient kick failure must not
			// leave a stuck ticket (it would loop forever as "already
			// left" and wedge campaign termination). A failed delete is
			// surfaced; the record then self-heals via the kickOne
			// already-left skip on a later run.
			if derr := s.store.Delete(ctx, absChatID, m.UserID); derr != nil {
				s.log.Warn("gracekick: post-kick ticket delete failed",
					"chat", absChatID, "user", m.UserID, "error", derr)
			}
		}
		unlock()
	}

	// Do not start a public tag round during shutdown.
	if cerr := ctx.Err(); cerr != nil {
		return sum, cerr
	}

	// --- phase C: under the lock, recompute promote from CURRENT state --
	// Re-listing here is what makes a concurrent Cancel/Seed safe: a
	// stopped campaign has no records left, so nothing is resurrected.
	unlock = s.chatLock(absChatID)
	defer unlock()
	recs, err = s.store.ListByChat(ctx, absChatID)
	if err != nil {
		return sum, fmt.Errorf("gracekick: relist: %w", err)
	}
	var batch []Record
	for _, r := range recs {
		if r.State != StateQueued {
			continue
		}
		m, merr := s.members.GetMember(ctx, r.UserID, absChatID)
		if merr != nil || m == nil {
			// Can't verify -> do NOT publicly tag blindly; keep queued,
			// retry next run. (Escape from a permanently-stuck campaign is
			// the documented /cleanup stop.)
			continue
		}
		if seenAtOrAfter(m.LastMessageAt, r.SeededAt) || seenAtOrAfter(m.LastReactionAt, r.SeededAt) {
			// Came back on their own since the campaign started - spared,
			// never even tagged.
			_ = s.store.Delete(ctx, absChatID, r.UserID)
			sum.Saved++
			continue
		}
		if m.IsBot || shared.IsAnonymousAdmin(m.UserID) ||
			m.Status == membership.StatusLeft || m.Status == membership.StatusKicked ||
			m.Status == membership.StatusAdministrator || m.Status == membership.StatusCreator {
			// No longer a valid target (left / became admin / bot) - drop.
			_ = s.store.Delete(ctx, absChatID, r.UserID)
			continue
		}
		batch = append(batch, r)
		if len(batch) >= s.cfg.Batch {
			break
		}
	}
	if len(batch) == 0 {
		return sum, nil
	}
	batch = fitOneMessage(batch)
	if len(batch) == 0 {
		return sum, nil
	}
	if err := s.announce(ctx, signed, batch); err != nil {
		// Stays queued, retried next run. Never mark tagged for a warning
		// that never reached the chat (else a silent kick after grace).
		return sum, fmt.Errorf("gracekick: announce: %w", err)
	}
	taggedAt := now.Truncate(time.Second) // match second-granular activity ts
	deadline := taggedAt.Add(s.cfg.Grace)
	for _, r := range batch {
		r.State = StateTagged
		r.TaggedAt = taggedAt
		r.GraceDeadline = deadline
		if perr := s.store.Put(ctx, r); perr != nil {
			s.log.Warn("gracekick: persist tagged failed", "chat", absChatID, "user", r.UserID, "error", perr)
			continue
		}
		sum.Tagged++
	}
	return sum, nil
}

// activeSince reports whether the member wrote OR reacted at/after
// `since` (inclusive - see seenAtOrAfter). determined is false when the
// live membership record cannot be read; the caller must never kick on
// determined=false.
func (s *Service) activeSince(ctx context.Context, userID, absChatID int64, since time.Time) (active, determined bool) {
	m, err := s.members.GetMember(ctx, userID, absChatID)
	if err != nil || m == nil {
		return false, false
	}
	return seenAtOrAfter(m.LastMessageAt, since) || seenAtOrAfter(m.LastReactionAt, since), true
}

// utf16Len returns len(s) in UTF-16 code units - the unit Telegram uses
// for its 4096-char message cap (an emoji is one rune but TWO units).
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

// fitOneMessage trims the batch so the rendered announcement stays well
// under Telegram's 4096 UTF-16-unit limit; the rest are tagged next run.
// At least one is always kept.
func fitOneMessage(batch []Record) []Record {
	const budget = 3000 // headroom under 4096 for preamble + footer
	used := 0
	out := make([]Record, 0, len(batch))
	for _, r := range batch {
		c := utf16Len(mention(r)) + 2 // ", "
		if len(out) > 0 && used+c > budget {
			break
		}
		used += c
		out = append(out, r)
	}
	return out
}

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

// announce posts ONE public message @-mentioning the batch and stating
// the rule. Reply-or-react is asked explicitly because under BotFather
// privacy ON an ordinary message is invisible to the bot while a reply
// to the bot and any reaction are not.
func (s *Service) announce(ctx context.Context, signedChatID int64, batch []Record) error {
	var b strings.Builder
	fmt.Fprintf(&b, "🧹 <b>Чистка неактивных</b>\n\n"+
		"Вы давно не писали и не реагировали в этом чате. Чтобы остаться - "+
		"<b>напишите что-нибудь (можно ответом на это сообщение) или поставьте любую реакцию</b> "+
		"в течение %s. Иначе бот удалит вас из чата (вернуться можно по ссылке).\n\n",
		formatDur(s.cfg.Grace))
	for i, r := range batch {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(mention(r))
	}
	b.WriteString("\n\n<i>Любое сообщение или реакция в чате снимает вас из списка.</i>")

	_, err := s.out.SendMessage(ctx, &telego.SendMessageParams{
		ChatID:    telego.ChatID{ID: signedChatID},
		Text:      b.String(),
		ParseMode: telego.ModeHTML,
	})
	return err
}

// mention renders a single ping: @username when known, else an inline
// tg://user link with an HTML-escaped, length-bounded visible name so
// the tag still notifies a user who has no public @handle.
func mention(r Record) string {
	if r.Username != "" {
		return "@" + r.Username
	}
	name := strings.TrimSpace(r.FirstName)
	if name == "" {
		name = "участник"
	}
	name = truncUTF16(name, 32)
	return fmt.Sprintf(`<a href="tg://user?id=%d">%s</a>`, r.UserID, html.EscapeString(name))
}

// formatDur renders a grace duration in plain Russian ("3 дня").
func formatDur(d time.Duration) string {
	if d%(24*time.Hour) == 0 {
		days := int(d / (24 * time.Hour))
		return fmt.Sprintf("%d %s", days, plural(days, "день", "дня", "дней"))
	}
	hours := int(d / time.Hour)
	return fmt.Sprintf("%d %s", hours, plural(hours, "час", "часа", "часов"))
}

func plural(n int, one, few, many string) string {
	n %= 100
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

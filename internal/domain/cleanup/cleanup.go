package cleanup

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mymmrac/telego"

	"github.com/veschin/bidlobot/internal/domain/membership"
	"github.com/veschin/bidlobot/internal/shared"
)

// MinThreshold is the smallest inactivity window allowed by the
// service, set to 1 day so an admin cannot accidentally cleanup the
// whole chat with /cleanup 1m.
const MinThreshold = 24 * time.Hour

// MaxThreshold caps the input at 5 years for sanity.
const MaxThreshold = 5 * 365 * 24 * time.Hour

var (
	ErrThresholdTooSmall = fmt.Errorf("cleanup threshold must be at least %s", MinThreshold)
	ErrThresholdTooLarge = fmt.Errorf("cleanup threshold must be at most %s", MaxThreshold)
	ErrNoChat            = errors.New("cleanup: chat not registered")
)

// ParsePeriod parses a cleanup/inactivity period: 7d, 30d, 6mo, 1y, or a
// bare Go duration (72h). Months are 30 days and years 365 days - good
// enough for an inactivity window. It is the single source of truth for
// this format; the DM `/cleanup` parser and the daily-cleanup config
// both delegate here so the accepted syntax can never drift between
// surfaces.
func ParsePeriod(s string) (time.Duration, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if num, ok := strings.CutSuffix(s, "mo"); ok {
		n, err := strconv.Atoi(num)
		if err != nil || n <= 0 {
			return 0, fmt.Errorf("bad months: %q", s)
		}
		return time.Duration(n) * 30 * 24 * time.Hour, nil
	}
	if num, ok := strings.CutSuffix(s, "y"); ok {
		n, err := strconv.Atoi(num)
		if err != nil || n <= 0 {
			return 0, fmt.Errorf("bad years: %q", s)
		}
		return time.Duration(n) * 365 * 24 * time.Hour, nil
	}
	if num, ok := strings.CutSuffix(s, "d"); ok {
		n, err := strconv.Atoi(num)
		if err != nil || n <= 0 {
			return 0, fmt.Errorf("bad days: %q", s)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, err
	}
	return d, nil
}

// Preview is what the cleanup preview message renders. It deliberately
// includes ObservationWindow so the user can see how partial the data
// is - the bot only sees activity from members it has observed since
// InstalledAt.
type Preview struct {
	AbsChatID         int64
	Threshold         time.Duration
	ObservationWindow time.Duration // now - InstalledAt; zero if chat not registered
	InstalledAt       time.Time
	KnownMembers      int

	// Candidates is the actionable list: members the bot has actually
	// OBSERVED (a message or reaction, live or imported) whose last
	// recorded activity is older than the cutoff. Only these carry real
	// inactivity evidence; ExecuteCleanup and the daily lifecycle act on
	// this list and nothing else.
	Candidates []membership.Member

	// NoEvidence is members that clear the not-bot / not-admin filter but
	// for whom the bot has NEVER recorded a single message or reaction
	// (all timestamps and counters zero). This is the dominant class
	// right after a partial-window import: join-only members, or
	// react-only members whose reactions predate the bot (the export
	// carries no reactions and no usernames). Absence of data is NOT
	// evidence of inactivity, so these are surfaced for manual review,
	// never placed in Candidates, and never auto-kicked.
	NoEvidence []membership.Member

	// ThresholdExceedsWindow is true when the requested threshold is
	// longer than the window the bot actually has data for
	// (ObservationWindow). When true, "no recorded activity in the
	// window" cannot be honestly read as "inactive for the threshold" -
	// the renderer must warn loudly and the NoEvidence split matters most.
	ThresholdExceedsWindow bool

	Now time.Time
}

// Outcome describes the per-target result of a kick attempt. Aggregated
// into Report by ExecuteCleanup.
type Outcome string

const (
	OutcomeKicked         Outcome = "kicked"
	OutcomeSkippedAdmin   Outcome = "skipped_admin"
	OutcomeSkippedBot     Outcome = "skipped_bot"
	OutcomeSkippedAlready Outcome = "skipped_already_left"
	OutcomeFailed         Outcome = "failed"
)

type ExecutionEntry struct {
	UserID   int64
	Display  string
	Outcome  Outcome
	APIError string
}

type Report struct {
	AbsChatID  int64
	Total      int
	Kicked     int
	Skipped    int
	Failed     int
	StartedAt  time.Time
	FinishedAt time.Time
	Entries    []ExecutionEntry
}

type Service struct {
	members membership.Store
	api     shared.TelegramAPI
	log     *slog.Logger

	// kickInterval throttles execution - one kick per interval. Defaults
	// to 2s, which leaves plenty of headroom under Telegram's 30 req/s
	// global limit while still letting a 200-candidate cleanup finish in
	// under 7 minutes.
	kickInterval time.Duration
}

func NewService(members membership.Store, api shared.TelegramAPI, log *slog.Logger) *Service {
	return &Service{
		members:      members,
		api:          api,
		log:          log,
		kickInterval: 2 * time.Second,
	}
}

// SetKickInterval lets tests override the throttle so they run fast.
func (s *Service) SetKickInterval(d time.Duration) {
	s.kickInterval = d
}

// PreviewInactive returns the candidates for cleanup without performing
// any moderation action. ChatNotRegistered is reported via Preview.InstalledAt
// being zero - in that case ObservationWindow is also zero and the
// caller should warn the admin that the bot has no recorded
// installation timestamp (e.g. it was added before this code shipped).
func (s *Service) PreviewInactive(ctx context.Context, absChatID int64, threshold time.Duration, now time.Time) (*Preview, error) {
	if threshold < MinThreshold {
		return nil, ErrThresholdTooSmall
	}
	if threshold > MaxThreshold {
		return nil, ErrThresholdTooLarge
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}

	chat, _ := s.members.GetChat(ctx, absChatID)
	all, err := s.members.ListByChat(ctx, absChatID)
	if err != nil {
		return nil, err
	}

	cutoff := now.Add(-threshold)
	stale := make([]membership.Member, 0)
	noEvidence := make([]membership.Member, 0)
	for _, m := range all {
		if !s.isInactive(m, cutoff) {
			continue
		}
		// Split "we watched them go quiet" (real evidence) from "we have
		// never seen them at all" (a data gap, not proof of silence).
		// Conflating the two is the bug that listed import-only members
		// as confident kick targets.
		if everObserved(m) {
			stale = append(stale, m)
		} else {
			noEvidence = append(noEvidence, m)
		}
	}

	// Sort each list by least recent LastSeenAt first - the most clearly
	// inactive users appear at the top of the preview. (NoEvidence rows
	// all have a zero LastSeenAt; the sort is stable and harmless there.)
	byLeastRecent := func(ms []membership.Member) {
		sort.Slice(ms, func(i, j int) bool {
			return ms[i].LastSeenAt.Before(ms[j].LastSeenAt)
		})
	}
	byLeastRecent(stale)
	byLeastRecent(noEvidence)

	preview := &Preview{
		AbsChatID:    absChatID,
		Threshold:    threshold,
		KnownMembers: len(all),
		Candidates:   stale,
		NoEvidence:   noEvidence,
		Now:          now,
	}
	if chat != nil {
		preview.InstalledAt = chat.InstalledAt
		if !chat.InstalledAt.IsZero() {
			preview.ObservationWindow = now.Sub(chat.InstalledAt)
			preview.ThresholdExceedsWindow = threshold > preview.ObservationWindow
		}
	}
	return preview, nil
}

// everObserved reports whether the bot has ANY recorded activity for this
// member - a message or a reaction, live or imported. When false the
// member is a pure data gap (join-only, or react-only before the bot was
// watching): the bot has zero evidence of (in)activity, only an absence
// of data, and must not present them as a confident kick target.
func everObserved(m membership.Member) bool {
	return !m.LastMessageAt.IsZero() || !m.LastReactionAt.IsZero() ||
		m.MessageCount > 0 || m.ReactionCount > 0
}

// isInactive returns true when the member is a regular non-bot, non-admin
// user whose last message AND last reaction are both before the cutoff
// (or never recorded at all).
func (s *Service) isInactive(m membership.Member, cutoff time.Time) bool {
	if m.IsBot {
		return false
	}
	if shared.IsAnonymousAdmin(m.UserID) {
		return false
	}
	switch m.Status {
	case membership.StatusAdministrator, membership.StatusCreator,
		membership.StatusKicked, membership.StatusLeft:
		return false
	}
	if !m.LastMessageAt.IsZero() && m.LastMessageAt.After(cutoff) {
		return false
	}
	if !m.LastReactionAt.IsZero() && m.LastReactionAt.After(cutoff) {
		return false
	}
	return true
}

// ExecuteCleanup walks the candidate list and kicks each one with a
// fresh getChatMember pre-check. progress is invoked after every entry
// so the caller can update an in-chat status message.
//
// chatID here is Telegram's signed chat ID (e.g. -1001234567890), not
// the absolute one stored in membership records, because all telego API
// calls expect the signed value.
func (s *Service) ExecuteCleanup(ctx context.Context, chatID int64, candidates []membership.Member, progress func(done, total int, last ExecutionEntry)) (*Report, error) {
	report := &Report{
		AbsChatID: absChatIDOf(chatID),
		Total:     len(candidates),
		StartedAt: time.Now().UTC(),
		Entries:   make([]ExecutionEntry, 0, len(candidates)),
	}

	for i, m := range candidates {
		entry := s.kickOne(ctx, chatID, m)
		report.Entries = append(report.Entries, entry)
		switch entry.Outcome {
		case OutcomeKicked:
			report.Kicked++
		case OutcomeFailed:
			report.Failed++
		default:
			report.Skipped++
		}
		if progress != nil {
			progress(i+1, len(candidates), entry)
		}
		if i+1 < len(candidates) {
			select {
			case <-ctx.Done():
				report.FinishedAt = time.Now().UTC()
				return report, ctx.Err()
			case <-time.After(s.kickInterval):
			}
		}
	}

	report.FinishedAt = time.Now().UTC()
	return report, nil
}

// kickOne is the single-target sequence: pre-check membership status,
// ban, immediately unban with only_if_banned=true. We tolerate the
// pre-check failing (e.g. user already gone from chat) by skipping
// rather than failing.
func (s *Service) kickOne(ctx context.Context, chatID int64, m membership.Member) ExecutionEntry {
	display := shared.UserDisplay(m.Username, m.FirstName)
	if display == "" {
		display = fmt.Sprintf("user %d", m.UserID)
	}
	entry := ExecutionEntry{UserID: m.UserID, Display: display}

	current, err := s.api.GetChatMember(ctx, &telego.GetChatMemberParams{
		ChatID: telego.ChatID{ID: chatID},
		UserID: m.UserID,
	})
	if err != nil {
		entry.Outcome = OutcomeFailed
		entry.APIError = "getChatMember: " + err.Error()
		return entry
	}
	if current != nil {
		switch current.MemberStatus() {
		case "administrator", "creator":
			entry.Outcome = OutcomeSkippedAdmin
			return entry
		case "kicked":
			entry.Outcome = OutcomeSkippedAlready
			return entry
		case "left":
			entry.Outcome = OutcomeSkippedAlready
			return entry
		}
		if u := current.MemberUser(); u.IsBot {
			entry.Outcome = OutcomeSkippedBot
			return entry
		}
	}

	if err := s.api.BanChatMember(ctx, &telego.BanChatMemberParams{
		ChatID:         telego.ChatID{ID: chatID},
		UserID:         m.UserID,
		RevokeMessages: false,
	}); err != nil {
		entry.Outcome = OutcomeFailed
		entry.APIError = "banChatMember: " + err.Error()
		return entry
	}

	if err := s.api.UnbanChatMember(ctx, &telego.UnbanChatMemberParams{
		ChatID:       telego.ChatID{ID: chatID},
		UserID:       m.UserID,
		OnlyIfBanned: true,
	}); err != nil {
		// We already banned successfully; record the unban failure but
		// still count this as a kick - the user is removed from the chat.
		// Operator can rerun unban manually if they care about the
		// permanent-ban side effect.
		entry.Outcome = OutcomeKicked
		entry.APIError = "unbanChatMember: " + err.Error()
		s.log.Warn("cleanup: unban after ban failed",
			"chat_id", chatID, "user_id", m.UserID, "error", err)
		return entry
	}

	entry.Outcome = OutcomeKicked
	return entry
}

func absChatIDOf(chatID int64) int64 {
	if chatID < 0 {
		return -chatID
	}
	return chatID
}

// signedChatIDOf is the inverse of absChatIDOf: membership records store
// the absolute id, every telego API call wants Telegram's signed id
// (-100...). Matches dmSignedChat in the bot package.
func signedChatIDOf(absChatID int64) int64 { return -absChatID }

// ResolvedMember is a candidate after a best-effort live getChatMember
// lookup. It exists because a Telegram Desktop export carries no
// usernames and no display name for join-only members, so an
// import-seeded candidate is stored as a bare numeric id. Showing the
// admin "id 1250985701" defeats the human-in-the-loop confirm; we resolve
// the real identity (and current status) before rendering or tagging.
type ResolvedMember struct {
	membership.Member

	// Resolved is true when a human-readable identity is known - either
	// it was already stored, or getChatMember returned a name/username.
	Resolved bool
	// Present is false when getChatMember reports the user already left
	// or was kicked (no point listing/kicking them), or when the lookup
	// failed (status unknown - shown honestly, never silently dropped).
	Present bool
	// Protected is true for admins/creators/bots: never a kick target.
	Protected bool
}

// ResolveIdentities resolves human-readable identity and current status
// for members that have no stored name, via getChatMember. Members that
// already carry a Username or FirstName are returned as-is with no API
// call. The lookup is bounded: maxAPILookups caps the number of API
// calls (<=0 means "resolve all", still clamped to an internal hard cap)
// so a 5000-ghost chat cannot turn one preview into 5000 API calls -
// callers pass only the slice they will display or act on.
//
// Order is preserved. A member whose lookup fails is kept (Resolved
// false, Present false) so the renderer can say "id N - не удалось
// проверить" rather than silently losing them.
func (s *Service) ResolveIdentities(ctx context.Context, absChatID int64, in []membership.Member, maxAPILookups int) []ResolvedMember {
	const hardCap = 100
	if maxAPILookups <= 0 || maxAPILookups > hardCap {
		maxAPILookups = hardCap
	}
	signed := signedChatIDOf(absChatID)

	out := make([]ResolvedMember, 0, len(in))
	used := 0
	for _, m := range in {
		rm := ResolvedMember{Member: m, Present: true}

		if m.Username != "" || strings.TrimSpace(m.FirstName) != "" {
			rm.Resolved = true
			out = append(out, rm)
			continue
		}
		if used >= maxAPILookups {
			rm.Resolved = false
			rm.Present = false // unknown - rendered honestly, not dropped
			out = append(out, rm)
			continue
		}
		used++

		cm, err := s.api.GetChatMember(ctx, &telego.GetChatMemberParams{
			ChatID: telego.ChatID{ID: signed},
			UserID: m.UserID,
		})
		if err != nil || cm == nil {
			rm.Resolved = false
			rm.Present = false
			out = append(out, rm)
			continue
		}

		switch cm.MemberStatus() {
		case "left", "kicked":
			rm.Present = false
		case "administrator", "creator":
			rm.Protected = true
		}
		u := cm.MemberUser()
		if u.IsBot {
			rm.Protected = true
		}
		rm.Member.Username = u.Username
		rm.Member.FirstName = u.FirstName
		rm.Member.IsBot = u.IsBot
		rm.Resolved = u.Username != "" || strings.TrimSpace(u.FirstName) != ""
		out = append(out, rm)
	}
	return out
}

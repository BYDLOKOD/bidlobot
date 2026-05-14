package cleanup

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
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

// Preview is what the inline preview message renders. It deliberately
// includes ObservationWindow so the user can see how partial the data
// is - the bot only sees activity from members it has observed since
// InstalledAt.
type Preview struct {
	AbsChatID         int64
	Threshold         time.Duration
	ObservationWindow time.Duration // now - InstalledAt; zero if chat not registered
	InstalledAt       time.Time
	KnownMembers      int
	Candidates        []membership.Member
	Now               time.Time
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
	candidates := make([]membership.Member, 0)
	for _, m := range all {
		if !s.isInactive(m, cutoff) {
			continue
		}
		candidates = append(candidates, m)
	}

	// Sort by least recent LastSeenAt first - the most clearly inactive
	// users appear at the top of the preview.
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].LastSeenAt.Before(candidates[j].LastSeenAt)
	})

	preview := &Preview{
		AbsChatID:    absChatID,
		Threshold:    threshold,
		KnownMembers: len(all),
		Candidates:   candidates,
		Now:          now,
	}
	if chat != nil {
		preview.InstalledAt = chat.InstalledAt
		if !chat.InstalledAt.IsZero() {
			preview.ObservationWindow = now.Sub(chat.InstalledAt)
		}
	}
	return preview, nil
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

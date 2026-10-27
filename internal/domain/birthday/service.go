package birthday

import (
	"context"
	"fmt"
	"html"
	"log/slog"
	"strings"
	"time"

	"github.com/mymmrac/telego"

	"github.com/veschin/bidlobot/internal/domain/membership"
	"github.com/veschin/bidlobot/internal/text"
)

// Moscow is UTC+3 all year. The project uses Europe/Moscow as the day
// boundary (see docs/llm/30_stats.md); a fixed zone avoids a tzdata
// dependency in the distroless-ish runtime image.
var moscow = time.FixedZone("MSK", 3*60*60)

const (
	dailyHourMSK   = 10
	catchUpHourMSK = 20
	markerTTL      = 48 * time.Hour
	maxNamesPerMsg = 10

	// DefaultPause spaces out GetChat calls so a large chat does not
	// produce an API burst. Tests pass Config.Pause = 0.
	DefaultPause = 50 * time.Millisecond
)

// API is the narrow Telegram surface the daily job needs.
type API interface {
	GetChat(ctx context.Context, params *telego.GetChatParams) (*telego.ChatFullInfo, error)
	GetChatMember(ctx context.Context, params *telego.GetChatMemberParams) (telego.ChatMember, error)
	SendMessage(ctx context.Context, params *telego.SendMessageParams) (*telego.Message, error)
}

// Markers records which members were already greeted on a given day.
type Markers interface {
	IsGreeted(ctx context.Context, absChatID, userID int64, day string) (bool, error)
	MarkGreeted(ctx context.Context, absChatID, userID int64, day string, at time.Time) error
	ClearGreeted(ctx context.Context, absChatID, userID int64, day string) error
	Prune(ctx context.Context, absChatID int64, before time.Time) error
}

// Config carries the test seams. Zero values mean production behavior.
type Config struct {
	Now   func() time.Time // nil -> time.Now
	Pause time.Duration    // between GetChat calls; zero keeps tests fast
}

// Service runs the daily birthday check. It reads the birth date from
// the Telegram profile (ChatFullInfo.birthdate), so it stores no date of
// birth - only a short-lived "already greeted" marker.
type Service struct {
	members membership.Store
	api     API
	markers Markers
	log     *slog.Logger
	now     func() time.Time
	pause   time.Duration
}

// NewService wires the membership store, Telegram client, and marker
// store. A nil logger falls back to slog.Default.
func NewService(members membership.Store, api API, markers Markers, log *slog.Logger, cfg Config) *Service {
	if log == nil {
		log = slog.Default()
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &Service{
		members: members,
		api:     api,
		markers: markers,
		log:     log,
		now:     now,
		pause:   cfg.Pause,
	}
}

// Run executes the daily check until ctx is cancelled. A startup
// catch-up runs when the bot comes up after today's slot but before
// catchUpHourMSK; the marker bucket makes the catch-up idempotent.
func (s *Service) Run(ctx context.Context) {
	now := s.now().In(moscow)
	if now.Hour() >= dailyHourMSK && now.Hour() < catchUpHourMSK {
		s.RunOnce(ctx, now)
	}
	for {
		next := nextRun(s.now().In(moscow))
		timer := time.NewTimer(time.Until(next))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		s.RunOnce(ctx, s.now().In(moscow))
	}
}

// RunOnce checks every installed chat once for today's birthdays. It is
// exported so tests and the catch-up path can drive a single pass.
func (s *Service) RunOnce(ctx context.Context, now time.Time) {
	now = now.In(moscow)
	day := now.Format("2006-01-02")
	chats, err := s.members.ListChats(ctx)
	if err != nil {
		s.log.Warn("birthday: list chats failed", "error", err)
		return
	}
	for _, chat := range chats {
		if ctx.Err() != nil {
			return
		}
		// Prune even for a chat the bot already left: no later pass
		// would visit it, and its markers are dead weight.
		if err := s.markers.Prune(ctx, chat.AbsChatID, now.Add(-markerTTL)); err != nil {
			s.log.Warn("birthday: prune markers failed",
				"chat_id", chat.AbsChatID, "error", err)
		}
		if !botIsCurrent(chat.BotStatus) {
			continue
		}
		s.runChat(ctx, chat.AbsChatID, day, now)
	}
}

func (s *Service) runChat(ctx context.Context, absChatID int64, day string, now time.Time) {
	members, err := s.members.ListByChat(ctx, absChatID)
	if err != nil {
		s.log.Warn("birthday: list members failed", "chat_id", absChatID, "error", err)
		return
	}

	checked := 0
	candidates := make([]candidate, 0)
	for _, m := range members {
		if ctx.Err() != nil {
			return
		}
		if m.UserID == 0 || m.IsBot {
			continue
		}
		if m.Status == membership.StatusLeft || m.Status == membership.StatusKicked {
			continue
		}
		greeted, err := s.markers.IsGreeted(ctx, absChatID, m.UserID, day)
		if err != nil {
			s.log.Warn("birthday: marker lookup failed",
				"chat_id", absChatID, "user_id", m.UserID, "error", err)
			continue
		}
		if greeted {
			continue
		}

		checked++
		info, err := s.api.GetChat(ctx, &telego.GetChatParams{
			ChatID: telego.ChatID{ID: m.UserID},
		})
		if err != nil {
			s.log.Debug("birthday: getChat failed",
				"chat_id", absChatID, "user_id", m.UserID, "error", err)
			s.pauseBetween(ctx)
			continue
		}
		if info == nil || !matches(info.Birthdate, now) {
			s.pauseBetween(ctx)
			continue
		}

		cm, err := s.api.GetChatMember(ctx, &telego.GetChatMemberParams{
			ChatID: telego.ChatID{ID: -absChatID},
			UserID: m.UserID,
		})
		if err != nil {
			s.log.Warn("birthday: getChatMember failed",
				"chat_id", absChatID, "user_id", m.UserID, "error", err)
			continue
		}
		if !isMember(cm) {
			continue
		}

		candidates = append(candidates, candidate{userID: m.UserID, name: displayName(m)})
		s.pauseBetween(ctx)
	}

	sent := 0
	for start := 0; start < len(candidates); start += maxNamesPerMsg {
		end := min(start+maxNamesPerMsg, len(candidates))
		if s.sendChunk(ctx, absChatID, day, candidates[start:end], now) {
			sent++
		}
	}
	if checked > 0 || sent > 0 {
		s.log.Info("birthday: chat run",
			"chat_id", absChatID,
			"checked", checked,
			"matched", len(candidates),
			"messages", sent)
	}
}

type candidate struct {
	userID int64
	name   string
}

func displayName(m membership.Member) string {
	// A history-import member's FirstName is the exporting account's
	// private address-book label, not the user's own profile name
	// (docs/llm/30_stats.md). Never publish it; fall back to the
	// neutral label until a live event supplies a real name.
	if m.KnownVia != membership.SourceImport && m.FirstName != "" {
		return html.EscapeString(m.FirstName)
	}
	if m.Username != "" {
		return html.EscapeString(m.Username)
	}
	return "именинник"
}

func mention(c candidate) string {
	return fmt.Sprintf(`<a href="tg://user?id=%d">%s</a>`, c.userID, c.name)
}

func (s *Service) sendChunk(ctx context.Context, absChatID int64, day string, chunk []candidate, now time.Time) bool {
	mentions := make([]string, len(chunk))
	for i, c := range chunk {
		mentions[i] = mention(c)
	}
	body := fmt.Sprintf(text.MsgBirthdayOne, mentions[0])
	if len(mentions) > 1 {
		body = fmt.Sprintf(text.MsgBirthdayMany, joinNames(mentions))
	}

	// Mark before sending: a crash between the marker and the send
	// loses one greeting instead of duplicating it.
	marked := 0
	for _, c := range chunk {
		if err := s.markers.MarkGreeted(ctx, absChatID, c.userID, day, now); err != nil {
			s.log.Warn("birthday: mark greeted failed",
				"chat_id", absChatID, "user_id", c.userID, "error", err)
			for i := range marked {
				_ = s.markers.ClearGreeted(ctx, absChatID, chunk[i].userID, day)
			}
			return false
		}
		marked++
	}

	if _, err := s.api.SendMessage(ctx, &telego.SendMessageParams{
		ChatID:    telego.ChatID{ID: -absChatID},
		Text:      body,
		ParseMode: telego.ModeHTML,
	}); err != nil {
		s.log.Warn("birthday: send failed", "chat_id", absChatID, "error", err)
		for _, c := range chunk {
			if cerr := s.markers.ClearGreeted(ctx, absChatID, c.userID, day); cerr != nil {
				s.log.Warn("birthday: clear marker failed",
					"chat_id", absChatID, "user_id", c.userID, "error", cerr)
			}
		}
		return false
	}
	return true
}

func (s *Service) pauseBetween(ctx context.Context) {
	if s.pause <= 0 {
		return
	}
	timer := time.NewTimer(s.pause)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

// nextRun returns the next 10:00 Moscow instant strictly after now.
func nextRun(now time.Time) time.Time {
	now = now.In(moscow)
	run := time.Date(now.Year(), now.Month(), now.Day(), dailyHourMSK, 0, 0, 0, moscow)
	if !run.After(now) {
		run = run.Add(24 * time.Hour)
	}
	return run
}

// matches reports whether the profile birth date falls on today's
// Moscow date. A 29 February birthday is greeted on 28 February in a
// non-leap year.
func matches(b *telego.Birthdate, now time.Time) bool {
	if b == nil {
		return false
	}
	now = now.In(moscow)
	if b.Day == now.Day() && b.Month == int(now.Month()) {
		return true
	}
	return now.Month() == time.February && now.Day() == 28 &&
		!isLeapYear(now.Year()) && b.Day == 29 && b.Month == int(time.February)
}

func isLeapYear(y int) bool {
	return y%4 == 0 && (y%100 != 0 || y%400 == 0)
}

func isMember(cm telego.ChatMember) bool {
	if cm == nil {
		return false
	}
	switch cm.MemberStatus() {
	case telego.MemberStatusLeft, telego.MemberStatusBanned:
		return false
	case telego.MemberStatusRestricted:
		r, ok := cm.(*telego.ChatMemberRestricted)
		return ok && r.IsMember
	default:
		return true
	}
}

func botIsCurrent(status membership.Status) bool {
	return status != membership.StatusLeft && status != membership.StatusKicked
}

func joinNames(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	default:
		return strings.Join(names[:len(names)-1], ", ") + " и " + names[len(names)-1]
	}
}

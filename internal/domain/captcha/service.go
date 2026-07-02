package captcha

import (
	"html"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/mymmrac/telego"

	"github.com/veschin/bidlobot/internal/shared"
	"github.com/veschin/bidlobot/internal/text"
)

// Service drives the captcha lifecycle: post on join, resolve on answer,
// kick on timeout. It owns every Telegram call (send, edit, restrict, kick,
// toast) so the bot handler layer is a thin routing wrapper.
type Service struct {
	store   Store
	api     shared.TelegramAPI
	log     *slog.Logger
	timeout time.Duration
}

// NewService wires the store and the rate-limited Telegram client. timeout
// is how long a newcomer has to answer before the sweeper kicks them.
func NewService(store Store, api shared.TelegramAPI, log *slog.Logger, timeout time.Duration) *Service {
	return &Service{store: store, api: api, log: log, timeout: timeout}
}

// Timeout returns the configured answer window. App uses it to size the
// sweep interval so the gap between expiry and kick stays small.
func (s *Service) Timeout() time.Duration { return s.timeout }

// OnJoin handles a new chat member. It clears any prior challenge first
// (rejoin edge case), generates a fresh puzzle, mutes the newcomer, then
// posts the captcha with answer buttons and persists it (so the callback
// can resolve it). The mute is defense-in-depth: if the bot lacks
// CanRestrict it is logged and skipped, and the captcha+kick still work
// via the buttons.
func (s *Service) OnJoin(ctx context.Context, user telego.User, signedChatID, absChatID int64, now time.Time) error {
	// Rejoin guard: a user kicked mid-captcha who rejoins gets a fresh
	// challenge. The old one MUST be deleted or the sweeper would later
	// kick them on the expired first challenge while they solve the new
	// one. A Delete failure is a bbolt error; bail rather than risk two
	// concurrent challenges for one user.
	if old, err := s.store.GetByUser(ctx, absChatID, user.ID); err == nil && old != nil {
		if derr := s.store.Delete(ctx, old.ID); derr != nil {
			return fmt.Errorf("delete stale challenge: %w", derr)
		}
	} else if err != nil && !errors.Is(err, ErrNotFound) {
		return fmt.Errorf("lookup stale challenge: %w", err)
	}

	c := Generate(user.ID, absChatID, now, s.timeout)
	c.Username = user.Username
	c.FirstName = user.FirstName

	// Mute BEFORE posting the captcha so there is no window where the
	// newcomer can send messages. Best-effort: a failure (bot demoted
	// mid-operation) is logged at WARN; captcha + kick still work.
	s.mute(ctx, absChatID, user.ID, now)

	mention := renderMention(user.Username, user.FirstName, user.ID)
	body := fmt.Sprintf("%s\n\n<b>%s = ?</b>\n\n%s",
		fmt.Sprintf(text.MsgCaptchaGreeting, mention),
		c.Question,
		timeoutLine(s.timeout))

	msg, err := s.api.SendMessage(ctx, &telego.SendMessageParams{
		ChatID:      telego.ChatID{ID: signedChatID},
		Text:        body,
		ParseMode:   telego.ModeHTML,
		ReplyMarkup: answerKeyboard(c.ID, c.Answers),
	})
	if err != nil {
		return fmt.Errorf("send captcha message: %w", err)
	}
	if msg == nil {
		return errors.New("send captcha message: nil response")
	}

	c.MessageID = msg.MessageID
	if err := s.store.Create(ctx, c); err != nil {
		// The message is already public; the challenge is unresolvable
		// (buttons will toast "expired"). Surface the store error so the
		// caller knows the join was not fully handled.
		return fmt.Errorf("persist challenge: %w", err)
	}

	return nil
}

// OnAnswer resolves a button tap. It answers the callback in EVERY path so
// the button spinner never hangs. Wrong answer: toast + kick (rejoinable).
// Correct answer: clear + welcome + unmute.
func (s *Service) OnAnswer(ctx context.Context, query telego.CallbackQuery, challengeID string, answer int) error {
	c, err := s.store.Get(ctx, challengeID)
	if err != nil {
		s.answer(ctx, query.ID, text.MsgCaptchaExpired)
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		return err
	}
	if query.From.ID != c.UserID {
		s.answer(ctx, query.ID, text.MsgCaptchaNotYours)
		return nil
	}
	if answer != c.CorrectAnswer {
		// Wrong answer: toast, then kick (rejoinable). Same kick sequence
		// as the timeout sweeper but driven by the callback, not a tick.
		s.answer(ctx, query.ID, text.MsgCaptchaWrong)
		if kerr := s.kick(ctx, *c); kerr != nil {
			s.log.Warn("captcha: wrong-answer kick failed", "challenge", c.ID, "error", kerr)
			return nil // leave challenge for sweeper retry
		}
		s.editResolved(ctx, *c, text.MsgCaptchaKicked)
		if derr := s.store.Delete(ctx, c.ID); derr != nil {
			s.log.Warn("captcha: delete after wrong-answer kick failed", "challenge", c.ID, "error", derr)
		}
		return nil
	}

	// Correct: clear, announce, unmute.
	if derr := s.store.Delete(ctx, c.ID); derr != nil {
		s.log.Warn("captcha: delete on solve failed", "challenge", c.ID, "error", derr)
	}
	s.editResolved(ctx, *c, text.MsgCaptchaSolved)
	s.unmute(ctx, c.AbsChatID, c.UserID)
	s.answer(ctx, query.ID, "") // clear the spinner, no toast
	return nil
}

// Sweep kicks every user whose captcha expired unanswered, rewrites each
// announcement as a "kicked" notice, and deletes the challenge. One
// failure (kick, edit, or delete) is logged and never aborts the rest.
func (s *Service) Sweep(ctx context.Context, now time.Time) error {
	expired, err := s.store.ListExpired(ctx, now)
	if err != nil {
		return fmt.Errorf("list expired: %w", err)
	}
	for _, c := range expired {
		if kerr := s.kick(ctx, c); kerr != nil {
			s.log.Warn("captcha sweep: kick failed",
				"challenge", c.ID, "user_id", c.UserID, "error", kerr)
			continue
		}
		s.editResolved(ctx, c, text.MsgCaptchaKicked)
		if derr := s.store.Delete(ctx, c.ID); derr != nil {
			s.log.Warn("captcha sweep: delete failed",
				"challenge", c.ID, "error", derr)
		}
	}
	return nil
}

// kick is the ban+unban sequence (rejoinable kick). It re-checks live
// status first and skips members who are no longer normal members (left,
// promoted, already kicked) so a race with a manual action is a no-op.
// Implemented inline so the captcha domain has no dependency on the
// cleanup or membership domains.
func (s *Service) kick(ctx context.Context, c Challenge) error {
	signedID := -c.AbsChatID
	if cur, err := s.api.GetChatMember(ctx, &telego.GetChatMemberParams{
		ChatID: telego.ChatID{ID: signedID}, UserID: c.UserID,
	}); err == nil && cur != nil {
		switch cur.MemberStatus() {
		case "administrator", "creator", "kicked", "left":
			return nil // promoted or already gone - nothing to kick
		}
	}
	if err := s.api.BanChatMember(ctx, &telego.BanChatMemberParams{
		ChatID:         telego.ChatID{ID: signedID},
		UserID:         c.UserID,
		RevokeMessages: false,
	}); err != nil {
		return fmt.Errorf("ban: %w", err)
	}
	if err := s.api.UnbanChatMember(ctx, &telego.UnbanChatMemberParams{
		ChatID:       telego.ChatID{ID: signedID},
		UserID:       c.UserID,
		OnlyIfBanned: true,
	}); err != nil {
		// User is already removed (banned); the unban only matters if
		// the operator cares about the ban being permanent. Count as done.
		s.log.Warn("captcha: unban after ban failed", "user_id", c.UserID, "error", err)
	}
	return nil
}

// mute silences the newcomer for the timeout window. All send permissions
// off, UntilDate = now + timeout so Telegram auto-lifts it even if the bot
// is removed before the sweep runs.
func (s *Service) mute(ctx context.Context, absChatID, userID int64, now time.Time) {
	signedID := -absChatID
	if err := s.api.RestrictChatMember(ctx, &telego.RestrictChatMemberParams{
		ChatID:      telego.ChatID{ID: signedID},
		UserID:      userID,
		Permissions: sendPerms(false),
		UntilDate:   now.Add(s.timeout).Unix(),
	}); err != nil {
		s.log.Warn("captcha: mute on join failed (bot may lack CanRestrict)",
			"user_id", userID, "error", err)
	}
}

// unmute restores the chat's default permissions. Best-effort: a failure
// is logged (the user already solved the captcha; an admin can unmute).
func (s *Service) unmute(ctx context.Context, absChatID, userID int64) {
	signedID := -absChatID
	perms, err := chatDefaultPerms(ctx, s.api, signedID)
	if err != nil {
		s.log.Warn("captcha: unmute getChat failed", "user_id", userID, "error", err)
		return
	}
	if err := s.api.RestrictChatMember(ctx, &telego.RestrictChatMemberParams{
		ChatID:      telego.ChatID{ID: signedID},
		UserID:      userID,
		Permissions: perms,
	}); err != nil {
		s.log.Warn("captcha: unmute restrict failed", "user_id", userID, "error", err)
	}
}

// chatDefaultPerms reads the chat's default permissions so unmute restores
// exactly what the group allows (a restricted group is not over-granted).
func chatDefaultPerms(ctx context.Context, api shared.TelegramAPI, signedID int64) (telego.ChatPermissions, error) {
	chat, err := api.GetChat(ctx, &telego.GetChatParams{ChatID: telego.ChatID{ID: signedID}})
	if err != nil {
		return telego.ChatPermissions{}, err
	}
	if chat.Permissions != nil {
		return *chat.Permissions, nil
	}
	return sendPerms(true), nil // unrestricted chat: allow everything
}

// editResolved rewrites the announcement message. tmpl is a printf format
// whose single %s is the user mention. The keyboard is removed.
func (s *Service) editResolved(ctx context.Context, c Challenge, tmpl string) {
	_, err := s.api.EditMessageText(ctx, &telego.EditMessageTextParams{
		ChatID:      telego.ChatID{ID: -c.AbsChatID},
		MessageID:   c.MessageID,
		Text:        fmt.Sprintf(tmpl, renderMention(c.Username, c.FirstName, c.UserID)),
		ParseMode:   telego.ModeHTML,
		ReplyMarkup: &telego.InlineKeyboardMarkup{InlineKeyboard: [][]telego.InlineKeyboardButton{}},
	})
	if err != nil {
		s.log.Warn("captcha: edit resolved notice failed", "challenge", c.ID, "error", err)
	}
}

// answer clears the button spinner, optionally showing a short toast.
func (s *Service) answer(ctx context.Context, queryID, msg string) {
	if queryID == "" {
		return
	}
	_ = s.api.AnswerCallbackQuery(ctx, &telego.AnswerCallbackQueryParams{
		CallbackQueryID: queryID,
		Text:            msg,
	})
}

// answerKeyboard builds one row of buttons, one per answer choice. The
// callback_data carries the challenge id and the answer value so the
// handler can resolve a tap without a store round-trip to know the options.
func answerKeyboard(challengeID string, answers []int) *telego.InlineKeyboardMarkup {
	row := make([]telego.InlineKeyboardButton, len(answers))
	for i, a := range answers {
		row[i] = telego.InlineKeyboardButton{
			Text:         strconv.Itoa(a),
			CallbackData: fmt.Sprintf("cap:ans:%s:%d", challengeID, a),
		}
	}
	return &telego.InlineKeyboardMarkup{InlineKeyboard: [][]telego.InlineKeyboardButton{row}}
}

// renderMention renders an HTML mention: @username when known, else an
// inline tg://user link with an escaped, length-bounded visible name so a
// user with no public @handle is still pinged.
func renderMention(username, firstName string, userID int64) string {
	if username != "" {
		return "@" + username
	}
	name := strings.TrimSpace(firstName)
	if name == "" {
		name = "участник"
	}
	if r := []rune(name); len(r) > 32 {
		name = string(r[:32])
	}
	return fmt.Sprintf(`<a href="tg://user?id=%d">%s</a>`, userID, html.EscapeString(name))
}

// sendPerms returns a ChatPermissions with every send capability set to v.
// Management perms (info/invite/pin/topics) are left nil so a mute does not
// strip rights the user never had, and an unmute restores the chat default.
func sendPerms(v bool) telego.ChatPermissions {
	return telego.ChatPermissions{
		CanSendMessages:       &v,
		CanSendAudios:         &v,
		CanSendDocuments:      &v,
		CanSendPhotos:         &v,
		CanSendVideos:         &v,
		CanSendVideoNotes:     &v,
		CanSendVoiceNotes:     &v,
		CanSendPolls:          &v,
		CanSendOtherMessages:  &v,
		CanAddWebPagePreviews: &v,
	}
}

// timeoutLine renders the answer window in plain Russian for the join
// message ("У вас есть 10 минут на ответ.").
func timeoutLine(d time.Duration) string {
	min := int(d / time.Minute)
	if min < 1 {
		min = 1
	}
	return fmt.Sprintf("У вас есть %d %s на ответ.", min, pluralRU(min, "минута", "минуты", "минут"))
}

func pluralRU(n int, one, few, many string) string {
	if n%100 >= 11 && n%100 <= 14 {
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

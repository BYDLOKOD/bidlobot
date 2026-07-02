package bot

import (
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"

	"github.com/veschin/bidlobot/internal/domain/captcha"
	"github.com/veschin/bidlobot/internal/shared"
	"github.com/veschin/bidlobot/internal/storage"
)

// capCBPrefix is the callback_data namespace for captcha buttons. The full
// payload is "cap:ans:<challengeID>:<answerValue>".
const capCBPrefix = "cap:"

// captchaChatMemberHandler fires the captcha only on a genuine new join:
// new status "member" coming from "left" or "kicked". It MUST NOT fire on
// "restricted" -> "member" (that is the unmute the service itself performs
// on a correct answer; re-captching there would loop). Bots, anonymous
// admins, and non-supergroup chats are skipped. Nil-tolerant: a nil svc
// (feature off) makes this a no-op so the chat-member fanout can always
// invoke it.
func captchaChatMemberHandler(svc *captcha.Service, log *slog.Logger) th.ChatMemberUpdatedHandler {
	return func(ctx *th.Context, cmu telego.ChatMemberUpdated) error {
		if svc == nil {
			return nil
		}
		if cmu.Chat.Type != telego.ChatTypeSupergroup {
			return nil
		}
		newStatus := cmu.NewChatMember.MemberStatus()
		if newStatus != "member" {
			return nil
		}
		// Only a fresh join (left/kicked -> member) triggers a captcha.
		// restricted -> member is the unmute path; acting there would
		// post a second captcha for a challenge the service just cleared.
		oldStatus := cmu.OldChatMember.MemberStatus()
		if oldStatus != "left" && oldStatus != "kicked" {
			return nil
		}

		user := cmu.NewChatMember.MemberUser()
		if user.IsBot || shared.IsAnonymousAdmin(user.ID) {
			return nil
		}

		absChatID := storage.AbsChatID(cmu.Chat.ID)
		if err := svc.OnJoin(ctx.Context(), user, cmu.Chat.ID, absChatID, time.Now().UTC()); err != nil {
			log.Error("captcha OnJoin", "error", err, "chat_id", absChatID, "user_id", user.ID)
		}
		return nil
	}
}

// captchaCallbackHandler parses "cap:ans:<id>:<answer>" and delegates to the
// service, which owns every toast/edit/restrict decision. The handler logs
// and swallows errors so the update pipeline never blocks on a captcha.
func captchaCallbackHandler(svc *captcha.Service, log *slog.Logger) th.CallbackQueryHandler {
	return func(ctx *th.Context, query telego.CallbackQuery) error {
		// svc is non-nil: the predicate + registration only happen when the
		// feature is wired (routes.go guards on a.captchaSvc != nil).
		parts := strings.Split(query.Data, ":")
		// ["cap", "ans", "<challengeID>", "<answer>"]
		if len(parts) != 4 || parts[0] != "cap" || parts[1] != "ans" {
			return nil
		}
		answer, err := strconv.Atoi(parts[3])
		if err != nil {
			return nil
		}
		if err := svc.OnAnswer(ctx.Context(), query, parts[2], answer); err != nil {
			log.Error("captcha OnAnswer", "error", err, "challenge", parts[2])
		}
		return nil
	}
}

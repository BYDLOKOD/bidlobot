package bot

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"

	"github.com/veschin/bidlobot/internal/domain/pending"
	"github.com/veschin/bidlobot/internal/storage"
)

// CallbackPrefix is the namespace of callback_data this dispatcher
// owns. Anything outside the namespace is forwarded to the next
// handler (currently none) so we don't accidentally swallow callbacks
// destined for future features (mini-games etc).
const callbackNamespace = "v1:"

// CallbackKind enumerates the second segment of callback_data. The
// payload carries the pending action ID (16-char hex), so the full
// callback_data fits comfortably inside the 64-byte Telegram limit.
const (
	cbApply   = "apply"   // confirm a pending destructive action
	cbCancel  = "cancel"  // discard a pending destructive action
	cbPreview = "preview" // step from "cleanup announcement" to the actual candidate list
	cbConfirm = "confirm" // final confirm after a preview was rendered
)

// callbackResponse encapsulates how the dispatcher answers an
// invocation. AnswerText is shown as a transient toast above the chat
// (<=200 chars, <=10s). EditedText replaces the original inline message;
// if empty the message stays as is. ReplyMarkup overrides the keyboard
// of the original message; pass an empty markup to remove buttons.
type callbackResponse struct {
	AnswerText  string
	ShowAlert   bool
	EditedText  string
	ReplyMarkup *telego.InlineKeyboardMarkup
}

// AdminChecker is the narrow contract the dispatcher needs from the
// admin cache - declared here so tests can stub admin status without
// constructing a full AdminCache backed by a Telegram API mock.
type AdminChecker interface {
	IsAdmin(absChatID, userID int64) (bool, error)
}

// CallbackDispatcher routes callback_query events to the right
// executor. Concrete executors are pluggable so each domain (moderation,
// cleanup, mini-games) can register without bot-package coupling.
type CallbackDispatcher struct {
	pending    pending.Store
	adminCache AdminChecker
	bot        *telego.Bot
	log        *slog.Logger

	// One executor per (kind, callback verb). Map key format:
	// "<kind>:<verb>" e.g. "warn:apply", "cleanup:preview".
	// nil means "not yet implemented" - the dispatcher returns a
	// friendly toast instead of failing silently.
	executors map[string]CallbackExecutor
}

// CallbackExecutor performs the side effect for one (kind, verb) pair.
// It is called only after the dispatcher has loaded the action,
// validated TTL, and verified the actor matches the caller. The
// implementation can assume action != nil and action.ActorUserID ==
// query.From.ID.
type CallbackExecutor func(ctx context.Context, query telego.CallbackQuery, action *pending.Action) callbackResponse

func NewCallbackDispatcher(pendingStore pending.Store, adminCache AdminChecker, bot *telego.Bot, log *slog.Logger) *CallbackDispatcher {
	return &CallbackDispatcher{
		pending:    pendingStore,
		adminCache: adminCache,
		bot:        bot,
		log:        log,
		executors:  make(map[string]CallbackExecutor),
	}
}

// Register installs an executor under "<kind>:<verb>". Calling Register
// twice for the same key overwrites - handy for tests, but also means
// callers should construct dispatcher once during startup.
func (d *CallbackDispatcher) Register(kind pending.Kind, verb string, exec CallbackExecutor) {
	d.executors[string(kind)+":"+verb] = exec
}

// Handle is the th.CallbackQueryHandler entry point. The function
// always answers the callback query (the spinner above the button must
// be cleared even on error).
func (d *CallbackDispatcher) Handle(ctx *th.Context, query telego.CallbackQuery) error {
	resp := d.dispatch(ctx.Context(), query)
	d.respond(ctx.Context(), query, resp)
	return nil
}

func (d *CallbackDispatcher) dispatch(ctx context.Context, query telego.CallbackQuery) callbackResponse {
	verb, id, ok := parseCallbackData(query.Data)
	if !ok {
		return callbackResponse{AnswerText: "Кнопка устарела."}
	}

	action, err := d.pending.Get(ctx, id)
	switch {
	case errors.Is(err, pending.ErrExpired):
		return callbackResponse{
			AnswerText: "Действие истекло. Запросите команду заново.",
			ShowAlert:  true,
			EditedText: "⌛️ Действие истекло (5 минут таймаут).",
		}
	case errors.Is(err, pending.ErrNotFound):
		return callbackResponse{
			AnswerText: "Действие не найдено или уже выполнено.",
			ShowAlert:  true,
		}
	case err != nil:
		d.log.Error("pending.Get failed", "error", err, "id", id)
		return callbackResponse{
			AnswerText: "Ошибка чтения операции. Попробуйте ещё раз.",
			ShowAlert:  true,
		}
	}

	if query.From.ID != action.ActorUserID {
		return callbackResponse{
			AnswerText: "Только инициатор команды может её подтвердить.",
			ShowAlert:  true,
		}
	}

	if verb == cbCancel {
		_ = d.pending.Delete(ctx, id)
		return callbackResponse{
			AnswerText: "Отменено.",
			EditedText: "❌ Действие отменено.",
		}
	}

	exec := d.executors[string(action.Kind)+":"+verb]
	if exec == nil {
		return callbackResponse{
			AnswerText: "Эта команда ещё не подключена. Phase 3d/3e в работе.",
			ShowAlert:  true,
		}
	}

	if action.AbsChatID == 0 {
		if msg := query.Message; msg != nil {
			action.AbsChatID = storage.AbsChatID(msg.GetChat().ID)
		}
	}

	if !d.callerStillAdmin(action.AbsChatID, query.From.ID) {
		return callbackResponse{
			AnswerText: "У вас нет прав администратора в этом чате.",
			ShowAlert:  true,
		}
	}

	return exec(ctx, query, action)
}

// callerStillAdmin re-checks admin status at confirm time. The user
// could have been demoted between issuing the inline command and
// tapping Confirm; we don't want a stale promotion to authorize a
// destructive action.
func (d *CallbackDispatcher) callerStillAdmin(absChatID, userID int64) bool {
	if absChatID == 0 || d.adminCache == nil {
		return false
	}
	ok, err := d.adminCache.IsAdmin(absChatID, userID)
	if err != nil {
		d.log.Warn("admin re-check failed", "error", err, "chat_id", absChatID, "user_id", userID)
		return false
	}
	return ok
}

func (d *CallbackDispatcher) respond(ctx context.Context, query telego.CallbackQuery, resp callbackResponse) {
	_ = d.bot.AnswerCallbackQuery(ctx, &telego.AnswerCallbackQueryParams{
		CallbackQueryID: query.ID,
		Text:            resp.AnswerText,
		ShowAlert:       resp.ShowAlert,
	})

	msg := query.Message
	if msg == nil || (resp.EditedText == "" && resp.ReplyMarkup == nil) {
		return
	}

	chatID := msg.GetChat().ID
	messageID := msg.GetMessageID()

	if resp.EditedText != "" {
		params := &telego.EditMessageTextParams{
			ChatID:    telego.ChatID{ID: chatID},
			MessageID: messageID,
			Text:      resp.EditedText,
			ParseMode: telego.ModeHTML,
		}
		if resp.ReplyMarkup != nil {
			params.ReplyMarkup = resp.ReplyMarkup
		}
		if _, err := d.bot.EditMessageText(ctx, params); err != nil {
			d.log.Warn("EditMessageText failed", "error", err)
		}
		return
	}

	if resp.ReplyMarkup != nil {
		_, err := d.bot.EditMessageReplyMarkup(ctx, &telego.EditMessageReplyMarkupParams{
			ChatID:      telego.ChatID{ID: chatID},
			MessageID:   messageID,
			ReplyMarkup: resp.ReplyMarkup,
		})
		if err != nil {
			d.log.Warn("EditMessageReplyMarkup failed", "error", err)
		}
	}
}

// parseCallbackData splits "v1:<verb>:<id>" into (verb, id, true). Any
// other shape returns ok=false.
func parseCallbackData(data string) (verb, id string, ok bool) {
	if !strings.HasPrefix(data, callbackNamespace) {
		return "", "", false
	}
	rest := data[len(callbackNamespace):]
	colon := strings.IndexByte(rest, ':')
	if colon <= 0 || colon == len(rest)-1 {
		return "", "", false
	}
	return rest[:colon], rest[colon+1:], true
}

// MakeCallback builds a "v1:<verb>:<id>" callback_data string. Inline
// preview generators use it so the format is centralized.
func MakeCallback(verb, id string) string {
	return callbackNamespace + verb + ":" + id
}

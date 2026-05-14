package bot

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/mymmrac/telego"

	"github.com/veschin/bidlobot/internal/domain/membership"
	"github.com/veschin/bidlobot/internal/domain/moderation"
	"github.com/veschin/bidlobot/internal/domain/pending"
	"github.com/veschin/bidlobot/internal/shared"
	"github.com/veschin/bidlobot/internal/storage"
)

// ModerationExecutor wires the moderation domain into the
// CallbackDispatcher. Each Execute* method implements one
// (kind, "apply") pair. Methods are pure-ish - all side effects go
// through the injected services so tests can stub them.
type ModerationExecutor struct {
	mod        *moderation.Service
	members    membership.Store
	adminCache AdminChecker
	log        *slog.Logger
}

func NewModerationExecutor(mod *moderation.Service, members membership.Store, adminCache AdminChecker, log *slog.Logger) *ModerationExecutor {
	return &ModerationExecutor{
		mod:        mod,
		members:    members,
		adminCache: adminCache,
		log:        log,
	}
}

// RegisterAll attaches each executor under the dispatcher's pending.Kind
// keys so the dispatcher can route apply taps to them. cleanup is not
// registered here - it lives in its own executor (Phase 3e).
func (e *ModerationExecutor) RegisterAll(d *CallbackDispatcher) {
	d.Register(pending.KindWarn, cbApply, e.ExecuteWarn)
	d.Register(pending.KindMute, cbApply, e.ExecuteMute)
	d.Register(pending.KindUnmute, cbApply, e.ExecuteUnmute)
	d.Register(pending.KindBan, cbApply, e.ExecuteBan)
	d.Register(pending.KindUnban, cbApply, e.ExecuteUnban)
}

// resolveTarget looks the @username up in the membership store. If the
// store has no record we cannot proceed - the bot only knows users it
// observed at least once, and a moderation action against an unknown
// user_id would silently target user 0.
func (e *ModerationExecutor) resolveTarget(ctx context.Context, action *pending.Action) (userID int64, isBot bool, ok bool, hint string) {
	username := strings.TrimPrefix(action.TargetDisplay, "@")
	if username == "" {
		return 0, false, false, "У цели нет @username - используйте reply на её сообщение в чате."
	}
	m, err := e.members.GetMemberByUsername(ctx, action.AbsChatID, username)
	if err != nil {
		return 0, false, false, fmt.Sprintf("Бот не знает @%s в этом чате. Пользователь должен хотя бы раз написать или отреагировать.", username)
	}
	return m.UserID, m.IsBot, true, ""
}

// signedChatID extracts Telegram's signed chat ID from the callback's
// originating message. Domain methods like Mute/Ban take signed IDs;
// stored membership uses absolute IDs. This is the conversion point.
func signedChatID(query telego.CallbackQuery) int64 {
	if query.Message == nil {
		return 0
	}
	return query.Message.GetChat().ID
}

func (e *ModerationExecutor) ExecuteWarn(ctx context.Context, query telego.CallbackQuery, action *pending.Action) callbackResponse {
	targetID, isBot, ok, hint := e.resolveTarget(ctx, action)
	if !ok {
		return callbackResponse{AnswerText: hint, ShowAlert: true}
	}
	if err := e.mod.ValidateTarget(ctx, action.AbsChatID, action.ActorUserID, targetID, isBot, "warn"); err != nil {
		return callbackResponse{AnswerText: err.Error(), ShowAlert: true}
	}

	count, err := e.mod.Warn(ctx, action.AbsChatID, targetID, action.ActorUserID, action.Reason)
	if err != nil {
		e.log.Error("warn execute failed", "error", err, "target", targetID, "chat", action.AbsChatID)
		return callbackResponse{AnswerText: "Не удалось выдать предупреждение, повторите.", ShowAlert: true}
	}

	body := fmt.Sprintf("⚠️ <b>%s</b> предупреждён (%d/3)", htmlEscape(action.TargetDisplay), count)
	if action.Reason != "" {
		body += fmt.Sprintf("\n<b>Причина:</b> %s", htmlEscape(action.Reason))
	}

	if count == 3 {
		signed := signedChatID(query)
		if err := e.mod.AutoMute(ctx, signed, targetID); err != nil {
			body += fmt.Sprintf("\n⚠ Auto-mute не удался: %s", htmlEscape(err.Error()))
			e.log.Error("automute failed", "error", err, "target", targetID, "chat", signed)
		} else {
			body += "\n🔇 Автомьют на 24 часа активирован."
		}
	} else if count > 3 {
		body = fmt.Sprintf("⚠️ <b>%s</b> уже превысил порог авто-мьюта (%d предупреждений).",
			htmlEscape(action.TargetDisplay), count)
	}

	return callbackResponse{
		AnswerText: "Предупреждение выдано.",
		EditedText: body,
	}
}

func (e *ModerationExecutor) ExecuteMute(ctx context.Context, query telego.CallbackQuery, action *pending.Action) callbackResponse {
	targetID, isBot, ok, hint := e.resolveTarget(ctx, action)
	if !ok {
		return callbackResponse{AnswerText: hint, ShowAlert: true}
	}
	if err := e.mod.ValidateTarget(ctx, action.AbsChatID, action.ActorUserID, targetID, isBot, "mute"); err != nil {
		return callbackResponse{AnswerText: err.Error(), ShowAlert: true}
	}
	signed := signedChatID(query)
	if err := e.mod.Mute(ctx, signed, targetID, action.Duration); err != nil {
		e.log.Error("mute execute failed", "error", err, "target", targetID, "chat", signed)
		return callbackResponse{AnswerText: "Не удалось замьютить.", ShowAlert: true}
	}
	body := fmt.Sprintf("🔇 <b>%s</b> заглушён на %s.", htmlEscape(action.TargetDisplay), formatDuration(action.Duration))
	return callbackResponse{
		AnswerText: "Mute применён.",
		EditedText: body,
	}
}

func (e *ModerationExecutor) ExecuteUnmute(ctx context.Context, query telego.CallbackQuery, action *pending.Action) callbackResponse {
	targetID, _, ok, hint := e.resolveTarget(ctx, action)
	if !ok {
		return callbackResponse{AnswerText: hint, ShowAlert: true}
	}
	signed := signedChatID(query)
	if err := e.mod.Unmute(ctx, signed, targetID); err != nil {
		e.log.Error("unmute execute failed", "error", err, "target", targetID, "chat", signed)
		return callbackResponse{AnswerText: "Не удалось снять mute.", ShowAlert: true}
	}
	body := fmt.Sprintf("🔊 <b>%s</b> размьючен.", htmlEscape(action.TargetDisplay))
	return callbackResponse{
		AnswerText: "Unmute применён.",
		EditedText: body,
	}
}

func (e *ModerationExecutor) ExecuteBan(ctx context.Context, query telego.CallbackQuery, action *pending.Action) callbackResponse {
	targetID, isBot, ok, hint := e.resolveTarget(ctx, action)
	if !ok {
		return callbackResponse{AnswerText: hint, ShowAlert: true}
	}
	if err := e.mod.ValidateTarget(ctx, action.AbsChatID, action.ActorUserID, targetID, isBot, "ban"); err != nil {
		return callbackResponse{AnswerText: err.Error(), ShowAlert: true}
	}
	signed := signedChatID(query)
	if err := e.mod.Ban(ctx, signed, targetID); err != nil {
		e.log.Error("ban execute failed", "error", err, "target", targetID, "chat", signed)
		return callbackResponse{AnswerText: "Не удалось забанить.", ShowAlert: true}
	}
	body := fmt.Sprintf("🚫 <b>%s</b> забанен.", htmlEscape(action.TargetDisplay))
	if action.Reason != "" {
		body += fmt.Sprintf("\n<b>Причина:</b> %s", htmlEscape(action.Reason))
	}
	return callbackResponse{
		AnswerText: "Бан применён.",
		EditedText: body,
	}
}

func (e *ModerationExecutor) ExecuteUnban(ctx context.Context, query telego.CallbackQuery, action *pending.Action) callbackResponse {
	targetID, _, ok, hint := e.resolveTarget(ctx, action)
	if !ok {
		return callbackResponse{AnswerText: hint, ShowAlert: true}
	}
	signed := signedChatID(query)
	if err := e.mod.Unban(ctx, signed, targetID); err != nil {
		e.log.Warn("unban execute failed", "error", err, "target", targetID, "chat", signed)
		return callbackResponse{AnswerText: err.Error(), ShowAlert: true}
	}
	body := fmt.Sprintf("✅ <b>%s</b> разбанен.", htmlEscape(action.TargetDisplay))
	return callbackResponse{
		AnswerText: "Unban применён.",
		EditedText: body,
	}
}

// _ keeps storage and shared imports referenced if a future executor
// needs them; both packages are likely to be used by the cleanup
// executor in Phase 3e.
var _ = storage.AbsChatID
var _ = shared.IsAnonymousAdmin

package bot

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"

	"github.com/veschin/bidlobot/internal/domain/summarize"
	"github.com/veschin/bidlobot/internal/shared"
	"github.com/veschin/bidlobot/internal/shared/glm"
	"github.com/veschin/bidlobot/internal/storage"
	"github.com/veschin/bidlobot/internal/text"
)

const (
	// summarizeCooldown gates a single admin from re-firing an expensive
	// GLM call; over-frequency calls are dropped silently by gateMsg (a
	// "wait" reply would itself be public spam).
	summarizeCooldown  = 90 * time.Second
	summarizeDefaultN  = 200
	summarizeMaxN      = 4000 // also bounded by the buffer's per-chat cap
	summarizeBodyLimit = 3500 // runes; leaves headroom under Telegram's 4096

	// Placeholder send / result edit run on a context derived from the
	// app lifetime (not context.Background), so shutdown cancels them
	// instead of leaking a goroutine that writes after store.Close().
	summarizePlaceholderTO = 15 * time.Second
	summarizeEditTO        = 25 * time.Second
)

// summarizeSender is the narrow Telegram surface the feature needs:
// SendMessage for the placeholder and EditMessageText to swap in the
// result. shared.TelegramAPI deliberately omits EditMessageText, so -
// exactly like the cleanup executor - we take the concrete rate-limited
// *tgclient.Client (which satisfies this) rather than widening the
// shared interface for one consumer.
type summarizeSender interface {
	SendMessage(ctx context.Context, params *telego.SendMessageParams) (*telego.Message, error)
	EditMessageText(ctx context.Context, params *telego.EditMessageTextParams) (*telego.Message, error)
}

// summarizeRecorder is a passive observer (registered like
// statsCountHandler) that feeds the RAM window. It mirrors
// monthstats.ExtractSample's exclusion predicate exactly - non-bot, not
// an anonymous admin, no sender_chat - and additionally skips the bot's
// own command messages so a "/summarize 50" never pollutes the next
// transcript. Text or media caption is recorded; everything else is
// ignored. It never blocks the chain and never errors.
func summarizeRecorder(svc *summarize.Service) th.Handler {
	return func(ctx *th.Context, update telego.Update) error {
		msg := update.Message
		if msg != nil && msg.From != nil && !msg.From.IsBot &&
			!shared.IsAnonymousAdmin(msg.From.ID) && msg.SenderChat == nil {
			body := msg.Text
			if body == "" {
				body = msg.Caption
			}
			if body != "" && !strings.HasPrefix(strings.TrimSpace(msg.Text), "/") {
				svc.Record(storage.AbsChatID(msg.Chat.ID), summarize.Entry{
					MsgID:  msg.MessageID,
					UserID: msg.From.ID,
					Name:   summarizeName(msg.From),
					TS:     time.Unix(int64(msg.Date), 0).UTC(),
					Text:   body,
				})
			}
		}
		return ctx.Next(update)
	}
}

// handleSummarize is the public in-chat /summarize [N] entry point.
// Authorization is the project standard (AdminCache: getChatAdministrators
// + 60s TTL, re-checked every call). It returns fast: the multi-minute
// GLM call runs in a tracked background goroutine that edits a
// placeholder message in place, so the chat carries one artifact, never
// two, and the handler never holds a telego worker for minutes.
func (a *App) handleSummarize(_ *th.Context, msg telego.Message) error {
	if msg.Chat.Type != telego.ChatTypeSupergroup {
		return a.replySummarize(&msg, text.ErrStatsGroupOnly)
	}
	if msg.From == nil {
		return nil
	}
	// Anonymous admins post as the group: no user id to match against
	// getChatAdministrators. Same limitation the DM-only moderation
	// surface documents; tell them how to proceed instead of silently
	// dropping (they ARE an admin, just unidentifiable here).
	if shared.IsAnonymousAdmin(msg.From.ID) {
		return a.replySummarize(&msg, text.ErrSummarizeAnon)
	}

	absChatID := storage.AbsChatID(msg.Chat.ID)
	isAdmin, err := a.adminCache.IsAdmin(absChatID, msg.From.ID)
	if err != nil {
		a.log.Warn("summarize admin check failed", "abs_chat_id", absChatID, "error", err)
		return a.replySummarize(&msg, text.ErrSummarizeProvider)
	}
	if !isAdmin {
		// Non-admins get no reply at all: the whole privacy/anti-spam
		// posture is "never add public noise" - a refusal line is noise.
		return nil
	}

	if a.summarize == nil || a.summarizeSender == nil {
		return a.replySummarize(&msg, text.MsgSummarizeNotConfigured)
	}

	n := parseSummarizeN(msg.Text)

	available := a.summarize.Available(absChatID)
	if available == 0 {
		return a.replySummarize(&msg, text.MsgSummarizeEmpty)
	}

	if !a.summarize.TryAcquire(absChatID) {
		return a.replySummarize(&msg, text.MsgSummarizeBusy)
	}
	// Process-wide paid-API ceiling. Checked AFTER the per-chat slot so
	// a busy chat never consumes global budget; release the slot if the
	// global window is full.
	if !a.summarize.GlobalAllow() {
		a.summarize.Release(absChatID)
		return a.replySummarize(&msg, text.ErrSummarizeGlobalLimit)
	}

	pctx, pcancel := a.summarize.OpContext(summarizePlaceholderTO)
	placeholder, err := a.summarizeSender.SendMessage(pctx, &telego.SendMessageParams{
		ChatID:          telego.ChatID{ID: msg.Chat.ID},
		Text:            text.MsgSummarizeWorking,
		ReplyParameters: &telego.ReplyParameters{MessageID: msg.MessageID},
	})
	pcancel()
	if err != nil || placeholder == nil {
		// Could not post the message we would later edit: free the slot
		// and bail. Nothing was shown, so no error reply either.
		a.summarize.Release(absChatID)
		a.log.Warn("summarize placeholder send failed", "abs_chat_id", absChatID, "error", err)
		return nil
	}

	signedChatID := msg.Chat.ID
	placeholderID := placeholder.MessageID
	requester := summarizeName(msg.From)

	a.summarize.Go(func() {
		defer a.summarize.Release(absChatID)
		body, meta, serr := a.summarize.Summarize(absChatID, n)
		final := composeSummaryMessage(body, meta, requester, serr)
		ectx, ecancel := a.summarize.OpContext(summarizeEditTO)
		defer ecancel()
		if _, eerr := a.summarizeSender.EditMessageText(ectx, &telego.EditMessageTextParams{
			ChatID:    telego.ChatID{ID: signedChatID},
			MessageID: placeholderID,
			Text:      final,
		}); eerr != nil {
			a.log.Warn("summarize result edit failed",
				"abs_chat_id", absChatID, "error", eerr)
		}
	})
	return nil
}

// composeSummaryMessage maps a Summarize outcome to the plain-text body
// that replaces the placeholder. The model output is untrusted and goes
// into a public message, so no ParseMode is ever set by the caller and
// nothing here emits markup.
func composeSummaryMessage(body string, meta summarize.Meta, requester string, serr error) string {
	if serr != nil {
		switch {
		case errors.Is(serr, summarize.ErrNoMessages):
			return text.MsgSummarizeEmpty
		case errors.Is(serr, glm.ErrAuth):
			return text.ErrSummarizeAuth
		case errors.Is(serr, glm.ErrQuota):
			return text.ErrSummarizeQuota
		case errors.Is(serr, glm.ErrRateLimited):
			return text.ErrSummarizeRateLimited
		case errors.Is(serr, glm.ErrContextTooLong):
			return text.ErrSummarizeTooLong
		case errors.Is(serr, glm.ErrTimeout):
			return text.ErrSummarizeTimeout
		default: // glm.ErrProvider, glm.ErrEmpty, anything unexpected
			return text.ErrSummarizeProvider
		}
	}

	body = strings.TrimSpace(body)
	if body == "" {
		return text.ErrSummarizeProvider
	}
	if r := []rune(body); len(r) > summarizeBodyLimit {
		body = strings.TrimSpace(string(r[:summarizeBodyLimit])) + "..."
	}
	footer := "\n\n- " + summarizeFooter(meta, requester)
	// Untrusted model output and the @requester go into a public,
	// no-ParseMode message; defuse so nothing renders as a real,
	// notifying Telegram mention (a member could otherwise steer the
	// summary into mass-pinging the chat).
	return defuseMentions(body + footer)
}

// defuseMentions breaks Telegram's bare-"@username" auto-mention in
// untrusted/model text. In a plain-text message Telegram still parses
// "@name" as a real, notifying mention; inserting a U+2060 WORD JOINER
// right after '@' is visually invisible but stops the parse. Applied to
// the whole final body+footer as the single choke point.
func defuseMentions(s string) string {
	return strings.ReplaceAll(s, "@", "@\u2060")
}

// summarizeFooter is the attribution/disclosure line. It states the
// external-AI provenance explicitly: chat members must be able to see
// that recent messages were sent to an external model.
func summarizeFooter(meta summarize.Meta, requester string) string {
	from := meta.From.Format("15:04")
	to := meta.To.Format("15:04")
	return "итог " + strconv.Itoa(meta.Included) + " сообщений (" + from + "-" + to +
		" UTC), сгенерировано внешним AI (GLM) по запросу @" + requester
}

func (a *App) replySummarize(msg *telego.Message, body string) error {
	_, err := a.sender.SendMessage(context.Background(), &telego.SendMessageParams{
		ChatID:          telego.ChatID{ID: msg.Chat.ID},
		Text:            body,
		ReplyParameters: &telego.ReplyParameters{MessageID: msg.MessageID},
	})
	return err
}

// parseSummarizeN extracts the message count from "/summarize 50". A
// missing or non-numeric argument falls back to the default rather than
// erroring - the friendlier behavior for a quick admin command. The
// result is clamped to [1, summarizeMaxN]; the live window size is the
// other (lower) bound, enforced downstream.
func parseSummarizeN(cmdText string) int {
	parts := strings.Fields(cmdText)
	for _, tok := range parts[min(1, len(parts)):] {
		if strings.ContainsRune(tok, '@') {
			continue // command token written as /summarize@BotName
		}
		v, err := strconv.Atoi(tok)
		if err != nil || v <= 0 {
			continue
		}
		if v > summarizeMaxN {
			return summarizeMaxN
		}
		return v
	}
	return summarizeDefaultN
}

// summarizeName is a clean plain display token for the transcript and
// the footer attribution: @handle when known (most stable), else the
// trimmed first/last name, else a numeric fallback. No HTML/Markdown -
// this is fed to the model and into a no-ParseMode message.
func summarizeName(u *telego.User) string {
	if u == nil {
		return "user"
	}
	if h := strings.TrimSpace(u.Username); h != "" {
		return h
	}
	name := strings.TrimSpace(strings.TrimSpace(u.FirstName) + " " + strings.TrimSpace(u.LastName))
	if name != "" {
		return name
	}
	return "id" + strconv.FormatInt(u.ID, 10)
}

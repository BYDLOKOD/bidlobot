package bot

// YouTube `si=` sanitizer: clean si= out of text/caption/entities locally,
// post one cleaned copy (media copied server-side by copyMessage), then
// delete the original. Never destroys content: any failure keeps the
// original and posts no notice. Only YouTube hosts are touched; URL/entity
// transformation lives in youtube_entities.go.
//
// Exclusions: edited messages are not re-sanitized; album items
// (MediaGroupID != "") are never copied. Manually forwarded channel posts
// stay eligible: the copy reproduces content, not the forward header.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"

	"github.com/veschin/bidlobot/internal/shared"
)

// Exact YouTube host allowlist; anything else keeps its si untouched.
var youtubeHosts = map[string]struct{}{
	"youtube.com":          {},
	"youtu.be":             {},
	"youtube-nocookie.com": {},
	"music.youtube.com":    {},
}

// Bare/scheme-less YouTube links in plain text; stops at whitespace and
// excludes angle/paren wrappers.
var urlScanRe = regexp.MustCompile(`(?i)\b((?:https?://|www\.|m\.)?(?:[a-z0-9-]+\.)*(?:youtube\.com|youtu\.be|youtube-nocookie\.com)\b[^\s<>"')\]]*)`)

// Trailing sentence punctuation stripped from scanned tokens.
const trailingPunct = ".,!?;:"

// isYouTubeHost normalizes the host and checks the exact allowlist.
func isYouTubeHost(host string) bool {
	host = strings.ToLower(host)
	if i := strings.IndexByte(host, ':'); i >= 0 {
		host = host[:i]
	}
	switch {
	case strings.HasPrefix(host, "www."):
		host = host[len("www."):]
	case strings.HasPrefix(host, "m."):
		host = host[len("m."):]
	}
	_, ok := youtubeHosts[host]
	return ok
}

// youtubeCopySender is the sanitizer's send surface: text with corrected
// entities, server-side copy (CopyMessages can drop the caption), delete
// the original only after a confirmed send. A sender lacking these
// methods means "cannot sanitize" and the original is kept.
type youtubeCopySender interface {
	SendMessage(ctx context.Context, params *telego.SendMessageParams) (*telego.Message, error)
	CopyMessage(ctx context.Context, params *telego.CopyMessageParams) (*telego.MessageID, error)
	CopyMessages(ctx context.Context, params *telego.CopyMessagesParams) ([]telego.MessageID, error)
	DeleteMessage(ctx context.Context, params *telego.DeleteMessageParams) error
}

// sanitizedPost carries the corrected text/caption and their entities.
type sanitizedPost struct {
	text         string
	textEntities []telego.MessageEntity
	caption      string
	capEntities  []telego.MessageEntity
}

// youtubeMediaSender is the rate-limited send surface the TikTok/X
// reposter needs (file_id uploads); the YouTube sanitizer does not use it.
type youtubeMediaSender interface {
	SendMessage(ctx context.Context, params *telego.SendMessageParams) (*telego.Message, error)
	DeleteMessage(ctx context.Context, params *telego.DeleteMessageParams) error
	SendPhoto(ctx context.Context, params *telego.SendPhotoParams) (*telego.Message, error)
	SendVideo(ctx context.Context, params *telego.SendVideoParams) (*telego.Message, error)
	SendAnimation(ctx context.Context, params *telego.SendAnimationParams) (*telego.Message, error)
	SendDocument(ctx context.Context, params *telego.SendDocumentParams) (*telego.Message, error)
	SendMediaGroup(ctx context.Context, params *telego.SendMediaGroupParams) ([]telego.Message, error)
}

// youtubeSanitizer is the supergroup middleware: skip the stats exclusion
// set, pass through when nothing changed, otherwise post one cleaned copy
// and delete the original last. Always continues the chain.
func youtubeSanitizer(a *App) th.Handler {
	return func(thctx *th.Context, update telego.Update) error {
		if act, plan := sanitizeDecision(update.Message); act {
			handleSanitize(thctx.Context(), a.youtubeCopySender(), a.log,
				update.Message, plan)
		}
		return thctx.Next(update)
	}
}

// sanitizeDecision is the pure gate: same exclusion set as the stats
// counter; act=false on transformation error or when nothing changed.
// The input message and its entity slices are never mutated.
func sanitizeDecision(msg *telego.Message) (bool, *sanitizedPost) {
	if msg == nil {
		return false, nil
	}
	if msg.From == nil || msg.From.IsBot ||
		shared.IsAnonymousAdmin(msg.From.ID) || msg.SenderChat != nil {
		return false, nil
	}
	newText, newEntities, textChanged, err := SanitizeMessageText(msg.Text, msg.Entities)
	if err != nil {
		return false, nil
	}
	newCaption, newCapEntities, capChanged, err := SanitizeMessageText(msg.Caption, msg.CaptionEntities)
	if err != nil {
		return false, nil
	}
	if !textChanged && !capChanged {
		return false, nil
	}
	return true, &sanitizedPost{
		text:         newText,
		textEntities: newEntities,
		caption:      newCaption,
		capEntities:  newCapEntities,
	}
}

// sanitizerSender returns the rate-limited media-capable client, or a
// text-only fallback for non-media senders (degraded but safe).
func (a *App) sanitizerSender() youtubeMediaSender {
	if s, ok := a.sender.(youtubeMediaSender); ok {
		return s
	}
	return textOnlySender{a.sender}
}

// textOnlySender adapts a SendMessage/DeleteMessage-only client to the
// media interface by failing media sends with a sentinel error.
type textOnlySender struct {
	api interface {
		SendMessage(ctx context.Context, params *telego.SendMessageParams) (*telego.Message, error)
		DeleteMessage(ctx context.Context, params *telego.DeleteMessageParams) error
	}
}

var errNoMediaSend = &mediaUnsupportedError{}

type mediaUnsupportedError struct{}

func (*mediaUnsupportedError) Error() string {
	return "tgclient: media send not supported by configured sender"
}

func (t textOnlySender) SendMessage(ctx context.Context, p *telego.SendMessageParams) (*telego.Message, error) {
	return t.api.SendMessage(ctx, p)
}
func (t textOnlySender) DeleteMessage(ctx context.Context, p *telego.DeleteMessageParams) error {
	return t.api.DeleteMessage(ctx, p)
}
func (textOnlySender) SendPhoto(context.Context, *telego.SendPhotoParams) (*telego.Message, error) {
	return nil, errNoMediaSend
}
func (textOnlySender) SendVideo(context.Context, *telego.SendVideoParams) (*telego.Message, error) {
	return nil, errNoMediaSend
}
func (textOnlySender) SendAnimation(context.Context, *telego.SendAnimationParams) (*telego.Message, error) {
	return nil, errNoMediaSend
}
func (textOnlySender) SendDocument(context.Context, *telego.SendDocumentParams) (*telego.Message, error) {
	return nil, errNoMediaSend
}
func (textOnlySender) SendMediaGroup(context.Context, *telego.SendMediaGroupParams) ([]telego.Message, error) {
	return nil, errNoMediaSend
}

// youtubeCopySender returns the sanitizer's send surface (production:
// *tgclient.Client); nil makes handleSanitize retain the original.
func (a *App) youtubeCopySender() youtubeCopySender {
	if s, ok := a.sender.(youtubeCopySender); ok {
		return s
	}
	return nil
}

// preflightReason reports why a changed message cannot be safely copied;
// "" means safe. An unsafe shape keeps the original with no notice.
func preflightReason(msg *telego.Message, plan *sanitizedPost) string {
	switch {
	case msg.GetMessageID() <= 0:
		return "unusable original id"
	case msg.MediaGroupID != "":
		return "album item"
	case msg.PaidMedia != nil || msg.PaidStarCount != 0 || msg.IsPaidPost:
		return "paid media or paid post"
	case msg.ReplyMarkup != nil:
		return "inline keyboard not preservable"
	case msg.Invoice != nil || msg.SuccessfulPayment != nil || msg.RefundedPayment != nil:
		return "unsupported attached content"
	case msg.HasMediaSpoiler:
		return "media spoiler not preservable"
	case msg.IsAutomaticForward:
		return "automatic channel forward"
	case msg.Quote != nil && msg.ReplyToMessage == nil:
		return "quote without local reply"
	case msg.ExternalReply != nil || msg.ReplyToStory != nil ||
		msg.ReplyToChecklistTaskID != 0 || msg.ReplyToPollOptionID != "":
		return "unsupported reply context"
	case utf16Length(plan.text) > 4096:
		return "cleaned text over 4096 UTF-16 units"
	case sanitizedContentKind(msg) == "":
		return "unsupported content kind"
	}
	return ""
}

// sanitizedContentKind classifies media (reproduced server-side by
// copyMessage), text, or "" (never rewritten). Media wins over text.
func sanitizedContentKind(msg *telego.Message) string {
	switch {
	case len(msg.Photo) > 0 || msg.Video != nil || msg.Animation != nil ||
		msg.Document != nil || msg.Audio != nil || msg.Voice != nil:
		return "media"
	case msg.Text != "":
		return "text"
	default:
		return ""
	}
}

// copyReplyParams preserves a same-chat reply target and a local quote's
// text/entities/position on the copy.
func copyReplyParams(msg *telego.Message) *telego.ReplyParameters {
	if msg.ReplyToMessage == nil {
		return nil
	}
	rp := &telego.ReplyParameters{
		MessageID: msg.ReplyToMessage.GetMessageID(),
		ChatID:    telego.ChatID{ID: msg.Chat.ID},
	}
	if msg.Quote != nil {
		rp.Quote = msg.Quote.Text
		rp.QuoteEntities = msg.Quote.Entities
		rp.QuotePosition = msg.Quote.Position
	}
	return rp
}

// sanitizedTextParams builds the text copy: corrected text/entities,
// original thread/protection/reply context, link preview with a cleaned
// URL. LinkPreviewOptions is copied before modification.
func sanitizedTextParams(msg *telego.Message, plan *sanitizedPost) *telego.SendMessageParams {
	params := &telego.SendMessageParams{
		ChatID:          telego.ChatID{ID: msg.Chat.ID},
		Text:            plan.text,
		Entities:        plan.textEntities,
		MessageThreadID: msg.MessageThreadID,
		ProtectContent:  msg.HasProtectedContent,
		ReplyParameters: copyReplyParams(msg),
	}
	if msg.LinkPreviewOptions != nil {
		lpo := *msg.LinkPreviewOptions
		if lpo.URL != "" {
			if cleaned, changed := StripShareTracking(lpo.URL); changed {
				lpo.URL = cleaned
			}
		}
		params.LinkPreviewOptions = &lpo
	}
	return params
}

// sanitizedCopyParams builds the server-side media copy of the ORIGINAL
// message (no reupload); only the caption carries the si cleanup.
func sanitizedCopyParams(msg *telego.Message, plan *sanitizedPost) *telego.CopyMessageParams {
	return &telego.CopyMessageParams{
		ChatID:                telego.ChatID{ID: msg.Chat.ID},
		MessageThreadID:       msg.MessageThreadID,
		FromChatID:            telego.ChatID{ID: msg.Chat.ID},
		MessageID:             msg.GetMessageID(),
		Caption:               plan.caption,
		CaptionEntities:       plan.capEntities,
		ShowCaptionAboveMedia: msg.ShowCaptionAboveMedia,
		ProtectContent:        msg.HasProtectedContent,
		ReplyParameters:       copyReplyParams(msg),
	}
}

// handleSanitize performs the guarded copy + delete: the original is
// deleted only after a confirmed copy with a positive new id; any failure
// keeps it and posts no notice. A delete failure keeps both messages.
func handleSanitize(
	ctx context.Context,
	snd youtubeCopySender,
	log *slog.Logger,
	msg *telego.Message,
	plan *sanitizedPost,
) {
	if msg == nil || plan == nil {
		return
	}
	if snd == nil {
		log.Info("youtube sanitizer: no copy-capable sender; original kept",
			"chat_id", msg.Chat.ID, "message_id", msg.GetMessageID())
		return
	}
	if reason := preflightReason(msg, plan); reason != "" {
		log.Info("youtube sanitizer: unrepresentable message kept as-is",
			"chat_id", msg.Chat.ID, "message_id", msg.GetMessageID(), "reason", reason)
		return
	}

	var (
		newID int
		err   error
	)
	kind := sanitizedContentKind(msg)
	switch {
	case kind != "text" && utf16Length(plan.caption) > 1024:
		newID, err = copyLongYouTubeCaption(ctx, snd, log, msg, plan)
	case kind == "text":
		var m *telego.Message
		m, err = snd.SendMessage(ctx, sanitizedTextParams(msg, plan))
		switch {
		case err == nil && m == nil:
			err = errors.New("text send returned no message")
		case err == nil && m.MessageID <= 0:
			err = fmt.Errorf("text send returned non-positive id %d", m.MessageID)
		}
		if err == nil {
			newID = m.MessageID
		}
	default:
		var id *telego.MessageID
		id, err = snd.CopyMessage(ctx, sanitizedCopyParams(msg, plan))
		switch {
		case err == nil && id == nil:
			err = errors.New("copy returned no message id")
		case err == nil && id.MessageID <= 0:
			err = fmt.Errorf("copy returned non-positive id %d", id.MessageID)
		}
		if err == nil {
			newID = id.MessageID
		}
	}
	if err != nil {
		log.Warn("youtube sanitizer: send failed; original kept",
			"chat_id", msg.Chat.ID, "message_id", msg.GetMessageID(), "error", err)
		return
	}

	if err := snd.DeleteMessage(ctx, &telego.DeleteMessageParams{
		ChatID:    telego.ChatID{ID: msg.Chat.ID},
		MessageID: msg.GetMessageID(),
	}); err != nil {
		// The cleaned copy is live; we just cannot remove the original.
		log.Info("youtube sanitizer: copied but delete failed; original kept",
			"chat_id", msg.Chat.ID, "message_id", msg.GetMessageID(),
			"new_message_id", newID, "error", err)
	}
}

// largestPhotoFileID returns the highest-resolution PhotoSize file_id,
// picking by area rather than trusting order.
func largestPhotoFileID(sizes []telego.PhotoSize) string {
	best := sizes[0]
	bestArea := best.Width * best.Height
	for _, s := range sizes[1:] {
		if a := s.Width * s.Height; a > bestArea {
			best, bestArea = s, a
		}
	}
	return best.FileID
}

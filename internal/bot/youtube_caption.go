package bot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/mymmrac/telego"
)

// CopyMessages with RemoveCaption is used for long captions: telego marks
// CopyMessage's Caption omitempty, so an empty caption would keep the
// original instead of removing it.
type youtubeCaptionPart struct {
	text     string
	entities []telego.MessageEntity
}

// splitYouTubeCaption retains every byte and clips formatting and hidden
// link entities at message boundaries. Visible URLs/mentions stay whole.
// An indivisible entity longer than a Telegram message rejects the whole copy.
func splitYouTubeCaption(text string, entities []telego.MessageEntity) ([]youtubeCaptionPart, error) {
	const maxUnits = 4096
	if utf16Length(text) <= maxUnits {
		return []youtubeCaptionPart{{text: text, entities: entities}}, nil
	}

	var parts []youtubeCaptionPart
	startByte, startUnit := 0, 0
	for startByte < len(text) {
		bytes, units := youtubeCaptionBoundary(text[startByte:], maxUnits)
		endUnit := startUnit + units
		// Moving a boundary out of one link may put it inside another
		// indivisible entity. Keep moving left until none is crossed.
		for {
			boundary := endUnit
			for _, e := range entities {
				if !splittableYouTubeEntity(e.Type) && e.Offset < endUnit && e.Offset+e.Length > endUnit {
					if e.Offset < boundary {
						boundary = e.Offset
					}
				}
			}
			if boundary == endUnit {
				break
			}
			if boundary <= startUnit {
				return nil, errors.New("caption entity cannot fit in a text message")
			}
			endUnit = boundary
		}
		if endUnit != startUnit+units {
			bytes, units = youtubeCaptionBoundary(text[startByte:], endUnit-startUnit)
		}
		if bytes == 0 {
			return nil, errors.New("caption has no safe message boundary")
		}
		part := youtubeCaptionPart{text: text[startByte : startByte+bytes]}
		for _, entity := range entities {
			end := entity.Offset + entity.Length
			if end <= startUnit || entity.Offset >= endUnit {
				continue
			}
			begin := entity.Offset
			if begin < startUnit {
				begin = startUnit
			}
			if end > endUnit {
				end = endUnit
			}
			entity.Offset = begin - startUnit
			entity.Length = end - begin
			part.entities = append(part.entities, entity)
		}
		parts = append(parts, part)
		startByte += bytes
		startUnit += units
	}
	return parts, nil
}

func splittableYouTubeEntity(kind string) bool {
	switch kind {
	case "bold", "italic", "underline", "strikethrough", "spoiler", "blockquote", "expandable_blockquote", "code", "pre", "text_link":
		return true
	default:
		return false
	}
}

// youtubeCaptionBoundary returns a rune-aligned prefix no longer than
// maxUnits UTF-16 units, so astral characters are never split in half.
func youtubeCaptionBoundary(text string, maxUnits int) (int, int) {
	units := 0
	for i, r := range text {
		width := 1
		if r > 0xFFFF {
			width = 2
		}
		if units+width > maxUnits {
			return i, units
		}
		units += width
	}
	return len(text), units
}

// copyLongYouTubeCaption preflights all text chunks, copies the media with
// no caption, then sends the complete cleaned caption. Any partial failure
// retains the original and attempts to remove only the bot's new messages.
func copyLongYouTubeCaption(ctx context.Context, snd youtubeCopySender, log *slog.Logger, msg *telego.Message, plan *sanitizedPost) (int, error) {
	parts, err := splitYouTubeCaption(plan.caption, plan.capEntities)
	if err != nil {
		return 0, err
	}
	ids, err := snd.CopyMessages(ctx, &telego.CopyMessagesParams{
		ChatID:          telego.ChatID{ID: msg.Chat.ID},
		MessageThreadID: msg.MessageThreadID,
		FromChatID:      telego.ChatID{ID: msg.Chat.ID},
		MessageIDs:      []int{msg.GetMessageID()},
		ProtectContent:  msg.HasProtectedContent,
		RemoveCaption:   true,
	})
	if err != nil {
		return 0, err
	}
	if len(ids) != 1 || ids[0].MessageID <= 0 || ids[0].MessageID == msg.GetMessageID() {
		rollbackYouTubeCopies(ctx, snd, log, msg, ids)
		return 0, errors.New("captionless copy returned no usable media id")
	}
	mediaID := ids[0].MessageID
	created := make([]telego.MessageID, 0, len(parts)+1)
	created = append(created, ids[0])
	for i, part := range parts {
		reply := &telego.ReplyParameters{MessageID: mediaID}
		// A local reply/quote belongs to the post's text. Otherwise the
		// text replies to the copied media, keeping the parts connected.
		if i == 0 && msg.ReplyToMessage != nil {
			reply = copyReplyParams(msg)
		}
		sent, sendErr := snd.SendMessage(ctx, &telego.SendMessageParams{
			ChatID:             telego.ChatID{ID: msg.Chat.ID},
			MessageThreadID:    msg.MessageThreadID,
			Text:               part.text,
			Entities:           part.entities,
			ProtectContent:     msg.HasProtectedContent,
			ReplyParameters:    reply,
			LinkPreviewOptions: &telego.LinkPreviewOptions{IsDisabled: true},
		})
		if sendErr != nil || sent == nil || sent.MessageID <= 0 || sent.MessageID == msg.GetMessageID() {
			rollbackYouTubeCopies(ctx, snd, log, msg, created)
			if sendErr != nil {
				return 0, sendErr
			}
			return 0, fmt.Errorf("caption part %d returned no usable message id", i+1)
		}
		created = append(created, telego.MessageID{MessageID: sent.MessageID})
	}
	return mediaID, nil
}

func rollbackYouTubeCopies(ctx context.Context, snd youtubeCopySender, log *slog.Logger, original *telego.Message, ids []telego.MessageID) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	for _, id := range ids {
		if id.MessageID <= 0 || id.MessageID == original.GetMessageID() {
			continue
		}
		if err := snd.DeleteMessage(cleanupCtx, &telego.DeleteMessageParams{
			ChatID:    telego.ChatID{ID: original.Chat.ID},
			MessageID: id.MessageID,
		}); err != nil {
			log.Warn("youtube sanitizer: partial copy cleanup failed; original kept",
				"chat_id", original.Chat.ID, "message_id", id.MessageID, "error", err)
		}
	}
}

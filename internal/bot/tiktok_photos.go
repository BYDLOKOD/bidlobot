package bot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/mymmrac/telego"
	"github.com/veschin/bidlobot/internal/shared/tgclient"
)

type slideshowSender interface {
	SendSlideshow(context.Context, *tgclient.SlideshowParams) (*telego.Message, error)
}

// repostTikTokPhotos materializes every slide before one atomic rich-message
// send. Missing/oversized slides never produce an incomplete carousel or remove
// the source link. Files are owned by the caller's per-post temporary directory.
func repostTikTokPhotos(ctx context.Context, snd youtubeMediaSender, log *slog.Logger, owners ownerRecorder, msg *telego.Message, post tiktokPost, workDir string) (*telego.Message, repostOutcome, error) {
	slides, ok := snd.(slideshowSender)
	if !ok {
		return nil, repostPermanent, errNoMediaSend
	}
	images := post.Images
	attachments := len(images)
	if post.AudioURL != "" {
		attachments++
	}
	if len(images) == 0 || attachments > tgclient.MaxSlideshowPhotos {
		return nil, repostPermanent, fmt.Errorf("tiktok: photo post has %d media attachments, supported maximum is %d", attachments, tgclient.MaxSlideshowPhotos)
	}
	dlCtx, cancel := context.WithTimeout(ctx, mirrorDownloadTimeout)
	defer cancel()
	photos := make([]telego.InputFile, 0, len(images))
	for i, imageURL := range images {
		path := filepath.Join(workDir, fmt.Sprintf("slide-%02d.jpg", i))
		if err := downloadCommentImage(dlCtx, tiktokCommentHTTPClient, imageURL, path); err != nil {
			return nil, repostTransient, fmt.Errorf("photo %d: %w", i+1, err)
		}
		f, err := os.Open(path)
		if err != nil {
			return nil, repostTransient, err
		}
		defer f.Close()
		photos = append(photos, telego.InputFile{File: f})
	}
	var audio *telego.InputMediaAudio
	if post.AudioURL != "" {
		path := filepath.Join(workDir, "soundtrack.mp3")
		if err := downloadTikTokAudio(dlCtx, tiktokCommentHTTPClient, post.AudioURL, path); err != nil {
			if errors.Is(err, errTikTokAudioTooLarge) {
				return nil, repostPermanent, err
			}
			return nil, repostTransient, fmt.Errorf("soundtrack: %w", err)
		}
		f, err := os.Open(path)
		if err != nil {
			return nil, repostTransient, err
		}
		defer f.Close()
		audio = &telego.InputMediaAudio{
			Media: telego.InputFile{File: f}, Title: post.AudioTitle, Performer: post.AudioPerformer,
		}
	}
	rawCaption := msg.Caption
	if rawCaption == "" {
		rawCaption = msg.Text
	}
	sent, err := slides.SendSlideshow(ctx, &tgclient.SlideshowParams{
		ChatID: telego.ChatID{ID: msg.Chat.ID}, MessageThreadID: msg.MessageThreadID,
		Photos: photos, Audio: audio, CaptionHTML: repostCaption(msg.From.Username, msg.From.FirstName, rawCaption),
	})
	if err != nil {
		if isAPIPermanent(err) {
			return nil, repostPermanent, err
		}
		return nil, repostTransient, err
	}
	if sent == nil || sent.MessageID <= 0 {
		return nil, repostTransient, fmt.Errorf("tiktok: slideshow has no confirmed message ID")
	}
	if owners != nil {
		owners.RecordOwner(msg.Chat.ID, sent.MessageID, msg.From)
	}
	if err := snd.DeleteMessage(ctx, &telego.DeleteMessageParams{ChatID: telego.ChatID{ID: msg.Chat.ID}, MessageID: msg.MessageID}); err != nil {
		log.Info("tiktok: slideshow sent but delete failed; original kept", "chat_id", msg.Chat.ID, "message_id", msg.MessageID, "error", err)
	}
	log.Info("tiktok: slideshow reposted", "chat_id", msg.Chat.ID, "message_id", msg.MessageID, "slides", len(images))
	return sent, repostSent, nil
}

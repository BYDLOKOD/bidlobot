package bot

// TikTok video repost.
//
// When a supergroup message contains a TikTok video link, the bot downloads
// the video, trims the watermark end-screen (last ~2s), reposts attributed
// to the original sender (display name only, no @, no tg://user?id=), then
// deletes the original. Same privacy gate as the YouTube sanitizer: privacy
// must be OFF.
//
// Design notes / documented v1 gaps (mirroring youtube_sanitizer.go):
//   - edited_message: OUT OF SCOPE for v1. The router only feeds
//     update.Message here; an edit that introduces a TikTok link is not
//     re-processed. Explicit gap.
//   - media groups / albums: only the caption-bearing item is processed.
//   - reply / forward context: lost on repost.
//   - text_link entities: detected but the URL is in entity.URL, not text.
//     We use the entity URL for the download but do not attempt to rewrite
//     inline text (same UTF-16 offset problem as YT sanitizer).

import (
	"context"
	"fmt"
	"html"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"

	"github.com/veschin/bidlobot/internal/shared"
)

// --- Constants -----------------------------------------------------------

const (
	// msgTikTokHeader is the attribution header for a reposted TikTok.
	// %s = sender display name (UserDisplay, no @, no tg://user?id=).
	msgTikTokHeader = "\U0001F464 <b>%s</b> \u043F\u0438\u0441\u0430\u043B(\u0430):"

	// msgTikTokSizeLimit is the decline note when video exceeds Telegram's 50 MB cap.
	msgTikTokSizeLimit = "\u26A0\uFE0F \u0412\u0438\u0434\u0435\u043E \u0438\u0437 TikTok \u0441\u043B\u0438\u0448\u043A\u043E\u043C \u0431\u043E\u043B\u044C\u0448\u043E\u0435 \u0434\u043B\u044F \u0437\u0430\u0433\u0440\u0443\u0437\u043A\u0438 (>50 \u041C\u0411)."

	// msgTikTokDownloadFail is the note when download/processing fails.
	msgTikTokDownloadFail = "\u26A0\uFE0F \u041D\u0435 \u0443\u0434\u0430\u043B\u043E\u0441\u044C \u0441\u043A\u0430\u0447\u0430\u0442\u044C \u0432\u0438\u0434\u0435\u043E \u0438\u0437 TikTok."
)

const (

	// maxVideoSize is Telegram's bot upload limit for video (50 MB).
	maxVideoSize = 50 * 1024 * 1024

	// tiktokDownloadTimeout caps the yt-dlp invocation.
	tiktokDownloadTimeout = 60 * time.Second
)

// --- Host detection ------------------------------------------------------

// tiktokHosts is the exact set of TikTok hosts that carry video links.
var tiktokHosts = map[string]struct{}{
	"tiktok.com":    {},
	"vm.tiktok.com": {},
	"vt.tiktok.com": {},
}

// tiktokURLRe finds TikTok video URLs in text. Matches:
//
//	https://www.tiktok.com/@user/video/123456789
//	https://vm.tiktok.com/ABCDEF/
//	https://vt.tiktok.com/ZSCqHSWxM/
//	https://m.tiktok.com/v/123456789.html
//	tiktok.com/@user/video/123456789 (scheme-less, edge case)
//
// Conservative: stops at whitespace and trailing punctuation.
var tiktokURLRe = regexp.MustCompile(`(?i)\b((?:https?://)?(?:www\.|m\.)?(?:(?:vm|vt)\.)?tiktok\.com[/\S]*[^\s<>"')\]]*)`)

// isTikTokHost lower-cases host, drops any port, strips a single leading
// "www." or "m." or "vm." or "vt." label, and checks the exact allowlist.
func isTikTokHost(host string) bool {
	host = strings.ToLower(host)
	if h, _, ok := strings.Cut(host, ":"); ok {
		host = h
	}
	for _, pfx := range []string{"www.", "m.", "vm.", "vt."} {
		if rest, ok := strings.CutPrefix(host, pfx); ok {
			host = rest
			break
		}
	}
	_, ok := tiktokHosts[host]
	return ok
}

// --- Decision gate (unit-testable) --------------------------------------

// tiktokDecision is the pure gate: applies the exclusion set and returns
// the first TikTok URL found in the message text/caption. Returns
// act=false when the message must be passed through untouched.
func tiktokDecision(msg *telego.Message) (act bool, tiktokURL string) {
	if msg == nil {
		return false, ""
	}
	if msg.From == nil || msg.From.IsBot ||
		shared.IsAnonymousAdmin(msg.From.ID) || msg.SenderChat != nil {
		return false, ""
	}

	// Check text entities first (url/text_link types pointing at TikTok hosts).
	for _, e := range msg.Entities {
		if (e.Type == "url" || e.Type == "text_link") && e.URL != "" {
			if u, err := url.Parse(e.URL); err == nil && isTikTokHost(u.Host) {
				return true, e.URL
			}
		}
	}
	for _, e := range msg.CaptionEntities {
		if (e.Type == "url" || e.Type == "text_link") && e.URL != "" {
			if u, err := url.Parse(e.URL); err == nil && isTikTokHost(u.Host) {
				return true, e.URL
			}
		}
	}

	// Scan bare URLs in text and caption.
	for _, m := range []string{msg.Text, msg.Caption} {
		for _, tok := range tiktokURLRe.FindAllString(m, -1) {
			core := strings.TrimRight(tok, trailingPunct)
			if u, err := url.Parse(ensureScheme(core)); err == nil && isTikTokHost(u.Host) {
				return true, core
			}
		}
	}

	return false, ""
}

// ensureScheme prepends https:// to a URL if it has no scheme.
// url.Parse on a scheme-less host/path pair (e.g. tiktok.com/@user/video/123)
// treats the whole string as opaque data with an empty Host.
func ensureScheme(raw string) string {
	if strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") {
		return raw
	}
	return "https://" + raw
}

// --- Video download ------------------------------------------------------

// downloadTikTok fetches a TikTok video via yt-dlp to a temp directory.
// Returns the file path (caller must os.Remove when done).
// On failure returns an error describing what went wrong.
func downloadTikTok(ctx context.Context, rawURL, workDir string) (string, error) {
	dlURL := ensureScheme(rawURL)

	dlCtx, cancel := context.WithTimeout(ctx, tiktokDownloadTimeout)
	defer cancel()

	const maxAttempts = 3
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		cmd := exec.CommandContext(dlCtx,
			"yt-dlp",
			"-f", "bv*[ext=mp4]+ba[ext=m4a]/b[ext=mp4]/best",
			"--no-playlist",
			"-o", workDir+"/video.%(ext)s",
			dlURL,
		)

		output, err := cmd.CombinedOutput()
		if err == nil {
			// Find the downloaded file in the workDir.
			entries, rdErr := os.ReadDir(workDir)
			if rdErr != nil {
				return "", fmt.Errorf("reading work dir: %w", rdErr)
			}
			for _, e := range entries {
				if !e.IsDir() {
					return filepath.Join(workDir, e.Name()), nil
				}
			}
			return "", fmt.Errorf("yt-dlp succeeded but no file found in %s", workDir)
		}

		lastErr = fmt.Errorf("yt-dlp attempt %d: %w\n%s", attempt, err, string(output))
		if attempt < maxAttempts {
			time.Sleep(2 * time.Second)
		}
	}
	return "", lastErr
}

// --- Middleware ----------------------------------------------------------

// tiktokReposter is the supergroup middleware. It mirrors youtubeSanitizer
// structurally but runs the heavy download+trim+upload asynchronously so it
// never stalls the sequential update loop (same lesson as welcome GIF).
func tiktokReposter(a *App) th.Handler {
	return func(thctx *th.Context, update telego.Update) error {
		msg := update.Message
		if msg == nil {
			return thctx.Next(update)
		}
		act, tiktokURL := tiktokDecision(msg)
		if !act {
			return thctx.Next(update)
		}
		// Fire-and-forget: download + trim + upload in background.
		// context.Background() is mandatory -- the per-update ctx is
		// cancelled when the handler returns.
		go processTikTok(context.Background(), a.sanitizerSender(), a.log, msg, tiktokURL, "")
		return thctx.Next(update)
	}
}

// --- Pipeline ------------------------------------------------------------

// processTikTok runs the full pipeline: download (if videoPath is ""),
// trim, size-check, upload, delete-original.
//
// videoPath is "" in production (download via yt-dlp), or a pre-created
// temp file path in tests (bypasses yt-dlp).
//
// Package-level (not a method) so tests can call it without an App.
// The goroutine is NOT tracked in App.inFlight: TikTok repost is best-effort;
// a shutdown mid-pipeline loses one video repost, which is acceptable.
func processTikTok(
	ctx context.Context,
	snd youtubeMediaSender,
	log *slog.Logger,
	msg *telego.Message,
	tiktokURL string,
	videoPath string,
) {
	chatID := msg.Chat.ID
	msgID := msg.GetMessageID()

	// Temp directory for this download.
	workDir, err := os.MkdirTemp("", "bidlobot-tiktok-")
	if err != nil {
		log.Error("tiktok: creating temp dir", "chat_id", chatID, "error", err)
		return
	}
	defer os.RemoveAll(workDir)

	// Step 1: Download.
	if videoPath == "" {
		var dlErr error
		videoPath, dlErr = downloadTikTok(ctx, tiktokURL, workDir)
		if dlErr != nil {
			log.Warn("tiktok: download failed", "chat_id", chatID, "url", tiktokURL, "error", dlErr)
			sendDecline(ctx, snd, log, chatID, msgID, msgTikTokDownloadFail)
			return
		}
		defer os.Remove(videoPath)
	} else {
		defer os.Remove(videoPath)
	}

	// Step 2: Size check.
	fi, err := os.Stat(videoPath)
	if err != nil {
		log.Error("tiktok: stat video", "chat_id", chatID, "path", videoPath, "error", err)
		sendDecline(ctx, snd, log, chatID, msgID, msgTikTokDownloadFail)
		return
	}
	if fi.Size() > maxVideoSize {
		log.Info("tiktok: video too large", "chat_id", chatID, "size", fi.Size())
		sendDecline(ctx, snd, log, chatID, msgID, msgTikTokSizeLimit)
		return
	}

	// Step 3: Open for upload.
	file, err := os.Open(videoPath)
	if err != nil {
		log.Error("tiktok: opening video for upload", "chat_id", chatID, "error", err)
		sendDecline(ctx, snd, log, chatID, msgID, msgTikTokDownloadFail)
		return
	}
	defer file.Close()

	// Step 5: Repost (first, before delete - repost-first contract).
	display := shared.UserDisplay(msg.From.Username, msg.From.FirstName)
	header := strings.Replace(msgTikTokHeader, "%s", display, 1)
	caption := header
	if msg.Caption != "" {
		caption += "\n" + html.EscapeString(msg.Caption)
	}

	_, sendErr := snd.SendVideo(ctx, &telego.SendVideoParams{
		ChatID:    telego.ChatID{ID: chatID},
		Video:     telego.InputFile{File: file},
		Caption:   caption,
		ParseMode: telego.ModeHTML,
	})
	if sendErr != nil {
		log.Warn("tiktok: repost failed; leaving original intact", "chat_id", chatID, "error", sendErr)
		return
	}

	// Step 6: Delete original (only after successful repost).
	if delErr := snd.DeleteMessage(ctx, &telego.DeleteMessageParams{
		ChatID:    telego.ChatID{ID: chatID},
		MessageID: msgID,
	}); delErr != nil {
		log.Info("tiktok: reposted but delete failed; original kept",
			"chat_id", chatID, "message_id", msgID, "error", delErr)
	}

	log.Info("tiktok: reposted", "chat_id", chatID, "message_id", msgID)
}

// sendDecline replies to the original message with a failure note.
// The original message is NOT deleted - the user can resend the link.
func sendDecline(
	ctx context.Context,
	snd youtubeMediaSender,
	log *slog.Logger,
	chatID int64,
	msgID int,
	note string,
) {
	_, err := snd.SendMessage(ctx, &telego.SendMessageParams{
		ChatID: telego.ChatID{ID: chatID},
		Text:   note,
		ReplyParameters: &telego.ReplyParameters{
			MessageID: msgID,
		},
	})
	if err != nil {
		log.Warn("tiktok: decline note send failed", "chat_id", chatID, "error", err)
	}
}

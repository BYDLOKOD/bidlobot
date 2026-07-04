package bot

// TikTok video repost middleware.
//
// When a supergroup message contains a TikTok video link, the bot
// downloads the video (via yt-dlp), trims the watermark end-screen
// (last ~2s, via ffmpeg), reposts it attributed to the original
// sender (display name only, no @, no tg://user?id=), then deletes
// the original. Same privacy gate as the YouTube sanitizer: privacy
// must be OFF.
//
// The download + trim + upload can take 5--15 seconds, so the handler
// fires a background goroutine (like the welcome GIF) rather than
// blocking the update loop.
//
// Design notes / documented v1 gaps:
//   - edited_message: OUT OF SCOPE. Only new messages are processed.
//   - media group / album: only the caption-bearing item processed.
//   - reply / forward context: lost on repost.
//   - no delete right: video repost stands, original remains (repost-first).
//   - bot crash mid-pipeline: original may survive (no data loss since
//     delete only happens after successful SendVideo).
//   - TikTok photo/slideshow: yt-dlp picks best mp4; if none, download
//     fails and the decline note is posted.

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
	"strconv"
	"strings"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"

	"github.com/veschin/bidlobot/internal/shared"
)

// tiktokHosts is the exact set of TikTok hosts that carry video links.
var tiktokHosts = map[string]struct{}{
	"tiktok.com":    {},
	"vm.tiktok.com": {},
}

// tiktokURLRe finds TikTok video URLs in text. Matches:
//
//	https://www.tiktok.com/@user/video/123456789
//	https://vm.tiktok.com/ABCDEF/
//	https://m.tiktok.com/v/123456789.html
//	tiktok.com/@user/video/123456789 (scheme-less, edge case)
var tiktokURLRe = regexp.MustCompile(`(?i)\b((?:https?://)?(?:www\.|m\.)?(?:vm\.)?tiktok\.com[/\S]*[^\s<>"')\]]*)`)

// TikTok repost constants.
const (
	// msgTikTokHeader is the attribution header for a reposted TikTok.
	// %s = sender display name (UserDisplay, no @, no tg://user?id=).
	msgTikTokHeader = "\U0001F464 <b>%s</b> писал(а):"

	// msgTikTokSizeLimit is the decline note when video exceeds Telegram's 50 MB cap.
	msgTikTokSizeLimit = "\u26A0\ufe0f Видео из TikTok слишком большое для загрузки (>50 МБ)."

	// msgTikTokDownloadFail is the note when download/processing fails.
	msgTikTokDownloadFail = "\u26A0\ufe0f Не удалось скачать видео из TikTok."

	// trimDefaultSec is how many seconds to crop from the video end.
	trimDefaultSec = 2.0

	// maxVideoSize is Telegram's bot upload limit for video (50 MB).
	maxVideoSize = 50 * 1024 * 1024
)

// tiktokReposter is the supergroup middleware for TikTok video repost.
func tiktokReposter(a *App) th.Handler {
	return func(ctx *th.Context, update telego.Update) error {
		msg := update.Message
		if msg == nil {
			return ctx.Next(update)
		}
		act, tiktokURL := tiktokDecision(msg)
		if !act {
			return ctx.Next(update)
		}
		// Fire-and-forget: download + trim + upload in background.
		// context.Background() is mandatory -- the per-update ctx is
		// cancelled when the handler returns.
		go processTikTok(context.Background(), a.sanitizerSender(), a.log, msg, tiktokURL, "")
		return ctx.Next(update)
	}
}

// tiktokDecision is the pure gate: applies the exclusion set and returns
// the first TikTok URL found in the message text/caption. Returns
// act=false when the message must be passed through untouched.
func tiktokDecision(msg *telego.Message) (act bool, tiktokURL string) {
	if msg.From == nil || msg.From.IsBot || shared.IsAnonymousAdmin(msg.From.ID) || msg.SenderChat != nil {
		return false, ""
	}

	// Scan text body for TikTok URLs.
	text := firstNonEmpty(msg.Text, msg.Caption)
	if text != "" {
		if u := firstTikTokURL(text); u != "" {
			return true, u
		}
	}

	// Scan entities for url/text_link pointing at TikTok hosts.
	for _, e := range msg.Entities {
		if e.Type == "url" || e.Type == "text_link" {
			if isTikTokHostURL(e.URL) {
				return true, e.URL
			}
		}
	}
	return false, ""
}

// firstTikTokURL extracts the first TikTok URL from text via the regex
// scanner, stripping trailing sentence punctuation.
func firstTikTokURL(text string) string {
	match := tiktokURLRe.FindString(text)
	if match == "" {
		return ""
	}
	return strings.TrimRight(match, trailingPunct)
}

// isTikTokHostURL parses rawURL and checks whether its host is a TikTok host.
func isTikTokHostURL(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	host = strings.TrimPrefix(host, "www.")
	host = strings.TrimPrefix(host, "m.")
	_, ok := tiktokHosts[host]
	return ok
}

// processTikTok runs the full pipeline: download (if videoPath is ""),
// trim, size-check, upload, delete-original.
// videoPath is "" in production (download via yt-dlp), or a pre-created
// temp file path in tests (bypasses yt-dlp).
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

	// Create a temp work directory for downloads.
	workDir, err := os.MkdirTemp("", "bidlobot-tiktok-*")
	if err != nil {
		log.Error("tiktok reposter: failed to create temp dir",
			"chat_id", chatID, "message_id", msgID, "error", err)
		return
	}
	defer os.RemoveAll(workDir)

	downloaded := videoPath != ""

	if !downloaded {
		path, err := downloadTikTok(ctx, tiktokURL, workDir)
		if err != nil {
			log.Warn("tiktok reposter: download failed",
				"chat_id", chatID, "message_id", msgID, "url", tiktokURL, "error", err)
			sendTikTokFallback(ctx, snd, log, chatID, msgID, msgTikTokDownloadFail)
			return
		}
		videoPath = path
		defer os.Remove(videoPath)
	}

	// Trim watermark end-screen.
	trimmed, trimErr := trimVideoEnd(ctx, videoPath, workDir, trimDefaultSec)
	if trimErr != nil {
		log.Warn("tiktok reposter: trim failed, posting untrimmed",
			"chat_id", chatID, "message_id", msgID, "error", trimErr)
	} else if trimmed != videoPath {
		defer os.Remove(trimmed)
		videoPath = trimmed
	}

	// Size check.
	fi, err := os.Stat(videoPath)
	if err != nil {
		log.Error("tiktok reposter: stat failed",
			"chat_id", chatID, "message_id", msgID, "path", videoPath, "error", err)
		sendTikTokFallback(ctx, snd, log, chatID, msgID, msgTikTokDownloadFail)
		return
	}
	if fi.Size() > maxVideoSize {
		log.Info("tiktok reposter: video too large",
			"chat_id", chatID, "message_id", msgID, "size", fi.Size())
		sendTikTokFallback(ctx, snd, log, chatID, msgID, msgTikTokSizeLimit)
		return
	}

	// Build attribution header.
	display := shared.UserDisplay(msg.From.Username, msg.From.FirstName)
	header := strings.Replace(msgTikTokHeader, "%s", html.EscapeString(display), 1)

	// Upload the video.
	file, err := os.Open(videoPath)
	if err != nil {
		log.Error("tiktok reposter: open failed",
			"chat_id", chatID, "message_id", msgID, "path", videoPath, "error", err)
		sendTikTokFallback(ctx, snd, log, chatID, msgID, msgTikTokDownloadFail)
		return
	}
	defer file.Close()

	_, err = snd.SendVideo(ctx, &telego.SendVideoParams{
		ChatID:    telego.ChatID{ID: chatID},
		Video:     telego.InputFile{File: file},
		Caption:   header,
		ParseMode: telego.ModeHTML,
	})
	if err != nil {
		log.Error("tiktok reposter: SendVideo failed",
			"chat_id", chatID, "message_id", msgID, "error", err)
		return
	}

	// Delete original only after successful repost.
	if delErr := snd.DeleteMessage(ctx, &telego.DeleteMessageParams{
		ChatID:    telego.ChatID{ID: chatID},
		MessageID: msgID,
	}); delErr != nil {
		log.Info("tiktok reposter: reposted but delete failed; original kept",
			"chat_id", chatID, "message_id", msgID, "error", delErr)
	}
}

// sendTikTokFallback posts a note in the chat and best-effort deletes the original.
func sendTikTokFallback(
	ctx context.Context,
	snd youtubeMediaSender,
	log *slog.Logger,
	chatID int64,
	msgID int,
	note string,
) {
	_, _ = snd.SendMessage(ctx, &telego.SendMessageParams{
		ChatID:    telego.ChatID{ID: chatID},
		Text:      note,
		ParseMode: telego.ModeHTML,
	})
	// Best-effort delete.
	if err := snd.DeleteMessage(ctx, &telego.DeleteMessageParams{
		ChatID:    telego.ChatID{ID: chatID},
		MessageID: msgID,
	}); err != nil {
		log.Info("tiktok reposter: fallback delete failed",
			"chat_id", chatID, "message_id", msgID, "error", err)
	}
}

// downloadTikTok fetches a TikTok video via yt-dlp to a temp file.
// Returns the file path (caller must os.Remove when done).
func downloadTikTok(ctx context.Context, tiktokURL, workDir string) (string, error) {
	// yt-dlp format selection: prefer mp4 video + m4a audio, fall back to best mp4.
	outTmpl := filepath.Join(workDir, "%(id)s.%(ext)s")

	cmd := exec.CommandContext(ctx,
		"yt-dlp",
		"-f", "bv*[ext=mp4]+ba[ext=m4a]/b[ext=mp4]/best",
		"--no-playlist",
		"-o", outTmpl,
		"--print", "filename",
		tiktokURL,
	)

	stdout, err := cmd.Output()
	if err != nil {
		var stderr string
		if exitErr, ok := err.(*exec.ExitError); ok {
			stderr = string(exitErr.Stderr)
		}
		return "", fmt.Errorf("yt-dlp: %w (stderr: %s)", err, stderr)
	}

	filename := strings.TrimSpace(string(stdout))
	if filename == "" {
		return "", fmt.Errorf("yt-dlp: no output filename for %s", tiktokURL)
	}
	return filename, nil
}

// videoDurationSec returns the duration of a video file in seconds via ffprobe.
func videoDurationSec(ctx context.Context, videoPath string) (float64, error) {
	cmd := exec.CommandContext(ctx,
		"ffprobe",
		"-v", "error",
		"-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1",
		videoPath,
	)
	stdout, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("ffprobe: %w", err)
	}
	dur, err := strconv.ParseFloat(strings.TrimSpace(string(stdout)), 64)
	if err != nil {
		return 0, fmt.Errorf("ffprobe parse: %w", err)
	}
	return dur, nil
}

// trimVideoEnd crops the last trimSeconds from videoPath, writing to a new temp
// file. Uses ffmpeg stream copy (no re-encode). Returns the cropped file path
// (caller must os.Remove when done). If ffmpeg is unavailable or fails, returns
// the original path unchanged.
func trimVideoEnd(ctx context.Context, videoPath, workDir string, trimSeconds float64) (string, error) {
	dur, err := videoDurationSec(ctx, videoPath)
	if err != nil {
		return videoPath, fmt.Errorf("ffprobe duration: %w", err)
	}

	// Don't trim very short videos.
	if dur <= trimSeconds+1.0 {
		return videoPath, nil
	}

	outPath := filepath.Join(workDir, "trimmed.mp4")
	trimDur := dur - trimSeconds
	cmd := exec.CommandContext(ctx,
		"ffmpeg",
		"-y",
		"-i", videoPath,
		"-t", strconv.FormatFloat(trimDur, 'f', 3, 64),
		"-c", "copy",
		"-avoid_negative_ts", "make_zero",
		outPath,
	)
	if _, err := cmd.Output(); err != nil {
		return videoPath, fmt.Errorf("ffmpeg trim: %w", err)
	}
	return outPath, nil
}

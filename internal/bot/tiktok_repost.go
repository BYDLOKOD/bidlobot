package bot

// TikTok video repost with deferred-export queue.
//
// When a supergroup message contains a TikTok video link, the bot downloads
// the video via yt-dlp, checks that it has an audio stream (TikTok sometimes
// serves a muted variant to non-browser clients), reposts it attributed to
// the original sender (display name only, no @, no tg://user?id=), then
// deletes the original.
//
// If the download fails (TikTok anti-bot block) or the video has no audio,
// the job is persisted to the per-user deferred queue (BoltDB) instead of
// being abandoned. The original message is NOT deleted - it stays as a
// fallback. The user can flush their queue with /flush (supergroup command)
// to retry all pending exports. Entries expire after 48 hours.
//
// Privacy gate: same as the YouTube sanitizer - privacy must be OFF.
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
	"encoding/json"
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
	"github.com/veschin/bidlobot/internal/storage"
)

// --- Constants -----------------------------------------------------------
const (
	// msgTikTokHeader is the attribution header for a reposted TikTok.
	// %s = sender display name (UserDisplay, no @, no tg://user?id=).
	msgTikTokHeader = "\U0001F464 <b>%s</b> \u043F\u0438\u0441\u0430\u043B(\u0430):"
)

const (

	// maxVideoSize is Telegram's bot upload limit for video (50 MB).
	maxVideoSize = 50 * 1024 * 1024

	// tiktokDownloadTimeout caps the yt-dlp invocation.
	tiktokDownloadTimeout = 60 * time.Second
)

// --- Deferred queue interface --------------------------------------------

// DeferredQueuer is the persistence surface for per-user deferred jobs
// (TikTok exports, summarize retries). nil (not wired) means failures
// fall back to a public decline reply instead of being queued.
type DeferredQueuer interface {
	Enqueue(ctx context.Context, job storage.DeferredJob) error
	ListByUser(ctx context.Context, userID int64) ([]storage.DeferredJob, error)
	Delete(ctx context.Context, key string) error
	GarbageCollect(ctx context.Context, before time.Time) (int, error)
}

// ffprobeHasAudio reports whether a video file has an audio stream.
// Package-level so tests can substitute a stub.
var ffprobeHasAudio = defaultFFprobeHasAudio

// defaultFFprobeHasAudio runs ffprobe to check for an audio stream. If
// ffprobe is not installed or fails on a file, it degrades to "assume
// audio present" so a broken ffprobe never blocks all TikTok reposts.
func defaultFFprobeHasAudio(path string) bool {
	if _, err := exec.LookPath("ffprobe"); err != nil {
		return true
	}
	cmd := exec.Command("ffprobe", "-v", "error", "-select_streams", "a",
		"-show_entries", "stream=codec_type", "-of", "csv=p=0", path)
	output, err := cmd.Output()
	if err != nil {
		return true
	}
	return strings.TrimSpace(string(output)) == "audio"
}

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
			// Prefer an h264 single-file variant: TikTok serves the bytevc1
			// (h265) variant muted to non-browser clients, and the ffprobe
			// audio gate would reject it. h264 variants carry aac audio.
			"-f", "b[vcodec=h264]/b",
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
// structurally but runs the heavy download+upload asynchronously so it
// never stalls the sequential update loop (same lesson as welcome GIF).
func tiktokReposter(a *App) th.Handler {
	return func(thctx *th.Context, update telego.Update) error {
		msg := update.Message
		if msg == nil {
			return thctx.Next(update)
		}
		// Comment permalinks are also video URLs: quote the comment instead
		// of replaying the video.
		if act, videoURL, commentID := tiktokCommentDecision(msg); act {
			go processTikTokComment(context.Background(), a.sanitizerSender(), a.log,
				tiktokCommentHTTPClient, a.repReactor, msg, videoURL, commentID)
			return thctx.Next(update)
		}
		act, tiktokURL := tiktokDecision(msg)
		if !act {
			return thctx.Next(update)
		}
		// Fire-and-forget: download + validate + upload in background.
		// context.Background() is mandatory -- the per-update ctx is
		// cancelled when the handler returns.
		go processTikTok(context.Background(), a.sanitizerSender(), a.log,
			a.deferredQ, a.repReactor, msg, tiktokURL, "")
		return thctx.Next(update)
	}
}

// --- Pipeline ------------------------------------------------------------

// processTikTok runs the full pipeline: download (if videoPath is ""),
// size-check, audio-check, upload, delete-original.
//
// On download failure or no-audio: the job is enqueued for later flush
// (if a queue is wired) instead of being abandoned. The original message
// is always left intact until a successful repost.
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
	queue DeferredQueuer,
	owners ownerRecorder,
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
			log.Warn("tiktok: download failed, queuing", "chat_id", chatID, "url", tiktokURL, "error", dlErr)
			enqueueOrFail(ctx, snd, log, queue, msg, tiktokURL)
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
		sendDecline(ctx, snd, log, chatID, msgID, publicPureFailure(), "tiktok: decline note send failed")
		return
	}
	if fi.Size() > maxVideoSize {
		log.Info("tiktok: video too large", "chat_id", chatID, "size", fi.Size())
		sendDecline(ctx, snd, log, chatID, msgID, publicPureFailure(), "tiktok: decline note send failed")
		return
	}

	// Step 3: Audio check. TikTok sometimes serves a muted variant to
	// non-browser clients; never upload a silent video.
	if !ffprobeHasAudio(videoPath) {
		log.Warn("tiktok: no audio stream, queuing", "chat_id", chatID, "url", tiktokURL)
		enqueueOrFail(ctx, snd, log, queue, msg, tiktokURL)
		return
	}

	// Step 4: Open for upload.
	file, err := os.Open(videoPath)
	if err != nil {
		log.Error("tiktok: opening video for upload", "chat_id", chatID, "error", err)
		sendDecline(ctx, snd, log, chatID, msgID, publicPureFailure(), "tiktok: decline note send failed")
		return
	}
	defer file.Close()

	// Step 5: Repost (first, before delete - repost-first contract).
	sent, sendErr := snd.SendVideo(ctx, &telego.SendVideoParams{
		ChatID:    telego.ChatID{ID: chatID},
		Video:     telego.InputFile{File: file},
		Caption:   tiktokCaption(msg.From.Username, msg.From.FirstName, msg.Caption),
		ParseMode: telego.ModeHTML,
	})
	if sendErr != nil {
		log.Warn("tiktok: repost failed; leaving original intact", "chat_id", chatID, "error", sendErr)
		sendDecline(ctx, snd, log, chatID, msgID, publicPureFailure(), "tiktok: decline note send failed")
		return
	}

	if owners != nil {
		owners.RecordOwner(chatID, sent.GetMessageID(), msg.From)
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

// tiktokCaption builds the HTML caption for a reposted TikTok: attribution
// header (display name only) plus the original caption if any.
func tiktokCaption(username, firstName, rawCaption string) string {
	display := shared.UserDisplay(username, firstName)
	caption := strings.Replace(msgTikTokHeader, "%s", display, 1)
	if rawCaption != "" {
		caption += "\n" + html.EscapeString(rawCaption)
	}
	return caption
}

// enqueueOrFail enqueues a deferred TikTok job for the calling user if a
// queue is wired. If the queue is nil or the enqueue fails, it falls back
// to a public decline reply so the user is not left in silence.
func enqueueOrFail(
	ctx context.Context,
	snd youtubeMediaSender,
	log *slog.Logger,
	queue DeferredQueuer,
	msg *telego.Message,
	tiktokURL string,
) {
	if queue != nil {
		payload, _ := json.Marshal(storage.TikTokPayload{
			URL:       tiktokURL,
			Username:  msg.From.Username,
			FirstName: msg.From.FirstName,
			Caption:   msg.Caption,
		})
		job := storage.DeferredJob{
			UserID:    msg.From.ID,
			Type:      storage.DeferredTikTok,
			ChatID:    msg.Chat.ID,
			MessageID: msg.GetMessageID(),
			Payload:   payload,
			CreatedAt: time.Now().UTC(),
		}
		if err := queue.Enqueue(ctx, job); err != nil {
			log.Error("tiktok: enqueue failed, falling back to decline",
				"chat_id", msg.Chat.ID, "error", err)
		} else {
			log.Info("tiktok: queued for later export",
				"chat_id", msg.Chat.ID, "url", tiktokURL)
			return
		}
	}
	sendDecline(ctx, snd, log, msg.Chat.ID, msg.GetMessageID(),
		publicPureFailure(), "tiktok: decline note send failed")
}

// tryTikTokExport attempts the full download->validate->upload->delete
// cycle. Returns nil on success (caller removes from queue), error on
// any failure (caller keeps in queue for next flush).
func tryTikTokExport(
	ctx context.Context,
	snd youtubeMediaSender,
	log *slog.Logger,
	owners ownerRecorder,
	chatID int64,
	msgID int,
	userID int64,
	url, username, firstName, caption string,
) error {
	workDir, err := os.MkdirTemp("", "bidlobot-tiktok-")
	if err != nil {
		return fmt.Errorf("temp dir: %w", err)
	}
	defer os.RemoveAll(workDir)

	videoPath, err := downloadTikTok(ctx, url, workDir)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	defer os.Remove(videoPath)

	fi, err := os.Stat(videoPath)
	if err != nil {
		return fmt.Errorf("stat: %w", err)
	}
	if fi.Size() > maxVideoSize {
		return fmt.Errorf("too large (%d bytes)", fi.Size())
	}

	if !ffprobeHasAudio(videoPath) {
		return fmt.Errorf("no audio stream")
	}

	file, err := os.Open(videoPath)
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	defer file.Close()

	sent, sendErr := snd.SendVideo(ctx, &telego.SendVideoParams{
		ChatID:    telego.ChatID{ID: chatID},
		Video:     telego.InputFile{File: file},
		Caption:   tiktokCaption(username, firstName, caption),
		ParseMode: telego.ModeHTML,
	})
	if sendErr != nil {
		return fmt.Errorf("send: %w", sendErr)
	}
	if owners != nil {
		owners.RecordOwner(chatID, sent.GetMessageID(), &telego.User{
			ID:        userID,
			Username:  username,
			FirstName: firstName,
		})
	}

	if delErr := snd.DeleteMessage(ctx, &telego.DeleteMessageParams{
		ChatID:    telego.ChatID{ID: chatID},
		MessageID: msgID,
	}); delErr != nil {
		log.Info("tiktok flush: reposted but delete failed; original kept",
			"chat_id", chatID, "message_id", msgID, "error", delErr)
	}

	log.Info("tiktok flush: reposted", "chat_id", chatID, "message_id", msgID, "url", url)
	return nil
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
	failureEvent string,
) {
	_, err := snd.SendMessage(ctx, &telego.SendMessageParams{
		ChatID: telego.ChatID{ID: chatID},
		Text:   note,
		ReplyParameters: &telego.ReplyParameters{
			MessageID: msgID,
		},
	})
	if err != nil {
		log.Warn(failureEvent, "chat_id", chatID, "message_id", msgID, "error", err)
	}
}

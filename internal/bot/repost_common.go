package bot

// Shared parts of the video reposters. The TikTok reposter
// (tiktok_repost.go) and the Instagram reposter (instagram_repost.go) are
// one pipeline around different link detectors and downloaders: find a
// permalink, download the video with yt-dlp under a retry ladder, repost
// it attributed to the sender, delete the original, and queue the job for
// /flush when the failure is replayable. Everything in this file is
// service-agnostic. The per-service facts - hosts, path shapes, format
// selectors, egress options, permanent-failure strings, the audio gate -
// stay in the service files.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/mymmrac/telego"
	"github.com/mymmrac/telego/telegoapi"

	"github.com/veschin/bidlobot/internal/shared"
	"github.com/veschin/bidlobot/internal/storage"
)

// --- Constants -----------------------------------------------------------

const (
	// msgRepostHeader is the attribution header of a reposted video.
	// %s = sender display name (UserDisplay, no @, no tg://user?id=).
	msgRepostHeader = "\U0001F464 <b>%s</b> \u043F\u0438\u0441\u0430\u043B(\u0430):"

	// maxVideoSize is Telegram's bot upload limit for video (50 MB).
	maxVideoSize = 50 * 1024 * 1024

	// downloadAttempts is how many yt-dlp invocations one repost gets.
	downloadAttempts = 3

	// downloadAttemptTimeout caps one yt-dlp invocation. The attempts get
	// separate deadlines: one shared deadline leaves the last attempt
	// with whatever the first ones did not spend, usually nothing.
	downloadAttemptTimeout = 45 * time.Second
)

// --- Interfaces ----------------------------------------------------------

// DeferredQueuer is the persistence surface for per-user deferred jobs
// (TikTok and Instagram exports, summarize retries). nil (not wired)
// means failures fall back to a public decline reply instead of being
// queued.
type DeferredQueuer interface {
	Enqueue(ctx context.Context, job storage.DeferredJob) error
	ListByUser(ctx context.Context, userID int64) ([]storage.DeferredJob, error)
	Delete(ctx context.Context, key string) error
	GarbageCollect(ctx context.Context, before time.Time) (int, error)
}

// errNoVideo marks a post that will never yield a video: a photo post, an
// image carousel, or an answer the extractor cannot describe to an
// anonymous client. Both reposters decline it instead of queueing a job
// no retry can complete.
var errNoVideo = errors.New("no video stream in this post")

// --- Sender gate ---------------------------------------------------------

// repostableSender reports whether the message may be acted on at all:
// a real human sender, no bots, no anonymous admins, no channel-as-sender
// (linked channel) and no missing sender. Shared by the link detectors.
func repostableSender(msg *telego.Message) bool {
	return msg != nil && msg.From != nil && !msg.From.IsBot &&
		!shared.IsAnonymousAdmin(msg.From.ID) && msg.SenderChat == nil
}

// --- Small helpers -------------------------------------------------------

// ensureScheme prepends https:// to a URL if it has no scheme.
// url.Parse on a scheme-less host/path pair (e.g. tiktok.com/@user/video/123)
// treats the whole string as opaque data with an empty Host.
func ensureScheme(raw string) string {
	if strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") {
		return raw
	}
	return "https://" + raw
}

// previewOutput trims captured subprocess output so one failure does not
// dump kilobytes of yt-dlp noise into a structured log record.
func previewOutput(b []byte, maxRunes int) string {
	s := strings.TrimSpace(string(b))
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	return string(r[:maxRunes]) + "...(truncated)"
}

// hostAllowed lower-cases host, drops any port, strips a single leading
// label from strip, and checks the exact allowlist. strip is empty when a
// service's links carry no subdomain.
func hostAllowed(host string, strip []string, allow map[string]struct{}) bool {
	host = strings.ToLower(host)
	if h, _, ok := strings.Cut(host, ":"); ok {
		host = h
	}
	for _, pfx := range strip {
		if rest, ok := strings.CutPrefix(host, pfx); ok {
			host = rest
			break
		}
	}
	_, ok := allow[host]
	return ok
}

// fileFromWorkDir returns the media file a successful yt-dlp run wrote
// into workDir. Callers clean up anything left behind with os.Remove.
func fileFromWorkDir(workDir string) (string, error) {
	entries, err := os.ReadDir(workDir)
	if err != nil {
		return "", fmt.Errorf("reading work dir: %w", err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			return filepath.Join(workDir, e.Name()), nil
		}
	}
	return "", fmt.Errorf("yt-dlp succeeded but no file found in %s", workDir)
}

// repostCaption builds the HTML caption of a reposted video: attribution
// header (display name only, never an @ or a user link) plus the original
// caption if any.
func repostCaption(username, firstName, rawCaption string) string {
	display := shared.UserDisplay(username, firstName)
	caption := strings.Replace(msgRepostHeader, "%s", display, 1)
	if rawCaption != "" {
		caption += "\n" + html.EscapeString(rawCaption)
	}
	return caption
}

// --- yt-dlp --------------------------------------------------------------

// ytDlpBinary is the downloader executable. A variable so tests can point
// the download at a stub script.
var ytDlpBinary = "yt-dlp"

// ytDlpRun is one service's yt-dlp invocation: its format selector and
// egress flags, plus an optional classifier that turns the captured
// output into a permanent error (errNoVideo) instead of a retryable one.
type ytDlpRun struct {
	args     []string
	classify func(output []byte) error
}

// ytDlpRetry runs yt-dlp up to downloadAttempts times, each attempt under
// its own deadline. A permanent "no video" answer (errNoVideo) stops the
// ladder: the remaining attempts cannot change it.
func ytDlpRetry(ctx context.Context, rawURL, workDir string, run ytDlpRun) (string, error) {
	dlURL := ensureScheme(rawURL)

	var lastErr error
	for attempt := 1; attempt <= downloadAttempts; attempt++ {
		path, err := ytDlpAttempt(ctx, dlURL, workDir, run)
		if err == nil {
			return path, nil
		}
		if errors.Is(err, errNoVideo) {
			return "", err
		}
		lastErr = fmt.Errorf("yt-dlp attempt %d: %w", attempt, err)
		if attempt < downloadAttempts {
			time.Sleep(2 * time.Second)
		}
	}
	return "", lastErr
}

// ytDlpAttempt runs one yt-dlp invocation and returns the file it wrote
// into workDir.
func ytDlpAttempt(ctx context.Context, dlURL, workDir string, run ytDlpRun) (string, error) {
	dlCtx, cancel := context.WithTimeout(ctx, downloadAttemptTimeout)
	defer cancel()

	args := append(append([]string{}, run.args...),
		"--no-playlist",
		"-o", workDir+"/video.%(ext)s",
		dlURL,
	)
	cmd := exec.CommandContext(dlCtx, ytDlpBinary, args...)

	output, err := cmd.CombinedOutput()
	if err == nil {
		return fileFromWorkDir(workDir)
	}
	if run.classify != nil {
		if cerr := run.classify(output); cerr != nil {
			return "", cerr
		}
	}
	return "", fmt.Errorf("%w\n%s", err, previewOutput(output, 400))
}

// isAPIPermanent reports whether err is a Telegram rejection no retry can
// fix: any 4xx except 429. A transport fault never reached Telegram, and a
// 429 or 5xx outlived the sender's own retry ladder - both are replayable.
func isAPIPermanent(err error) bool {
	var apiErr *telegoapi.Error
	return errors.As(err, &apiErr) && apiErr.ErrorCode < 500 && apiErr.ErrorCode != 429
}

// --- Repost tail ---------------------------------------------------------

// repostOutcome is how an upload attempt ended: sent (the caller may
// finish its bookkeeping), permanent (Telegram refused - a decline note
// stands, no retry can help) or transient (replayable, the caller queues
// it for /flush).
type repostOutcome int

const (
	repostSent repostOutcome = iota
	repostPermanent
	repostTransient
)

// repostVideoTail uploads an already-downloaded video attributed to the
// sender and, on success, deletes the original message. tag is the log
// prefix ("tiktok", "instagram", "tiktok flush", ...). The caller owns the
// decline note, the deferred queue and its own bookkeeping (the TikTok
// repost index).
func repostVideoTail(
	ctx context.Context,
	snd youtubeMediaSender,
	log *slog.Logger,
	owners ownerRecorder,
	owner *telego.User,
	chatID int64,
	msgID int,
	videoPath, caption, tag string,
) (*telego.Message, repostOutcome, error) {
	fi, err := os.Stat(videoPath)
	if err != nil {
		log.Error(tag+": stat video", "chat_id", chatID, "path", videoPath, "error", err)
		return nil, repostPermanent, err
	}
	if fi.Size() > maxVideoSize {
		log.Info(tag+": video too large", "chat_id", chatID, "size", fi.Size())
		return nil, repostPermanent, fmt.Errorf("too large (%d bytes)", fi.Size())
	}

	file, err := os.Open(videoPath)
	if err != nil {
		log.Error(tag+": opening video for upload", "chat_id", chatID, "error", err)
		return nil, repostPermanent, err
	}
	defer file.Close()

	// Repost first, delete after: a failed upload leaves the original.
	sent, sendErr := snd.SendVideo(ctx, &telego.SendVideoParams{
		ChatID:    telego.ChatID{ID: chatID},
		Video:     telego.InputFile{File: file},
		Caption:   caption,
		ParseMode: telego.ModeHTML,
	})
	if sendErr != nil {
		// Permanent = Telegram answered 4xx (bad file, chat forbidden,
		// too large): a decline note stands. Everything else is
		// replayable - a transport fault never reached Telegram, 429 and
		// 5xx outlived the retry ladder - so the caller queues it.
		if isAPIPermanent(sendErr) {
			log.Warn(tag+": repost rejected by the API; leaving original intact",
				"chat_id", chatID, "error", sendErr)
			return nil, repostPermanent, sendErr
		}
		log.Warn(tag+": repost failed transiently, queuing",
			"chat_id", chatID, "error", sendErr)
		return nil, repostTransient, sendErr
	}

	if owners != nil && owner != nil {
		owners.RecordOwner(chatID, sent.GetMessageID(), owner)
	}

	// Delete the original only after a successful repost.
	if delErr := snd.DeleteMessage(ctx, &telego.DeleteMessageParams{
		ChatID:    telego.ChatID{ID: chatID},
		MessageID: msgID,
	}); delErr != nil {
		log.Info(tag+": reposted but delete failed; original kept",
			"chat_id", chatID, "message_id", msgID, "error", delErr)
	}

	log.Info(tag+": reposted", "chat_id", chatID, "message_id", msgID)
	return sent, repostSent, nil
}

// --- Deferred queue ------------------------------------------------------

// enqueueRepostOrFail enqueues a deferred video-repost job for the calling
// user if a queue is wired. If the queue is nil or the enqueue fails, it
// falls back to a public decline reply so the user is not left in silence.
// jobType doubles as the log prefix (storage.DeferredTikTok / ...Instagram).
func enqueueRepostOrFail(
	ctx context.Context,
	snd youtubeMediaSender,
	log *slog.Logger,
	queue DeferredQueuer,
	msg *telego.Message,
	jobType string,
	sourceURL string,
) {
	if queue != nil {
		payload, _ := json.Marshal(storage.RepostPayload{
			URL:       sourceURL,
			Username:  msg.From.Username,
			FirstName: msg.From.FirstName,
			Caption:   msg.Caption,
		})
		job := storage.DeferredJob{
			UserID:    msg.From.ID,
			Type:      jobType,
			ChatID:    msg.Chat.ID,
			MessageID: msg.GetMessageID(),
			Payload:   payload,
			CreatedAt: time.Now().UTC(),
		}
		if err := queue.Enqueue(ctx, job); err != nil {
			log.Error(jobType+": enqueue failed, falling back to decline",
				"chat_id", msg.Chat.ID, "error", err)
		} else {
			log.Info(jobType+": queued for later export",
				"chat_id", msg.Chat.ID, "url", sourceURL)
			return
		}
	}
	sendDecline(ctx, snd, log, msg.Chat.ID, msg.GetMessageID(),
		publicPureFailure(), jobType+": decline note send failed")
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

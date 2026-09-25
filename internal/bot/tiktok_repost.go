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
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"

	"github.com/veschin/bidlobot/internal/shared"
	"github.com/veschin/bidlobot/internal/storage"
)

// --- TikTok link detection -----------------------------------------------

// --- Video repost index interface ----------------------------------------

// tiktokVideoIndex records which TikTok videos the bot has reposted into
// which chat and where. The comment-quote pipeline consults it to reply
// to a video already in the chat. nil (not wired) means quotes always
// stand alone and reposts are not recorded.
type tiktokVideoIndex interface {
	RecordVideo(ctx context.Context, chatID int64, videoID string, msgID int) error
	FindVideo(ctx context.Context, chatID int64, videoID string) (int, bool, error)
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

// isTikTokHost normalizes host and checks the exact allowlist.
func isTikTokHost(host string) bool {
	return hostAllowed(host, []string{"www.", "m.", "vm.", "vt."}, tiktokHosts)
}

// --- Decision gate (unit-testable) --------------------------------------

// tiktokDecision is the pure gate: applies the exclusion set and returns
// the first TikTok URL found in the message text/caption. Returns
// act=false when the message must be passed through untouched.
func tiktokDecision(msg *telego.Message) (act bool, tiktokURL string) {
	if !repostableSender(msg) {
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

// --- Video download ------------------------------------------------------

// ytdlpDownload is the yt-dlp fallback path; a variable so tests can
// substitute a stub.
var ytdlpDownload = downloadTikTokViaYtDlp

// downloadTikTok fetches a TikTok video for repost: the tikwm mirror
// first (it resolves and serves the file without any request to TikTok
// web, which the ISP filters by TLS SNI), yt-dlp as the fallback.
// Returns the file path (caller must os.Remove when done).
func downloadTikTok(ctx context.Context, rawURL, workDir string) (string, error) {
	mctx, cancel := context.WithTimeout(ctx, mirrorDownloadTimeout)
	defer cancel()
	path, mirrorErr := downloadFromMirror(mctx, tiktokCommentHTTPClient, rawURL, workDir)
	if mirrorErr == nil {
		return path, nil
	}
	if errors.Is(mirrorErr, errNoVideo) {
		return "", mirrorErr
	}
	path, dlpErr := ytdlpDownload(ctx, rawURL, workDir)
	if dlpErr == nil {
		return path, nil
	}
	return "", fmt.Errorf("mirror: %w; yt-dlp: %w", mirrorErr, dlpErr)
}

// downloadTikTokViaYtDlp runs the yt-dlp fallback for a TikTok link.
func downloadTikTokViaYtDlp(ctx context.Context, rawURL, workDir string) (string, error) {
	return ytDlpRetry(ctx, rawURL, workDir, ytDlpRun{
		// Prefer an h264 single-file variant: TikTok serves the bytevc1
		// (h265) variant muted to non-browser clients, and the ffprobe
		// audio gate would reject it. h264 variants carry aac audio.
		args: []string{"-f", "b[vcodec=h264]/b"},
	})
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
			shared.Go(a.log, "tiktok-comment", func() {
				processTikTokComment(context.Background(), a.sanitizerSender(), a.log,
					tiktokCommentHTTPClient, a.repReactor, a.tiktokVideos, msg, videoURL, commentID)
			})
			return thctx.Next(update)
		}
		act, tiktokURL := tiktokDecision(msg)
		if !act {
			return thctx.Next(update)
		}
		// Short links (vm./vt.) may hide a comment permalink behind their
		// redirect (the app's share button on a comment produces exactly
		// that). Resolve first, then dispatch: comment quote or video
		// replay. Fire-and-forget either way - context.Background() is
		// mandatory, the per-update ctx is cancelled when the handler
		// returns.
		if isTikTokShortLink(tiktokURL) {
			snd := a.sanitizerSender()
			shared.Go(a.log, "tiktok-shortlink", func() {
				ctx := context.Background()
				if final := resolveTikTokURL(ctx, tiktokCommentHTTPClient, tiktokURL); final != "" {
					if videoURL, commentID, ok := tiktokCommentIDFromURL(final); ok {
						processTikTokComment(ctx, snd, a.log, tiktokCommentHTTPClient, a.repReactor, a.tiktokVideos, msg, videoURL, commentID)
						return
					}
					// Pass the resolved long URL so the video id can be
					// extracted for the repost index.
					processTikTok(ctx, snd, a.log, a.deferredQ, a.tiktokVideos, a.repReactor, msg, final, "")
					return
				}
				processTikTok(ctx, snd, a.log, a.deferredQ, a.tiktokVideos, a.repReactor, msg, tiktokURL, "")
			})
			return thctx.Next(update)
		}
		shared.Go(a.log, "tiktok", func() {
			processTikTok(context.Background(), a.sanitizerSender(), a.log,
				a.deferredQ, a.tiktokVideos, a.repReactor, msg, tiktokURL, "")
		})
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
	videos tiktokVideoIndex,
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
			if errors.Is(dlErr, errNoVideo) {
				log.Info("tiktok: photo post has no video, declining", "chat_id", chatID, "url", tiktokURL)
				sendDecline(ctx, snd, log, chatID, msgID, publicPureFailure(), "tiktok: decline note send failed")
				return
			}
			log.Warn("tiktok: download failed, queuing", "chat_id", chatID, "url", tiktokURL, "error", dlErr)
			enqueueRepostOrFail(ctx, snd, log, queue, msg, storage.DeferredTikTok, tiktokURL)
			return
		}
		defer os.Remove(videoPath)
	} else {
		defer os.Remove(videoPath)
	}

	// Step 2: Audio check. TikTok sometimes serves a muted variant to
	// non-browser clients; never upload a silent video.
	if !ffprobeHasAudio(videoPath) {
		log.Warn("tiktok: no audio stream, queuing", "chat_id", chatID, "url", tiktokURL)
		enqueueRepostOrFail(ctx, snd, log, queue, msg, storage.DeferredTikTok, tiktokURL)
		return
	}

	// Step 3: Repost (upload first, delete after).
	sent, out, _ := repostVideoTail(ctx, snd, log, owners, msg.From, chatID, msgID, videoPath,
		repostCaption(msg.From.Username, msg.From.FirstName, msg.Caption), "tiktok")
	switch out {
	case repostPermanent:
		sendDecline(ctx, snd, log, chatID, msgID, publicPureFailure(), "tiktok: decline note send failed")
		return
	case repostTransient:
		enqueueRepostOrFail(ctx, snd, log, queue, msg, storage.DeferredTikTok, tiktokURL)
		return
	}

	// Record the repost so a later comment quote can reply to this
	// message. Best-effort: a failed record only costs the reply link.
	if videos != nil {
		if vid := tiktokVideoID(tiktokURL); vid != "" {
			if rerr := videos.RecordVideo(ctx, chatID, vid, sent.GetMessageID()); rerr != nil {
				log.Warn("tiktok: recording repost index failed",
					"chat_id", chatID, "video_id", vid, "error", rerr)
			}
		}
	}
}

// tryTikTokExport attempts the full download->validate->upload->delete
// cycle. Returns nil on success (caller removes from queue), error on
// any failure (caller keeps in queue for next flush).
func tryTikTokExport(
	ctx context.Context,
	snd youtubeMediaSender,
	log *slog.Logger,
	owners ownerRecorder,
	videos tiktokVideoIndex,
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
		if errors.Is(err, errNoVideo) {
			// No retry can succeed on a photo post: report once and let
			// the caller drop the job instead of keeping it forever.
			log.Info("tiktok: photo post has no video, declining", "chat_id", chatID, "url", url)
			sendDecline(ctx, snd, log, chatID, msgID, publicPureFailure(), "tiktok: decline note send failed")
			return nil
		}
		return fmt.Errorf("download: %w", err)
	}
	defer os.Remove(videoPath)

	if !ffprobeHasAudio(videoPath) {
		return fmt.Errorf("no audio stream")
	}

	sent, out, tailErr := repostVideoTail(ctx, snd, log, owners, &telego.User{
		ID:        userID,
		Username:  username,
		FirstName: firstName,
	}, chatID, msgID, videoPath, repostCaption(username, firstName, caption), "tiktok flush")
	if out == repostPermanent && isAPIPermanent(tailErr) {
		// Telegram refused the upload: no retry can fix it. Report once and
		// let the caller drop the job instead of re-downloading it on every
		// /flush for the rest of the TTL.
		log.Info("tiktok flush: upload rejected by the API, dropping job",
			"chat_id", chatID, "url", url, "error", tailErr)
		sendDecline(ctx, snd, log, chatID, msgID, publicPureFailure(), "tiktok: decline note send failed")
		return nil
	}
	if tailErr != nil {
		return fmt.Errorf("send: %w", tailErr)
	}

	if videos != nil {
		// A queued short link (resolve failed at share time) yields no
		// video id from the path; resolve it now so the repost still
		// lands in the index.
		vid := tiktokVideoID(url)
		if vid == "" {
			if final := resolveTikTokURL(ctx, tiktokCommentHTTPClient, url); final != "" {
				vid = tiktokVideoID(final)
			}
		}
		if vid != "" {
			if rerr := videos.RecordVideo(ctx, chatID, vid, sent.GetMessageID()); rerr != nil {
				log.Warn("tiktok flush: recording repost index failed",
					"chat_id", chatID, "video_id", vid, "error", rerr)
			}
		}
	}

	return nil
}

// --- End of TikTok-specific code -----------------------------------------

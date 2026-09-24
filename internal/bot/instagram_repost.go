package bot

// Instagram reel/post repost with deferred-export queue.
//
// Same shape as the TikTok reposter (tiktok_repost.go): when a supergroup
// message carries an Instagram post permalink, the bot downloads the video
// as-is, reposts it attributed to the original sender (display name only,
// no @, no tg://user?id=), then deletes the original. On a download
// failure the job lands in the per-user deferred queue (/flush) instead of
// being abandoned - the original message stays as a fallback. A permalink
// that carries no video at all (photo post, image carousel, or a post
// Instagram refuses to describe to an anonymous client) is declined once
// and never queued: no retry can turn it into a video.
//
// Source: yt-dlp only. TikTok keeps the tikwm mirror first because the
// deployment ISP filters tiktok.com by TLS SNI (56_tiktok_repost.md);
// Instagram has no equivalent working public resolver - measured from the
// deployment egress 2026-09-24: kkinstagram/instagramez are ad landing
// pages, ddinstagram and instafix.app do not answer, cobalt requires an
// API key, and Instagram's own web/app endpoints answer an anonymous
// client with a JS login wall or 403. yt-dlp resolves the same public
// permalinks (4/5 sampled reels, formats: one muxed H.264+AAC mp4 plus
// video-only VP9 DASH renditions).
//
// No audio gate, unlike TikTok: Instagram's progressive rendition is a
// muxed avc1+mp4a file (verified on the downloaded artifact 2026-09-24),
// the DASH renditions are never picked, and a reel may legitimately be
// silent - so a missing audio track must not queue a job that can never
// change.
//
// Egress: several ISPs block Instagram outright, so the download honours
// two optional settings - an HTTP/SOCKS proxy and a Netscape cookie jar
// for posts that need a session (INSTAGRAM_PROXY / INSTAGRAM_COOKIES,
// 70_deployment.md). Both default to unset: direct, anonymous. With the
// ISP blocking Instagram and neither set, every link here fails into the
// deferred queue - the documented cost of the mirror-less source.
//
// Documented v1 gaps (mirroring the TikTok reposter):
//   - edited_message: an edit that introduces an Instagram link is not
//     re-processed.
//   - media groups: only the caption-bearing item is processed.
//   - carousels: a multi-item post is not assembled into an album; it has
//     no single video stream and is declined.
//   - text_link entities: detected, but the inline text is not rewritten
//     (same UTF-16 offset problem as the YT sanitizer).
//   - the Instagram author is not named in the caption (TikTok parity:
//     attribution is the Telegram sender only).

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/mymmrac/telego"
	"github.com/mymmrac/telego/telegoapi"
	th "github.com/mymmrac/telego/telegohandler"

	"github.com/veschin/bidlobot/internal/shared"
	"github.com/veschin/bidlobot/internal/storage"
)

// msgInstagramHeader is the attribution header for a reposted Instagram
// post. %s = sender display name (UserDisplay, no @, no tg://user?id=).
const msgInstagramHeader = "\U0001F464 <b>%s</b> \u043F\u0438\u0441\u0430\u043B(\u0430):"

const (
	// igDownloadAttempts is how many yt-dlp invocations one repost gets.
	igDownloadAttempts = 3

	// igDownloadAttemptTimeout caps one yt-dlp invocation. Three attempts
	// get three separate deadlines (same rationale as the TikTok path): one
	// shared deadline leaves the last attempt with whatever the first two
	// did not spend, usually nothing.
	igDownloadAttemptTimeout = 45 * time.Second
)

// InstagramDownloadOptions carries the optional egress knobs for the
// yt-dlp call. The zero value is production's default: a direct,
// anonymous request.
type InstagramDownloadOptions struct {
	// Proxy is passed to yt-dlp as --proxy (http(s)://, socks5:// or
	// socks5h:// - the last one resolves DNS through the proxy, which is
	// what a filtered egress needs). Empty = no proxy.
	Proxy string
	// Cookies is the path to a Netscape-format cookie jar passed to
	// yt-dlp as --cookies. Empty = anonymous.
	Cookies string
}

// errIGNoVideo marks a permalink that yields no video stream: a photo
// post, an image carousel, or a post Instagram will not describe to an
// anonymous client (deleted, private, login-walled). All of these are
// permanent for the current configuration, so callers decline instead of
// queueing a job no retry could complete.
var errIGNoVideo = errors.New("instagram: post has no downloadable video")

// igPermanentMarkers are the yt-dlp sentences that mean "no retry will
// produce a video". Measured 2026-09-24 against yt-dlp 2026.08.19:
//
//	"There is no video in this post"          - a photo post
//	"No video formats found!"                 - an image carousel
//	"Instagram sent an empty media response"  - deleted, private, or
//	                                            login-walled post
//
// Anything else (DNS failure, TLS reset, timeout, HTTP 5xx) is treated as
// transient and queued.
var igPermanentMarkers = []string{
	"There is no video in this post",
	"No video formats found!",
	"Instagram sent an empty media response",
}

// --- Host detection ------------------------------------------------------

// instagramHosts is the exact set of hosts that carry post permalinks.
// instagr.am is Instagram's legacy short domain and still redirects.
var instagramHosts = map[string]struct{}{
	"instagram.com": {},
	"instagr.am":    {},
}

// instagramURLRe finds Instagram URLs in text. Matches:
//
//	https://www.instagram.com/reel/C8bXvT1J9yQ/
//	https://instagram.com/p/C8bXvT1J9yQ/?igsh=abc
//	https://m.instagram.com/tv/C8bXvT1J9yQ/
//	instagram.com/reels/C8bXvT1J9yQ/ (scheme-less, edge case)
//
// Conservative: stops at whitespace and trailing punctuation.
var instagramURLRe = regexp.MustCompile(`(?i)\b((?:https?://)?(?:www\.|m\.)?(?:instagram\.com|instagr\.am)[/\S]*[^\s<>"')\]]*)`)

// igPostPathRe validates the path of an Instagram permalink and captures
// the kind and shortcode. reel/reels/tv are video permalinks; p is any
// post - the video check downstream decides what to do with it.
var igPostPathRe = regexp.MustCompile(`^/(reel|reels|tv|p)/([A-Za-z0-9_-]+)`)

// isInstagramHost lower-cases host, drops any port, strips a single
// leading "www." or "m." label, and checks the exact allowlist.
func isInstagramHost(host string) bool {
	host = strings.ToLower(host)
	if h, _, ok := strings.Cut(host, ":"); ok {
		host = h
	}
	for _, pfx := range []string{"www.", "m."} {
		if rest, ok := strings.CutPrefix(host, pfx); ok {
			host = rest
			break
		}
	}
	_, ok := instagramHosts[host]
	return ok
}

// instagramPostURL reports whether raw is an Instagram post permalink and
// returns the original string to hand to yt-dlp (scheme and query kept -
// the app's share links carry ?igsh=, which yt-dlp ignores).
func instagramPostURL(raw string) (string, bool) {
	if raw == "" {
		return "", false
	}
	u, err := url.Parse(ensureScheme(raw))
	if err != nil || !isInstagramHost(u.Host) {
		return "", false
	}
	if !igPostPathRe.MatchString(u.Path) {
		return "", false
	}
	return raw, true
}

// --- Decision gate (unit-testable) --------------------------------------

// instagramDecision is the pure gate: applies the exclusion set and
// returns the first Instagram post permalink found in the message
// text/caption. Returns act=false when the message must be passed
// through untouched.
func instagramDecision(msg *telego.Message) (act bool, postURL string) {
	if msg == nil {
		return false, ""
	}
	if msg.From == nil || msg.From.IsBot ||
		shared.IsAnonymousAdmin(msg.From.ID) || msg.SenderChat != nil {
		return false, ""
	}

	for _, entities := range [][]telego.MessageEntity{msg.Entities, msg.CaptionEntities} {
		for _, e := range entities {
			if (e.Type == "url" || e.Type == "text_link") && e.URL != "" {
				if u, ok := instagramPostURL(e.URL); ok {
					return true, u
				}
			}
		}
	}

	for _, m := range []string{msg.Text, msg.Caption} {
		for _, tok := range instagramURLRe.FindAllString(m, -1) {
			core := strings.TrimRight(tok, trailingPunct)
			if u, ok := instagramPostURL(core); ok {
				return true, u
			}
		}
	}

	return false, ""
}

// --- Video download ------------------------------------------------------

// igPermanentMarker returns the first permanent-failure marker found in
// captured yt-dlp output, or "" when the failure looks transient (DNS
// failure, TLS reset, timeout, HTTP 5xx) and is worth another attempt.
func igPermanentMarker(output []byte) string {
	for _, marker := range igPermanentMarkers {
		if bytes.Contains(output, []byte(marker)) {
			return marker
		}
	}
	return ""
}

// igYtDlpBinary is the downloader executable. A variable so tests can
// point the download at a stub script.
var igYtDlpBinary = "yt-dlp"

// igDownload is the download path; a variable so tests can substitute a
// stub (same seam as ytdlpDownload for TikTok).
var igDownload = downloadInstagram

// downloadInstagram fetches an Instagram video for repost via yt-dlp,
// retrying transient failures. A permanent marker (errIGNoVideo) returns
// immediately: the remaining attempts cannot change the answer.
// Returns the file path (caller must os.Remove when done).
func downloadInstagram(ctx context.Context, opts InstagramDownloadOptions, rawURL, workDir string) (string, error) {
	var lastErr error
	for attempt := 1; attempt <= igDownloadAttempts; attempt++ {
		path, err := igYtDlpAttempt(ctx, opts, rawURL, workDir)
		if err == nil {
			return path, nil
		}
		if errors.Is(err, errIGNoVideo) {
			return "", err
		}
		lastErr = fmt.Errorf("yt-dlp attempt %d: %w", attempt, err)
		if attempt < igDownloadAttempts {
			time.Sleep(2 * time.Second)
		}
	}
	return "", lastErr
}

// igYtDlpAttempt runs one yt-dlp invocation and returns the file it wrote
// into workDir. The permalink is normalised here so a scheme-less link
// pasted from a chat never reaches the downloader bare.
func igYtDlpAttempt(ctx context.Context, opts InstagramDownloadOptions, rawURL, workDir string) (string, error) {
	dlCtx, cancel := context.WithTimeout(ctx, igDownloadAttemptTimeout)
	defer cancel()

	dlURL := ensureScheme(rawURL)

	args := []string{
		// "b" with a progressive-mp4 preference: Instagram serves the
		// muxed H.264+AAC rendition as an mp4, while its DASH renditions
		// are video-only (VP9) and would arrive silent.
		"-f", "b[ext=mp4]/b",
		"--no-playlist",
	}
	if opts.Proxy != "" {
		args = append(args, "--proxy", opts.Proxy)
	}
	if opts.Cookies != "" {
		args = append(args, "--cookies", opts.Cookies)
	}
	args = append(args, "-o", workDir+"/video.%(ext)s", dlURL)

	cmd := exec.CommandContext(dlCtx, igYtDlpBinary, args...)

	output, err := cmd.CombinedOutput()
	if err != nil {
		if marker := igPermanentMarker(output); marker != "" {
			return "", fmt.Errorf("%w: %s", errIGNoVideo, marker)
		}
		return "", fmt.Errorf("%w\n%s", err, previewOutput(output, 400))
	}
	return fileFromWorkDir(workDir)
}

// --- Middleware ----------------------------------------------------------

// instagramReposter is the supergroup middleware. It mirrors
// tiktokReposter structurally: the heavy download+upload runs
// asynchronously through shared.Go so the sequential update loop is never
// stalled, and context.Background() is mandatory - the per-update ctx is
// cancelled when the handler returns.
func instagramReposter(a *App) th.Handler {
	return func(thctx *th.Context, update telego.Update) error {
		msg := update.Message
		if msg == nil {
			return thctx.Next(update)
		}
		act, postURL := instagramDecision(msg)
		if !act {
			return thctx.Next(update)
		}
		shared.Go(a.log, "instagram", func() {
			processInstagram(context.Background(), a.sanitizerSender(), a.log,
				a.deferredQ, a.repReactor, a.igOptions, msg, postURL, "")
		})
		return thctx.Next(update)
	}
}

// --- Pipeline ------------------------------------------------------------

// processInstagram runs the full pipeline: download (if videoPath is ""),
// size-check, upload, delete-original.
//
// On a transient download failure the job is enqueued for later flush (if
// a queue is wired) instead of being abandoned. A permalink that carries
// no video is declined once and never queued. The original message is
// always left intact until a successful repost.
//
// videoPath is "" in production (download via yt-dlp), or a pre-created
// temp file path in tests (bypasses yt-dlp).
//
// Package-level (not a method) so tests can call it without an App. The
// goroutine is NOT tracked in App.inFlight: like the TikTok repost, this
// is best-effort and a shutdown may lose one in-flight repost.
func processInstagram(
	ctx context.Context,
	snd youtubeMediaSender,
	log *slog.Logger,
	queue DeferredQueuer,
	owners ownerRecorder,
	opts InstagramDownloadOptions,
	msg *telego.Message,
	postURL string,
	videoPath string,
) {
	chatID := msg.Chat.ID
	msgID := msg.GetMessageID()

	workDir, err := os.MkdirTemp("", "bidlobot-instagram-")
	if err != nil {
		log.Error("instagram: creating temp dir", "chat_id", chatID, "error", err)
		return
	}
	defer os.RemoveAll(workDir)

	// Step 1: Download.
	if videoPath == "" {
		var dlErr error
		videoPath, dlErr = igDownload(ctx, opts, postURL, workDir)
		if dlErr != nil {
			if errors.Is(dlErr, errIGNoVideo) {
				log.Info("instagram: post has no video, declining",
					"chat_id", chatID, "url", postURL, "error", dlErr)
				sendDecline(ctx, snd, log, chatID, msgID, publicPureFailure(),
					"instagram: decline note send failed")
				return
			}
			log.Warn("instagram: download failed, queuing",
				"chat_id", chatID, "url", postURL, "error", dlErr)
			enqueueInstagramOrFail(ctx, snd, log, queue, msg, postURL)
			return
		}
	}
	defer os.Remove(videoPath)

	// Step 2: Size check - Telegram's bot upload cap.
	fi, err := os.Stat(videoPath)
	if err != nil {
		log.Error("instagram: stat video", "chat_id", chatID, "path", videoPath, "error", err)
		sendDecline(ctx, snd, log, chatID, msgID, publicPureFailure(), "instagram: decline note send failed")
		return
	}
	if fi.Size() > maxVideoSize {
		log.Info("instagram: video too large", "chat_id", chatID, "size", fi.Size())
		sendDecline(ctx, snd, log, chatID, msgID, publicPureFailure(), "instagram: decline note send failed")
		return
	}

	// Step 3: Open for upload.
	file, err := os.Open(videoPath)
	if err != nil {
		log.Error("instagram: opening video for upload", "chat_id", chatID, "error", err)
		sendDecline(ctx, snd, log, chatID, msgID, publicPureFailure(), "instagram: decline note send failed")
		return
	}
	defer file.Close()

	// Step 4: Repost (first, before delete - repost-first contract).
	sent, sendErr := snd.SendVideo(ctx, &telego.SendVideoParams{
		ChatID:    telego.ChatID{ID: chatID},
		Video:     telego.InputFile{File: file},
		Caption:   instagramCaption(msg.From.Username, msg.From.FirstName, msg.Caption),
		ParseMode: telego.ModeHTML,
	})
	if sendErr != nil {
		// Permanent = Telegram answered 4xx (bad file, chat forbidden, too
		// large): the decline note stands. Everything else is transient and
		// replayable - a transport fault never reached Telegram, 429 and 5xx
		// outlived the retry ladder - so the job goes to the deferred queue.
		var apiErr *telegoapi.Error
		if errors.As(sendErr, &apiErr) && apiErr.ErrorCode < 500 && apiErr.ErrorCode != 429 {
			log.Warn("instagram: repost rejected by the API; leaving original intact",
				"chat_id", chatID, "error", sendErr)
			sendDecline(ctx, snd, log, chatID, msgID, publicPureFailure(), "instagram: decline note send failed")
			return
		}
		log.Warn("instagram: repost failed transiently, queuing",
			"chat_id", chatID, "url", postURL, "error", sendErr)
		enqueueInstagramOrFail(ctx, snd, log, queue, msg, postURL)
		return
	}

	if owners != nil {
		owners.RecordOwner(chatID, sent.GetMessageID(), msg.From)
	}

	// Step 5: Delete original (only after successful repost).
	if delErr := snd.DeleteMessage(ctx, &telego.DeleteMessageParams{
		ChatID:    telego.ChatID{ID: chatID},
		MessageID: msgID,
	}); delErr != nil {
		log.Info("instagram: reposted but delete failed; original kept",
			"chat_id", chatID, "message_id", msgID, "error", delErr)
	}

	log.Info("instagram: reposted", "chat_id", chatID, "message_id", msgID)
}

// instagramCaption builds the HTML caption for a reposted Instagram post:
// attribution header (display name only) plus the original caption if any.
func instagramCaption(username, firstName, rawCaption string) string {
	display := shared.UserDisplay(username, firstName)
	caption := strings.Replace(msgInstagramHeader, "%s", display, 1)
	if rawCaption != "" {
		caption += "\n" + html.EscapeString(rawCaption)
	}
	return caption
}

// enqueueInstagramOrFail enqueues a deferred Instagram job for the calling
// user if a queue is wired. If the queue is nil or the enqueue fails, it
// falls back to a public decline reply so the user is not left in silence.
func enqueueInstagramOrFail(
	ctx context.Context,
	snd youtubeMediaSender,
	log *slog.Logger,
	queue DeferredQueuer,
	msg *telego.Message,
	postURL string,
) {
	if queue != nil {
		payload, _ := json.Marshal(storage.InstagramPayload{
			URL:       postURL,
			Username:  msg.From.Username,
			FirstName: msg.From.FirstName,
			Caption:   msg.Caption,
		})
		job := storage.DeferredJob{
			UserID:    msg.From.ID,
			Type:      storage.DeferredInstagram,
			ChatID:    msg.Chat.ID,
			MessageID: msg.GetMessageID(),
			Payload:   payload,
			CreatedAt: time.Now().UTC(),
		}
		if err := queue.Enqueue(ctx, job); err != nil {
			log.Error("instagram: enqueue failed, falling back to decline",
				"chat_id", msg.Chat.ID, "error", err)
		} else {
			log.Info("instagram: queued for later export",
				"chat_id", msg.Chat.ID, "url", postURL)
			return
		}
	}
	sendDecline(ctx, snd, log, msg.Chat.ID, msg.GetMessageID(),
		publicPureFailure(), "instagram: decline note send failed")
}

// tryInstagramExport attempts the full download->validate->upload->delete
// cycle for a queued job. Returns nil on success (caller removes it from
// the queue), error on a retryable failure (caller keeps it).
func tryInstagramExport(
	ctx context.Context,
	snd youtubeMediaSender,
	log *slog.Logger,
	owners ownerRecorder,
	opts InstagramDownloadOptions,
	chatID int64,
	msgID int,
	userID int64,
	url, username, firstName, caption string,
) error {
	workDir, err := os.MkdirTemp("", "bidlobot-instagram-")
	if err != nil {
		return fmt.Errorf("temp dir: %w", err)
	}
	defer os.RemoveAll(workDir)

	videoPath, err := igDownload(ctx, opts, url, workDir)
	if err != nil {
		if errors.Is(err, errIGNoVideo) {
			// No retry can turn this into a video: report once, then let
			// the caller drop the job instead of keeping it for 48h.
			log.Info("instagram flush: post has no video, declining",
				"chat_id", chatID, "url", url, "error", err)
			sendDecline(ctx, snd, log, chatID, msgID, publicPureFailure(),
				"instagram: decline note send failed")
			return nil
		}
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

	file, err := os.Open(videoPath)
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	defer file.Close()

	sent, sendErr := snd.SendVideo(ctx, &telego.SendVideoParams{
		ChatID:    telego.ChatID{ID: chatID},
		Video:     telego.InputFile{File: file},
		Caption:   instagramCaption(username, firstName, caption),
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
		log.Info("instagram flush: reposted but delete failed; original kept",
			"chat_id", chatID, "message_id", msgID, "error", delErr)
	}

	log.Info("instagram flush: reposted", "chat_id", chatID, "message_id", msgID, "url", url)
	return nil
}

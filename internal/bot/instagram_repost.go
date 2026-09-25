package bot

// Instagram reel/post repost. The shared pipeline lives in
// repost_common.go; this file holds only what is Instagram-specific: link
// detection, the downloader (format selector, optional egress, yt-dlp
// failure table) and the two entry points (live message, /flush retry).
//
// No mirror, unlike TikTok: TikTok tries the tikwm mirror first because
// the deployment ISP filters tiktok.com by TLS SNI; measured 2026-09-24 no
// public Instagram resolver works (kkinstagram and instagramez redirect to
// ad landing pages, ddinstagram and instafix.app do not answer, cobalt
// wants an API key, Instagram's own web and app endpoints answer an
// anonymous client with a JS shell or 403). yt-dlp is the only source.
//
// No audio gate either: Instagram's progressive rendition is a muxed
// H.264+AAC mp4 and a reel may legitimately be silent, so a missing audio
// track must not queue a job that can never change.
//
// Egress: Instagram is blocked outright on some ISPs and many posts answer
// an anonymous client with a login wall, so the download accepts an
// optional proxy and a Netscape-format cookie jar (INSTAGRAM_PROXY /
// INSTAGRAM_COOKIES). Both unset - the default - keep the request direct
// and anonymous; a blocked egress then only means every link lands in the
// deferred queue.
//
// Documented gaps (same family as the TikTok reposter): edited messages
// are not re-processed, media groups lose their non-caption items,
// carousels are declined rather than assembled, and a text_link entity is
// used for the download but its inline text is left alone.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"regexp"
	"strings"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"

	"github.com/veschin/bidlobot/internal/shared"
	"github.com/veschin/bidlobot/internal/storage"
)

// InstagramDownloadOptions carries the optional egress knobs for the
// yt-dlp call. The zero value is the default: a direct, anonymous request.
type InstagramDownloadOptions struct {
	// Proxy is passed to yt-dlp as --proxy (http(s)://, socks4, socks5 or
	// socks5h - the last one resolves DNS through the proxy, which is
	// what a filtered egress needs). Empty = no proxy.
	Proxy string
	// Cookies is the path to a Netscape-format cookie jar passed to
	// yt-dlp as --cookies, for posts that require a session. Empty =
	// anonymous.
	Cookies string
}

// igPermanentMarkers are the yt-dlp sentences that mean "no retry will
// produce a video": a photo post, an image carousel, or a post Instagram
// will not describe to an anonymous client (deleted, private, login
// walled). Measured 2026-09-24 against yt-dlp 2026.08.19; all three are
// still present in 2026.07.04, the version the image pins (two in the
// Instagram extractor, "No video formats found!" in YoutubeDL.py). Anything
// else (DNS failure, TLS reset, timeout, HTTP 5xx, rate limit) stays
// retryable and goes to the deferred queue.
var igPermanentMarkers = []string{
	"There is no video in this post",
	"No video formats found!",
	"Instagram sent an empty media response",
}

// --- Instagram link detection --------------------------------------------

// instagramHosts is the exact set of hosts that carry post permalinks.
// instagr.am is Instagram's legacy short domain and still redirects.
var instagramHosts = map[string]struct{}{
	"instagram.com": {},
	"instagr.am":    {},
}

// instagramURLRe finds Instagram links in text. Same conservative shape as
// tiktokURLRe: a word boundary, an optional scheme and subdomain, then the
// rest of the token (trailing sentence punctuation is trimmed by
// igPostURL's caller).
var instagramURLRe = regexp.MustCompile(`(?i)\b((?:https?://)?(?:www\.|m\.)?(?:instagram\.com|instagr\.am)[/\S]*[^\s<>"')\]]*)`)

// igPostPathRe validates the path of an Instagram permalink and captures
// the kind and shortcode. reel/reels/tv are video permalinks; p is any
// post - the download decides what it holds. A profile, /stories/... or
// /explore/... path never matches.
var igPostPathRe = regexp.MustCompile(`^/(reel|reels|tv|p)/([A-Za-z0-9_-]+)`)

// isInstagramHost normalizes host and checks the exact allowlist.
func isInstagramHost(host string) bool {
	return hostAllowed(host, []string{"www.", "m."}, instagramHosts)
}

// instagramPostURL reports whether raw is an Instagram post permalink and
// returns the link to hand to yt-dlp. The host is normalised to
// https://www.instagram.com: the extractor's _VALID_URL accepts only that
// host (neither m. nor the instagr.am legacy domain), and a link it does not
// match falls through to the generic extractor, which spends the whole retry
// ladder before failing into the queue. Measured 2026-09-25 on yt-dlp
// 2026.08.19: instagr.am/p/<code> reached [generic] and failed on the
// network, m.instagram.com/reel/<code> reached [generic] first and delegated
// to [Instagram] only after fetching the page. The query is kept - the app's
// share links carry ?igsh=, which yt-dlp ignores.
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
	normalized := "https://www.instagram.com" + u.EscapedPath()
	if u.RawQuery != "" {
		normalized += "?" + u.RawQuery
	}
	return normalized, true
}

// instagramDecision is the pure gate: applies the shared sender predicate
// and returns the first Instagram post permalink found in the message text
// or caption. Returns act=false when the message must be passed through
// untouched.
func instagramDecision(msg *telego.Message) (act bool, postURL string) {
	if !repostableSender(msg) {
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

// --- Instagram downloader ------------------------------------------------

// igPermanentMarker returns the first permanent-failure marker found in
// captured yt-dlp output, or "" when the failure looks transient.
func igPermanentMarker(output []byte) string {
	for _, marker := range igPermanentMarkers {
		if bytes.Contains(output, []byte(marker)) {
			return marker
		}
	}
	return ""
}

// igDownload is the download path; a variable so tests can substitute a
// stub (same seam as ytdlpDownload for TikTok).
var igDownload = downloadInstagram

// downloadInstagram fetches an Instagram video for repost through the
// shared yt-dlp ladder.
func downloadInstagram(ctx context.Context, opts InstagramDownloadOptions, rawURL, workDir string) (string, error) {
	args := []string{
		// "b" with a progressive-mp4 preference: Instagram serves the
		// muxed H.264+AAC rendition as an mp4, while its DASH renditions
		// are video-only (VP9) and would arrive silent.
		"-f", "b[ext=mp4]/b",
	}
	if opts.Proxy != "" {
		args = append(args, "--proxy", opts.Proxy)
	}
	if opts.Cookies != "" {
		args = append(args, "--cookies", opts.Cookies)
	}
	return ytDlpRetry(ctx, rawURL, workDir, ytDlpRun{
		args: args,
		classify: func(output []byte) error {
			if marker := igPermanentMarker(output); marker != "" {
				return fmt.Errorf("%w: %s", errNoVideo, marker)
			}
			return nil
		},
	})
}

// --- Middleware ----------------------------------------------------------

// igSlot serializes Instagram downloads behind one slot, the same shape as
// xpostSlot: one download is up to three yt-dlp runs of 45s and the
// container holds 256 MiB and half a CPU.
// ponytail: one global slot; per-chat slots only if a busy chat starves others.
var igSlot = make(chan struct{}, 1)

// instagramReposter is the supergroup middleware. It mirrors tiktokReposter
// structurally: the heavy download+upload runs asynchronously so the
// sequential update loop is never stalled, and context.Background() is
// mandatory - the per-update ctx is cancelled when the handler returns.
func instagramReposter(a *App) th.Handler {
	return func(thctx *th.Context, update telego.Update) error {
		if msg := update.Message; msg != nil {
			a.dispatchInstagram(msg)
		}
		return thctx.Next(update)
	}
}

// dispatchInstagram runs the detector and, on a hit, either starts the
// download in the single slot or queues the link when a download is already
// running. Both branches leave the update loop through shared.Go.
func (a *App) dispatchInstagram(msg *telego.Message) {
	act, postURL := instagramDecision(msg)
	if !act {
		return
	}
	snd := a.sanitizerSender()
	select {
	case igSlot <- struct{}{}:
		shared.Go(a.log, "instagram", func() {
			defer func() { <-igSlot }()
			processInstagram(context.Background(), snd, a.log, a.deferredQ, a.repReactor,
				a.igOptions, msg, postURL, "")
		})
	default:
		// Another download is running: keep the link in the queue
		// instead of dropping it.
		shared.Go(a.log, "instagram-defer", func() {
			enqueueRepostOrFail(context.Background(), snd, a.log, a.deferredQ, msg,
				storage.DeferredInstagram, postURL)
		})
	}
}

// --- Pipeline ------------------------------------------------------------

// processInstagram runs the full pipeline: download (if videoPath is ""),
// size-check, upload, delete-original. Same contract as processTikTok,
// minus the audio gate and the repost index.
//
// videoPath is "" in production (download via yt-dlp), or a pre-created
// temp file path in tests (bypasses yt-dlp).
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
			if errors.Is(dlErr, errNoVideo) {
				log.Info("instagram: post has no video, declining", "chat_id", chatID, "url", postURL)
				sendDecline(ctx, snd, log, chatID, msgID, publicPureFailure(), "instagram: decline note send failed")
				return
			}
			log.Warn("instagram: download failed, queuing", "chat_id", chatID, "url", postURL, "error", dlErr)
			enqueueRepostOrFail(ctx, snd, log, queue, msg, storage.DeferredInstagram, postURL)
			return
		}
	}
	defer os.Remove(videoPath)

	// Step 2: Repost (upload first, delete after).
	_, out, _ := repostVideoTail(ctx, snd, log, owners, msg.From, chatID, msgID, videoPath,
		repostCaption(msg.From.Username, msg.From.FirstName, msg.Caption), "instagram")
	switch out {
	case repostPermanent:
		sendDecline(ctx, snd, log, chatID, msgID, publicPureFailure(), "instagram: decline note send failed")
	case repostTransient:
		enqueueRepostOrFail(ctx, snd, log, queue, msg, storage.DeferredInstagram, postURL)
	}
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
		if errors.Is(err, errNoVideo) {
			// No retry can turn this into a video: report once and let the
			// caller drop the job instead of keeping it for 48h.
			log.Info("instagram flush: post has no video, declining", "chat_id", chatID, "url", url)
			sendDecline(ctx, snd, log, chatID, msgID, publicPureFailure(), "instagram: decline note send failed")
			return nil
		}
		return fmt.Errorf("download: %w", err)
	}
	defer os.Remove(videoPath)

	_, out, tailErr := repostVideoTail(ctx, snd, log, owners, &telego.User{
		ID:        userID,
		Username:  username,
		FirstName: firstName,
	}, chatID, msgID, videoPath, repostCaption(username, firstName, caption), "instagram flush")
	if out == repostPermanent && isAPIPermanent(tailErr) {
		// Telegram refused the upload: no retry can fix it. Report once and
		// let the caller drop the job instead of re-downloading it on every
		// /flush for the rest of the TTL.
		log.Info("instagram flush: upload rejected by the API, dropping job",
			"chat_id", chatID, "url", url, "error", tailErr)
		sendDecline(ctx, snd, log, chatID, msgID, publicPureFailure(), "instagram: decline note send failed")
		return nil
	}
	if tailErr != nil {
		return fmt.Errorf("send: %w", tailErr)
	}
	return nil
}

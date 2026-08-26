package bot

// TikTok comment quote ("Парсинг ссылки на коммент", issue #1).
//
// Two link shapes carry a comment reference:
//
//	https://www.tiktok.com/@user/video/123?comment_id=456        (web timestamp permalink)
//	https://vt.tiktok.com/ZSxxxx/  ->  .../video/123?share_comment_id=456  (app share button)
//
// The app's share button on a comment produces a SHORT link whose
// redirect target carries share_comment_id; the middleware resolves
// short links before dispatching, so both shapes reach the quote
// pipeline. App share links often point at a REPLY, so the scan covers
// reply threads too (comments with reply_total > 0, newest-first).
//
// When a supergroup message carries such a link, the bot fetches the
// comment (author, text, attached images) and posts a quote attributed to
// the commenter, then deletes the original message (repost-first contract,
// same as the video replayer):
//
//	👤 <b>@username</b> писал:
//	текст комментария
//
// Source. TikTok exposes no unsigned API for comments (verified live):
// www.tiktok.com/api/comment/list answers HTTP 200 with an empty body
// without a browser-generated msToken/X-Bogus signature, the mobile app
// API answers 2146 "Please upgrade your TikTok app", and yt-dlp has no
// TikTok comment extractor. The fetch therefore goes through the public
// tikwm.com mirror API - the same cookie-free fallback the maintained
// TikTokDownloader project uses. tikwm's free tier is rate-limited to
// roughly one request per second, so every call goes through a global
// pacer.
//
// v1 gaps (documented deliberately):
//   - the scan is capped (tiktokCommentMaxRequests overall, newest
// first): on heavily commented videos a buried or old comment may fall
// outside the window and decline.
//   - fetch failures are NOT queued for /flush (unlike video downloads):
//     the public decline reply is the whole retry story.
//   - animated images (GIF stickers) in a MULTI-image comment are sent as
//     static photos (Telegram albums cannot carry animations); a
//     single-image comment keeps the animation.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mymmrac/telego"

	"github.com/veschin/bidlobot/internal/shared"
)

const (
	// msgTikTokCommentHeader is the quote header for a reposted TikTok
	// comment. %s = the commenter's @handle (issue #1 format: "{username}
	// писал:"). No "(а)": the commenter's gender is unknown.
	msgTikTokCommentHeader = "\U0001F464 <b>@%s</b> \u043F\u0438\u0441\u0430\u043B:"

	// tiktokCommentMaxPages caps the newest-first top-level scan while
	// locating the comment (20 comments per page).
	tiktokCommentMaxPages = 10

	// tiktokCommentMaxRequests is the overall request budget for one
	// comment scan (top-level pages + reply threads); 40 x 1.5s bounds
	// the worst case at one minute.
	tiktokCommentMaxRequests = 40

	// tiktokCommentMaxReplyPages caps reply pagination per thread.
	tiktokCommentMaxReplyPages = 3

	// tiktokCommentPageCount is the tikwm page size.
	tiktokCommentPageCount = 20

	// tiktokCommentMediaCap bounds one comment image download; comment
	// stickers are small, and Telegram's photo limit is 10 MB.
	tiktokCommentMediaCap = 10 * 1024 * 1024
)

// tikwmAPIHost is the tikwm API root; a variable so tests can point the
// fetcher at a stub server. tikwmPathList serves the top-level comments
// of a video (url param), tikwmPathReply serves one comment's replies
// (video_id + comment_id params).
var tikwmAPIHost = "https://www.tikwm.com"

const (
	tikwmPathList  = "/api/comment/list"
	tikwmPathReply = "/api/comment/reply"
)

// advertises 1 request/second; 1.5s keeps a safe margin - the limiter
// rejects 1.1s gaps). A variable so tests can shrink it.
var tikwmMinInterval = 1500 * time.Millisecond

// tiktokCommentHTTPClient is the shared client for tikwm and image-CDN
// requests.
var tiktokCommentHTTPClient = &http.Client{Timeout: 30 * time.Second}

// errTikTokCommentNotFound reports that the comment_id was not present in
// the scanned pages (deep-buried comment or a reply permalink).
var errTikTokCommentNotFound = errors.New("tiktok: comment not found in scanned pages")

// --- tikwm pacer ---------------------------------------------------------

var (
	tikwmMu   sync.Mutex
	tikwmLast time.Time
)

// tikwmPace serializes and spaces tikwm requests so the bot stays inside
// the free-tier rate limit even with concurrent comment quotes.
func tikwmPace() {
	tikwmMu.Lock()
	defer tikwmMu.Unlock()
	if wait := tikwmLast.Add(tikwmMinInterval).Sub(time.Now()); wait > 0 {
		time.Sleep(wait)
	}
	tikwmLast = time.Now()
}

// --- Decision gate (unit-testable) ---------------------------------------

// tiktokCommentDecision is the pure gate: applies the same exclusions as
// tiktokDecision and returns the first TikTok video permalink that
// carries a comment reference. Canonical links from the web carry
// comment_id; app share links carry share_comment_id. Short links
// (vm./vt.) hide both behind a redirect - the middleware resolves them
// before dispatching, this gate only sees raw text. act=false when the
// message must fall through to the video replayer (or pass untouched).
func tiktokCommentDecision(msg *telego.Message) (act bool, videoURL, commentID string) {
	if msg == nil {
		return false, "", ""
	}
	if msg.From == nil || msg.From.IsBot ||
		shared.IsAnonymousAdmin(msg.From.ID) || msg.SenderChat != nil {
		return false, "", ""
	}

	var candidates []string
	for _, e := range msg.Entities {
		if (e.Type == "url" || e.Type == "text_link") && e.URL != "" {
			candidates = append(candidates, e.URL)
		}
	}
	for _, e := range msg.CaptionEntities {
		if (e.Type == "url" || e.Type == "text_link") && e.URL != "" {
			candidates = append(candidates, e.URL)
		}
	}
	for _, m := range []string{msg.Text, msg.Caption} {
		for _, tok := range tiktokURLRe.FindAllString(m, -1) {
			candidates = append(candidates, strings.TrimRight(tok, trailingPunct))
		}
	}

	for _, raw := range candidates {
		if vid, cid, ok := tiktokCommentIDFromURL(raw); ok {
			return true, vid, cid
		}
	}
	return false, "", ""
}

// tiktokCommentIDFromURL extracts the comment reference from one TikTok
// URL. Both param spellings are accepted: comment_id (web timestamp
// permalink) and share_comment_id (app share button). Returns the
// canonical video URL (scheme://host/path, tracking query stripped - it
// is what tikwm expects) and the comment id.
func tiktokCommentIDFromURL(raw string) (videoURL, commentID string, ok bool) {
	u, err := url.Parse(ensureScheme(raw))
	if err != nil || !isTikTokHost(u.Host) {
		return "", "", false
	}
	if !strings.Contains(u.Path, "/video/") {
		return "", "", false
	}
	cid := u.Query().Get("comment_id")
	if cid == "" {
		cid = u.Query().Get("share_comment_id")
	}
	if cid == "" {
		return "", "", false
	}
	return u.Scheme + "://" + u.Host + u.Path, cid, true
}

// isTikTokShortLink reports a vm./vt. redirect link: it may hide a
// comment permalink, so the middleware resolves it before dispatch.
func isTikTokShortLink(raw string) bool {
	u, err := url.Parse(ensureScheme(raw))
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Host)
	if h, _, ok := strings.Cut(host, ":"); ok {
		host = h
	}
	return host == "vm.tiktok.com" || host == "vt.tiktok.com"
}

// resolveTikTokURL follows the redirect chain of a TikTok link and
// returns the final URL ("" on any error).
func resolveTikTokURL(ctx context.Context, client *http.Client, raw string) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ensureScheme(raw), nil)
	if err != nil {
		return ""
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/138.0.0.0 Safari/537.36")
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	return resp.Request.URL.String()
}

// tiktokVideoID extracts the numeric video id from a canonical video
// URL path ("" when absent).
func tiktokVideoID(videoURL string) string {
	u, err := url.Parse(ensureScheme(videoURL))
	if err != nil {
		return ""
	}
	_, id, ok := strings.Cut(u.Path, "/video/")
	if !ok {
		return ""
	}
	if rest, _, ok := strings.Cut(id, "/"); ok {
		id = rest
	}
	return id
}

// --- Fetch ----------------------------------------------------------------

// tikwmCommentUser is the author subset the quote needs.
type tikwmCommentUser struct {
	UniqueID string `json:"unique_id"`
	Nickname string `json:"nickname"`
}

// tikwmComment is one tikwm comment row. Images is a plain list of signed
// CDN URLs (comment stickers/photos, including GIFs).
type tikwmComment struct {
	ID         string           `json:"id"`
	Text       string           `json:"text"`
	User       tikwmCommentUser `json:"user"`
	Images     []string         `json:"images"`
	ReplyTotal int              `json:"reply_total"`
}

// tikwmCommentPage is one comment-list response. code 0 = success.
type tikwmCommentPage struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		Comments []tikwmComment `json:"comments"`
		Cursor   int            `json:"cursor"`
		HasMore  bool           `json:"hasMore"`
	} `json:"data"`
}

// tikwmGet performs one paced tikwm GET and decodes the page. budget
// counts the remaining requests across the whole scan (top-level pages
// plus reply threads); -1 on success means "budget exhausted".
func tikwmGet(ctx context.Context, client *http.Client, budget *int, path string, q url.Values) (tikwmCommentPage, error) {
	tikwmPace()
	*budget--

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, tikwmAPIHost+path+"?"+q.Encode(), nil)
	if err != nil {
		return tikwmCommentPage{}, fmt.Errorf("building tikwm request: %w", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/138.0.0.0 Safari/537.36")

	resp, err := client.Do(req)
	if err != nil {
		return tikwmCommentPage{}, fmt.Errorf("tikwm request: %w", err)
	}
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return tikwmCommentPage{}, fmt.Errorf("tikwm status %d", resp.StatusCode)
	}
	if readErr != nil {
		return tikwmCommentPage{}, fmt.Errorf("reading tikwm body: %w", readErr)
	}

	var parsed tikwmCommentPage
	if err := json.Unmarshal(body, &parsed); err != nil {
		return tikwmCommentPage{}, fmt.Errorf("decoding tikwm response: %w", err)
	}
	if parsed.Code != 0 {
		return tikwmCommentPage{}, fmt.Errorf("tikwm error %d: %s", parsed.Code, parsed.Msg)
	}
	return parsed, nil
}

// fetchTikTokComment locates one comment by id: first the top-level list
// (newest-first), then the reply threads of comments that have replies -
// app share links often point at a reply. The returned cursor (not page
// math) drives each loop: tikwm's cursor does not always advance by
// count, and a non-advancing cursor stops the scan. The overall request
// budget bounds the worst-case wall time (1.5s per request).
func fetchTikTokComment(ctx context.Context, client *http.Client, videoURL, commentID string) (tikwmComment, error) {
	budget := tiktokCommentMaxRequests

	// Phase 1: top-level pages.
	var threads []tikwmComment
	cursor := 0
	for range tiktokCommentMaxPages {
		if budget <= 0 {
			return tikwmComment{}, errTikTokCommentNotFound
		}
		parsed, err := tikwmGet(ctx, client, &budget, tikwmPathList, url.Values{
			"url":    {videoURL},
			"count":  {strconv.Itoa(tiktokCommentPageCount)},
			"cursor": {strconv.Itoa(cursor)},
		})
		if err != nil {
			return tikwmComment{}, err
		}
		for _, c := range parsed.Data.Comments {
			if c.ID == commentID {
				return c, nil
			}
			if c.ReplyTotal > 0 {
				threads = append(threads, c)
			}
		}
		if !parsed.Data.HasMore || parsed.Data.Cursor <= cursor {
			break
		}
		cursor = parsed.Data.Cursor
	}

	// Phase 2: reply threads, newest threads first.
	videoID := tiktokVideoID(videoURL)
	if videoID == "" {
		return tikwmComment{}, errTikTokCommentNotFound
	}
	for _, parent := range threads {
		cursor := 0
		for range tiktokCommentMaxReplyPages {
			if budget <= 0 {
				return tikwmComment{}, errTikTokCommentNotFound
			}
			parsed, err := tikwmGet(ctx, client, &budget, tikwmPathReply, url.Values{
				"video_id":   {videoID},
				"comment_id": {parent.ID},
				"count":      {strconv.Itoa(tiktokCommentPageCount)},
				"cursor":     {strconv.Itoa(cursor)},
			})
			if err != nil {
				return tikwmComment{}, err
			}
			for _, c := range parsed.Data.Comments {
				if c.ID == commentID {
					return c, nil
				}
			}
			if !parsed.Data.HasMore || parsed.Data.Cursor <= cursor {
				break
			}
			cursor = parsed.Data.Cursor
		}
	}
	return tikwmComment{}, errTikTokCommentNotFound
}

// tiktokCommentCaption builds the HTML caption: commenter header plus the
// comment text (issue #1 format). An image-only comment keeps just the
// header.
func tiktokCommentCaption(handle, text string) string {
	caption := fmt.Sprintf(msgTikTokCommentHeader, html.EscapeString(handle))
	if text != "" {
		caption += "\n" + html.EscapeString(text)
	}
	return caption
}

// commentHandle picks the display handle: the @unique_id, falling back to
// the display nickname for accounts without a handle.
func commentHandle(c tikwmComment) string {
	if c.User.UniqueID != "" {
		return c.User.UniqueID
	}
	return c.User.Nickname
}

// --- Delivery -------------------------------------------------------------

// sendTikTokComment delivers the quote as exactly one message: text, a
// single image (URL first, download fallback), or a photo album. Returns
// the sent message IDs for owner attribution.
func sendTikTokComment(
	ctx context.Context,
	snd youtubeMediaSender,
	client *http.Client,
	workDir string,
	chatID int64,
	caption string,
	images []string,
) ([]int, error) {
	switch {
	case len(images) == 0:
		m, err := snd.SendMessage(ctx, &telego.SendMessageParams{
			ChatID:    telego.ChatID{ID: chatID},
			Text:      caption,
			ParseMode: telego.ModeHTML,
		})
		if err != nil {
			return nil, err
		}
		return []int{m.GetMessageID()}, nil

	case len(images) == 1:
		ids, err := sendCommentSingleImage(ctx, snd, chatID, caption, images[0])
		if err == nil {
			return ids, nil
		}
		ids, ferr := sendCommentLocalImage(ctx, snd, client, workDir, chatID, caption, images[0])
		if ferr != nil {
			return nil, err
		}
		return ids, nil

	default:
		ids, err := sendCommentAlbum(ctx, snd, chatID, caption, images)
		if err == nil {
			return ids, nil
		}
		ids, ferr := sendCommentLocalAlbum(ctx, snd, client, workDir, chatID, caption, images)
		if ferr != nil {
			return nil, err
		}
		return ids, nil
	}
}

// isGIFMedia reports an animated image by URL extension.
func isGIFMedia(u string) bool {
	return strings.HasSuffix(strings.ToLower(u), ".gif")
}

// sendCommentSingleImage sends one image by URL: animation for GIFs,
// photo otherwise.
func sendCommentSingleImage(ctx context.Context, snd youtubeMediaSender, chatID int64, caption, imageURL string) ([]int, error) {
	if isGIFMedia(imageURL) {
		m, err := snd.SendAnimation(ctx, &telego.SendAnimationParams{
			ChatID:    telego.ChatID{ID: chatID},
			Animation: telego.InputFile{URL: imageURL},
			Caption:   caption,
			ParseMode: telego.ModeHTML,
		})
		if err != nil {
			return nil, err
		}
		return []int{m.GetMessageID()}, nil
	}
	m, err := snd.SendPhoto(ctx, &telego.SendPhotoParams{
		ChatID:    telego.ChatID{ID: chatID},
		Photo:     telego.InputFile{URL: imageURL},
		Caption:   caption,
		ParseMode: telego.ModeHTML,
	})
	if err != nil {
		return nil, err
	}
	return []int{m.GetMessageID()}, nil
}

// downloadCommentImage fetches one comment image into workDir.
func downloadCommentImage(ctx context.Context, client *http.Client, imageURL, path string) error {
	parsed, err := url.Parse(imageURL)
	if err != nil || parsed.Scheme != "https" {
		return fmt.Errorf("invalid image URL")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, imageURL, nil)
	if err != nil {
		return fmt.Errorf("building image request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("image request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("image status %d", resp.StatusCode)
	}
	file, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("creating image file: %w", err)
	}
	written, copyErr := io.Copy(file, io.LimitReader(resp.Body, tiktokCommentMediaCap+1))
	closeErr := file.Close()
	if copyErr != nil {
		return fmt.Errorf("downloading image: %w", copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("closing image: %w", closeErr)
	}
	if written > tiktokCommentMediaCap {
		return fmt.Errorf("image exceeds %d bytes", tiktokCommentMediaCap)
	}
	return nil
}

// sendCommentLocalImage is the download fallback for one image: sniffs the
// bytes so a GIF keeps its animation even when the URL lied.
func sendCommentLocalImage(ctx context.Context, snd youtubeMediaSender, client *http.Client, workDir string, chatID int64, caption, imageURL string) ([]int, error) {
	path := filepath.Join(workDir, "comment-img")
	if err := downloadCommentImage(ctx, client, imageURL, path); err != nil {
		return nil, err
	}
	head := make([]byte, 512)
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	n, _ := f.Read(head)
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	if http.DetectContentType(head[:n]) == "image/gif" {
		m, err := snd.SendAnimation(ctx, &telego.SendAnimationParams{
			ChatID:    telego.ChatID{ID: chatID},
			Animation: telego.InputFile{File: f},
			Caption:   caption,
			ParseMode: telego.ModeHTML,
		})
		if err != nil {
			return nil, err
		}
		return []int{m.GetMessageID()}, nil
	}
	m, err := snd.SendPhoto(ctx, &telego.SendPhotoParams{
		ChatID:    telego.ChatID{ID: chatID},
		Photo:     telego.InputFile{File: f},
		Caption:   caption,
		ParseMode: telego.ModeHTML,
	})
	if err != nil {
		return nil, err
	}
	return []int{m.GetMessageID()}, nil
}

// sendCommentAlbum sends 2+ images as one album by URL, caption on the
// first item.
func sendCommentAlbum(ctx context.Context, snd youtubeMediaSender, chatID int64, caption string, images []string) ([]int, error) {
	media := make([]telego.InputMedia, 0, len(images))
	for i, imageURL := range images {
		item := &telego.InputMediaPhoto{Media: telego.InputFile{URL: imageURL}}
		if i == 0 {
			item.Caption = caption
			item.ParseMode = telego.ModeHTML
		}
		media = append(media, item)
	}
	sent, err := snd.SendMediaGroup(ctx, &telego.SendMediaGroupParams{
		ChatID: telego.ChatID{ID: chatID},
		Media:  media,
	})
	if err != nil {
		return nil, err
	}
	ids := make([]int, 0, len(sent))
	for _, m := range sent {
		ids = append(ids, m.GetMessageID())
	}
	return ids, nil
}

// sendCommentLocalAlbum is the download fallback for albums. GIF items
// degrade to static photos (Telegram albums cannot carry animations).
func sendCommentLocalAlbum(ctx context.Context, snd youtubeMediaSender, client *http.Client, workDir string, chatID int64, caption string, images []string) ([]int, error) {
	media := make([]telego.InputMedia, 0, len(images))
	for i, imageURL := range images {
		path := filepath.Join(workDir, "comment-img-"+strconv.Itoa(i))
		if err := downloadCommentImage(ctx, client, imageURL, path); err != nil {
			return nil, err
		}
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		item := &telego.InputMediaPhoto{Media: telego.InputFile{File: f}}
		if i == 0 {
			item.Caption = caption
			item.ParseMode = telego.ModeHTML
		}
		media = append(media, item)
	}
	sent, err := snd.SendMediaGroup(ctx, &telego.SendMediaGroupParams{
		ChatID: telego.ChatID{ID: chatID},
		Media:  media,
	})
	if err != nil {
		return nil, err
	}
	ids := make([]int, 0, len(sent))
	for _, m := range sent {
		ids = append(ids, m.GetMessageID())
	}
	return ids, nil
}

// --- Pipeline -------------------------------------------------------------

// processTikTokComment runs the full comment pipeline: fetch, quote, owner
// attribution, delete-original. Runs on its own goroutine (started by the
// tiktokReposter middleware) so the sequential update loop never stalls.
func processTikTokComment(
	ctx context.Context,
	snd youtubeMediaSender,
	log *slog.Logger,
	client *http.Client,
	owners ownerRecorder,
	msg *telego.Message,
	videoURL, commentID string,
) {
	chatID := msg.Chat.ID
	msgID := msg.GetMessageID()

	comment, err := fetchTikTokComment(ctx, client, videoURL, commentID)
	if err != nil {
		log.Warn("tiktok comment: fetch failed",
			"chat_id", chatID, "message_id", msgID, "comment_id", commentID, "error", err)
		sendDecline(ctx, snd, log, chatID, msgID, publicPureFailure(), "tiktok comment: decline note send failed")
		return
	}

	workDir, err := os.MkdirTemp("", "bidlobot-ttcomment-")
	if err != nil {
		log.Error("tiktok comment: temp dir failed", "chat_id", chatID, "error", err)
		sendDecline(ctx, snd, log, chatID, msgID, publicPureFailure(), "tiktok comment: decline note send failed")
		return
	}
	defer os.RemoveAll(workDir)

	caption := tiktokCommentCaption(commentHandle(comment), comment.Text)
	sentIDs, sendErr := sendTikTokComment(ctx, snd, client, workDir, chatID, caption, comment.Images)
	if sendErr != nil {
		log.Warn("tiktok comment: quote send failed; leaving original intact",
			"chat_id", chatID, "message_id", msgID, "error", sendErr)
		sendDecline(ctx, snd, log, chatID, msgID, publicPureFailure(), "tiktok comment: decline note send failed")
		return
	}

	// Reactions on the bot's quote credit whoever posted the link.
	if owners != nil {
		for _, id := range sentIDs {
			owners.RecordOwner(chatID, id, msg.From)
		}
	}

	if delErr := snd.DeleteMessage(ctx, &telego.DeleteMessageParams{
		ChatID:    telego.ChatID{ID: chatID},
		MessageID: msgID,
	}); delErr != nil {
		log.Info("tiktok comment: quote sent but delete failed; original kept",
			"chat_id", chatID, "message_id", msgID, "error", delErr)
	}

	log.Info("tiktok comment: quoted",
		"chat_id", chatID, "message_id", msgID, "comment_id", commentID, "images", len(comment.Images))
}

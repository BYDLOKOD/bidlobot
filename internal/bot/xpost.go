package bot

// X (Twitter) post repost as a single message.
//
// When a supergroup message contains an x.com/twitter.com status link, the
// bot resolves the tweet through the public FixTweet JSON API
// (api.fxtwitter.com), re-sends it as ONE message - tweet text as the
// caption, every photo and video as native Telegram media, and the
// canonical link to the original post - then deletes the user's message.
//
// No renderer sidecar. The retired `xshot` Puppeteer service painted a
// fake tweet card as a PNG; the same API it proxied already returns the
// text plus direct twimg media URLs, so the Go bot assembles the message
// itself. Photos are handed to Telegram as URLs (Telegram fetches them
// server-side); if that fails they are downloaded and uploaded once as a
// fallback. Videos are always downloaded through the bot - the API offers
// several mp4 variants, and the best one whose estimated size fits the
// 50MB bot upload cap is picked offline (a 1080p/10Mbps clip of a minute
// exceeds it, which is why the old "always top variant" path declined).
//
// Contract mirrors the TikTok reposter: repost first, delete the original
// only after a successful send. On any failure the original stays and a
// decline note is posted, so the user's link is never lost.
//
// Documented gaps (same as the TikTok reposter / YT sanitizer):
//   - edited_message: an edit that introduces an X link is not processed.
//   - media groups: only the caption-bearing item is processed; sibling
//     media of the user's own album are not re-sent.
//   - only the first X status link in a message is expanded.
import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"

	"github.com/veschin/bidlobot/internal/shared"
)

const (
	// xpostMetadataTimeout bounds the FixTweet API call.
	xpostMetadataTimeout = 15 * time.Second

	// xpostMetadataLimit caps the tweet JSON we are willing to read.
	xpostMetadataLimit = 1 << 20

	// xpostMaxAlbumItems is Telegram's sendMediaGroup ceiling.
	xpostMaxAlbumItems = 10

	// xpostTranslateTimeout bounds one translation call.
	xpostTranslateTimeout = 60 * time.Second

	// xpostTranslateInputLimit caps the text handed to the translator
	// (long premium posts only ever yield a 1024-unit caption anyway).
	xpostTranslateInputLimit = 4000

	// msgXPostHeaderPrefix is the sender attribution line ("👤 name").
	msgXPostHeaderPrefix = "\U0001F464 "
)

// XPostTranslatePrompt is the system instruction for translating a
// foreign-language tweet to Russian for the repost caption.
const XPostTranslatePrompt = `You translate social media posts to Russian.
Output ONLY the translated text - no preamble, no quotes, no comments.
Preserve line breaks, emoji, URLs, @mentions and #hashtags verbatim.
Keep product names, technical terms and code identifiers in Latin
script. Aim for a natural chat register rather than literal
word-for-word translation.`

var (
	xpostTokenRe = regexp.MustCompile(`[^\s<>"']+`)
	xpostPathRe  = regexp.MustCompile(`^/(?:[A-Za-z0-9_]+/status|i/web/status)/[0-9]+/?$`)
	// xpostStatusRe extracts (username, status id) from an already
	// validated status path. The username segment is what the FixTweet
	// API expects in its path, but the service resolves by status id
	// alone - "i" (the web-app canonical form) works fine.
	xpostStatusRe = regexp.MustCompile(`^/(?:([A-Za-z0-9_]+)/status|i/web/status)/([0-9]+)/?$`)

	xpostSlot       = make(chan struct{}, 1)
	xpostHTTPClient = &http.Client{Timeout: 90 * time.Second}

	// xpostMediaHosts is the allowlist for media the bot downloads
	// itself (tweet videos, fallback photo downloads). Anything the
	// FixTweet API reports outside it is skipped, which keeps a
	// compromised response from turning the bot into a proxy.
	xpostMediaHosts = map[string]struct{}{
		"pbs.twimg.com":   {},
		"video.twimg.com": {},
		"pic.x.com":       {},
	}
)

// xpostTweetResponse is the subset of the FixTweet payload the repost
// needs. Unknown fields are ignored by encoding/json.
type xpostTweetResponse struct {
	Code  int `json:"code"`
	Tweet *struct {
		URL    string `json:"url"`
		Text   string `json:"text"`
		Lang   string `json:"lang"`
		Author struct {
			Name       string `json:"name"`
			ScreenName string `json:"screen_name"`
		} `json:"author"`
		Media struct {
			Photos []struct {
				URL string `json:"url"`
			} `json:"photos"`
			Videos []xpostVideo `json:"videos"`
		} `json:"media"`
	} `json:"tweet"`
}

// xpostVideo mirrors media.videos[] of the FixTweet payload: a
// highest-quality progressive `url` plus every `formats` variant
// (m3u8 playlists and mp4 renditions at different bitrates).
type xpostVideo struct {
	URL      string        `json:"url"`
	Duration float64       `json:"duration"`
	Formats  []xpostFormat `json:"formats"`
}

type xpostFormat struct {
	URL       string  `json:"url"`
	Bitrate   float64 `json:"bitrate"`
	Container string  `json:"container"`
}

// xpostResolvedTweet is the flattened, send-ready view of a tweet.
type xpostResolvedTweet struct {
	URL          string
	Text         string
	Lang         string
	AuthorName   string
	AuthorHandle string
	PhotoURLs    []string
	Videos       []xpostVideo
}

func xpostDecision(msg *telego.Message) (act bool, postURL string) {
	if msg == nil || msg.From == nil || msg.From.IsBot ||
		shared.IsAnonymousAdmin(msg.From.ID) || msg.SenderChat != nil {
		return false, ""
	}

	for _, entities := range [][]telego.MessageEntity{msg.Entities, msg.CaptionEntities} {
		for _, entity := range entities {
			if (entity.Type == "url" || entity.Type == "text_link") && validXPostURL(entity.URL) {
				return true, entity.URL
			}
		}
	}

	for _, text := range []string{msg.Text, msg.Caption} {
		for _, token := range xpostTokenRe.FindAllString(text, -1) {
			candidate := strings.TrimRight(strings.TrimLeft(token, "([{"), ")]}"+trailingPunct)
			if validXPostURL(candidate) {
				return true, candidate
			}
		}
	}
	return false, ""
}

func validXPostURL(raw string) bool {
	if raw == "" {
		return false
	}
	parsed, err := url.Parse(ensureScheme(raw))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil {
		return false
	}
	host := strings.ToLower(parsed.Host)
	for _, prefix := range []string{"www.", "m.", "mobile."} {
		if rest, ok := strings.CutPrefix(host, prefix); ok {
			host = rest
			break
		}
	}
	if host != "x.com" && host != "twitter.com" {
		return false
	}
	return xpostPathRe.MatchString(parsed.EscapedPath())
}

// xpostReposter is the supergroup middleware. The heavy fetch + send runs
// asynchronously behind a single slot (one X repost at a time) so the
// sequential update loop is never stalled; context.Background() is
// mandatory - the per-update ctx is cancelled when the handler returns.
func xpostReposter(a *App) th.Handler {
	return func(thctx *th.Context, update telego.Update) error {
		msg := update.Message
		act, postURL := xpostDecision(msg)
		if !act {
			return thctx.Next(update)
		}

		snd := a.sanitizerSender()
		select {
		case xpostSlot <- struct{}{}:
			go func() {
				defer func() { <-xpostSlot }()
				processXPost(context.Background(), snd, a.log, xpostHTTPClient, xpostAPIBase, a.tweetTranslator, a.repReactor, msg, postURL)
			}()
		default:
			go sendDecline(context.Background(), snd, a.log, msg.Chat.ID, msg.GetMessageID(), publicPureFailure(), "xpost: decline note send failed")
		}
		return thctx.Next(update)
	}
}

// xpostAPIBase is the FixTweet API root; a variable so tests can point
// the client at a stub server.
var xpostAPIBase = "https://api.fxtwitter.com"

func processXPost(
	ctx context.Context,
	snd youtubeMediaSender,
	log *slog.Logger,
	client *http.Client,
	apiBase string,
	translate func(ctx context.Context, text string) (string, error),
	owners ownerRecorder,
	msg *telego.Message,
	postURL string,
) {
	chatID := msg.Chat.ID
	messageID := msg.GetMessageID()

	tweet, err := fetchXPostTweet(ctx, client, apiBase, postURL)
	if err != nil {
		log.Warn("xpost: metadata fetch failed", "chat_id", chatID, "message_id", messageID, "error", err)
		sendDecline(ctx, snd, log, chatID, messageID, publicPureFailure(), "xpost: decline note send failed")
		return
	}

	xpostTranslateTweet(ctx, log, translate, chatID, messageID, tweet)

	workDir, err := os.MkdirTemp("", "bidlobot-xpost-")
	if err != nil {
		log.Warn("xpost: temp dir failed", "chat_id", chatID, "message_id", messageID, "error", err)
		sendDecline(ctx, snd, log, chatID, messageID, publicPureFailure(), "xpost: decline note send failed")
		return
	}
	defer os.RemoveAll(workDir)

	items, closers := buildXPostItems(ctx, client, log, workDir, msg, tweet)
	defer func() {
		for _, f := range closers {
			f.Close()
		}
	}()

	captionLimit := telegoCaptionLimit
	if len(items) == 0 {
		captionLimit = telegoTextLimit
	}
	caption := buildXPostCaption(
		shared.UserDisplay(msg.From.Username, msg.From.FirstName),
		stripXPostLinks(msg.Text+msg.Caption),
		tweet.AuthorName, tweet.AuthorHandle, tweet.Text, tweet.URL, captionLimit,
	)
	sentIDs, sendErr := sendXPostMessage(ctx, snd, client, workDir, chatID, caption, items)
	if sendErr != nil {
		log.Warn("xpost: send failed; leaving original intact", "chat_id", chatID, "message_id", messageID, "error", sendErr)
		sendDecline(ctx, snd, log, chatID, messageID, publicPureFailure(), "xpost: decline note send failed")
		return
	}

	if owners != nil {
		for _, id := range sentIDs {
			owners.RecordOwner(chatID, id, msg.From)
		}
	}

	if delErr := snd.DeleteMessage(ctx, &telego.DeleteMessageParams{
		ChatID:    telego.ChatID{ID: chatID},
		MessageID: messageID,
	}); delErr != nil {
		log.Info("xpost: reposted but delete failed; original kept",
			"chat_id", chatID, "message_id", messageID, "error", delErr)
	}

	log.Info("xpost: reposted", "chat_id", chatID, "message_id", messageID,
		"photos", len(tweet.PhotoURLs), "videos", len(tweet.Videos))
}

// xpostTranslateTweet replaces tweet.Text with a Russian translation
// when the tweet is in neither Russian nor English (the chat reads
// both) and a translator is wired. Any failure keeps the original
// text - the repost is never blocked by the translator.
func xpostTranslateTweet(
	ctx context.Context,
	log *slog.Logger,
	translate func(ctx context.Context, text string) (string, error),
	chatID int64,
	messageID int,
	tweet *xpostResolvedTweet,
) {
	if translate == nil || tweet.Text == "" {
		return
	}
	switch tweet.Lang {
	case "", "und", "ru", "en":
		return
	}
	input := tweet.Text
	if utf16Len(input) > xpostTranslateInputLimit {
		input = truncateUTF16(input, xpostTranslateInputLimit)
	}
	tctx, cancel := context.WithTimeout(ctx, xpostTranslateTimeout)
	defer cancel()
	translated, err := translate(tctx, input)
	if err != nil {
		log.Warn("xpost: translation failed; keeping original",
			"chat_id", chatID, "message_id", messageID, "lang", tweet.Lang, "error", err)
		return
	}
	if strings.TrimSpace(translated) == "" {
		return
	}
	log.Info("xpost: translated", "chat_id", chatID, "message_id", messageID, "lang", tweet.Lang)
	tweet.Text = translated
}

// Telegram caption/text ceilings; the caption path truncates against them.
const (
	telegoCaptionLimit = 1024
	telegoTextLimit    = 4096
)

// buildXPostItems assembles the album items for one repost, in order:
// the user's own attached photo (if the original message carried one, so
// deleting it loses nothing), tweet photos as URL references for Telegram
// to fetch, then tweet videos as downloaded files (best variant that fits
// the upload cap). The slice is capped at Telegram's album size.
//
// A video that cannot be sent (no fitting variant, oversized, download
// error) is skipped and logged - the rest of the post still goes out.
func buildXPostItems(
	ctx context.Context,
	client *http.Client,
	log *slog.Logger,
	workDir string,
	msg *telego.Message,
	tweet *xpostResolvedTweet,
) (items []telego.InputMedia, closers []*os.File) {
	if msg.Photo != nil && len(msg.Photo) > 0 {
		items = append(items, &telego.InputMediaPhoto{
			Type:  telego.MediaTypePhoto,
			Media: telego.InputFile{FileID: largestPhotoFileID(msg.Photo)},
		})
	}
	for _, photoURL := range tweet.PhotoURLs {
		items = append(items, &telego.InputMediaPhoto{
			Type:  telego.MediaTypePhoto,
			Media: telego.InputFile{URL: photoURL},
		})
	}
	for i, video := range tweet.Videos {
		mediaURL, ok := pickXPostVideoVariant(video)
		if !ok {
			log.Warn("xpost: video exceeds upload cap, skipping", "video_index", i+1, "duration_s", video.Duration)
			continue
		}
		path := filepath.Join(workDir, fmt.Sprintf("x-video-%d.mp4", i+1))
		if err := downloadXPostMedia(ctx, client, mediaURL, path); err != nil {
			log.Warn("xpost: video download failed, skipping", "video_index", i+1, "error", err)
			continue
		}
		file, err := os.Open(path)
		if err != nil {
			log.Warn("xpost: video open failed, skipping", "video_index", i+1, "error", err)
			continue
		}
		closers = append(closers, file)
		items = append(items, &telego.InputMediaVideo{
			Type:  telego.MediaTypeVideo,
			Media: telego.InputFile{File: file},
		})
	}
	if len(items) > xpostMaxAlbumItems {
		log.Warn("xpost: album truncated", "kept", xpostMaxAlbumItems, "total", len(items))
		items = items[:xpostMaxAlbumItems]
	}
	return items, closers
}

// pickXPostVideoVariant returns the highest-bitrate mp4 rendition whose
// estimated size (bitrate x duration) fits under the upload cap with 10%
// headroom, falling back to the response's top-level url when no variant
// metadata is present. Videos whose every rendition is estimated too
// large report ok=false and are skipped instead of declined.
func pickXPostVideoVariant(video xpostVideo) (string, bool) {
	type candidate struct {
		url     string
		bitrate float64
	}
	var candidates []candidate
	for _, f := range video.Formats {
		if f.Container == "mp4" && f.URL != "" {
			candidates = append(candidates, candidate{f.URL, f.Bitrate})
		}
	}
	if len(candidates) == 0 && video.URL != "" {
		return video.URL, true
	}
	if len(candidates) == 0 {
		return "", false
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].bitrate > candidates[j].bitrate })

	budget := 0.9 * float64(maxVideoSize)
	for _, c := range candidates {
		if c.bitrate/8*video.Duration <= budget {
			return c.url, true
		}
	}
	return "", false
}

// downloadXPostMedia fetches a media URL from the twimg allowlist into
// path, enforcing the upload cap while streaming.
func downloadXPostMedia(ctx context.Context, client *http.Client, mediaURL, path string) error {
	parsed, err := url.Parse(mediaURL)
	if err != nil || parsed.Scheme != "https" {
		return fmt.Errorf("invalid media URL")
	}
	if _, ok := xpostMediaHosts[strings.ToLower(parsed.Hostname())]; !ok {
		return fmt.Errorf("host not allowed: %s", parsed.Hostname())
	}

	response, err := getXPost(ctx, client, mediaURL)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	file, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("creating download: %w", err)
	}
	written, copyErr := io.Copy(file, io.LimitReader(response.Body, maxVideoSize+1))
	closeErr := file.Close()
	if copyErr != nil {
		return fmt.Errorf("downloading media: %w", copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("closing download: %w", closeErr)
	}
	if written > maxVideoSize {
		return fmt.Errorf("media exceeds %d bytes", maxVideoSize)
	}
	return nil
}

// getXPost performs a GET and requires HTTP 200.
func getXPost(ctx context.Context, client *http.Client, requestURL string) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		return nil, fmt.Errorf("unexpected status %d", response.StatusCode)
	}
	return response, nil
}

// sendXPostMessage delivers the repost as exactly one message: a text
// message, a single photo/video, or a mixed album. URL photos that
// Telegram refuses to fetch fall back to one download+upload retry.
// Returns the sent message IDs for owner attribution.
func sendXPostMessage(
	ctx context.Context,
	snd youtubeMediaSender,
	client *http.Client,
	workDir string,
	chatID int64,
	caption string,
	items []telego.InputMedia,
) ([]int, error) {
	switch {
	case len(items) == 0:
		m, err := snd.SendMessage(ctx, &telego.SendMessageParams{
			ChatID: telego.ChatID{ID: chatID},
			Text:   caption,
		})
		if err != nil {
			return nil, err
		}
		return []int{m.GetMessageID()}, nil

	case len(items) == 1:
		ids, err := sendXPostSingle(ctx, snd, chatID, caption, items[0])
		if err == nil {
			return ids, nil
		}
		if local, ok := materializeXPostItem(ctx, client, workDir, items[0]); ok {
			return sendXPostSingle(ctx, snd, chatID, caption, local)
		}
		return nil, err

	default:
		setXPostCaption(items[0], caption)
		sent, err := snd.SendMediaGroup(ctx, &telego.SendMediaGroupParams{
			ChatID: telego.ChatID{ID: chatID},
			Media:  items,
		})
		if err == nil {
			return xPostMessageIDs(sent), nil
		}
		if local, ok := materializeXPostItems(ctx, client, workDir, items); ok {
			setXPostCaption(local[0], caption)
			sent, err2 := snd.SendMediaGroup(ctx, &telego.SendMediaGroupParams{
				ChatID: telego.ChatID{ID: chatID},
				Media:  local,
			})
			if err2 == nil {
				return xPostMessageIDs(sent), nil
			}
		}
		return nil, err
	}
}

func sendXPostSingle(ctx context.Context, snd youtubeMediaSender, chatID int64, caption string, item telego.InputMedia) ([]int, error) {
	switch m := item.(type) {
	case *telego.InputMediaPhoto:
		sent, err := snd.SendPhoto(ctx, &telego.SendPhotoParams{
			ChatID:  telego.ChatID{ID: chatID},
			Photo:   m.Media,
			Caption: caption,
		})
		if err != nil {
			return nil, err
		}
		return []int{sent.GetMessageID()}, nil
	case *telego.InputMediaVideo:
		sent, err := snd.SendVideo(ctx, &telego.SendVideoParams{
			ChatID:  telego.ChatID{ID: chatID},
			Video:   m.Media,
			Caption: caption,
		})
		if err != nil {
			return nil, err
		}
		return []int{sent.GetMessageID()}, nil
	default:
		return nil, fmt.Errorf("unsupported album item %T", item)
	}
}

// xPostMessageIDs flattens an album send result into message IDs.
func xPostMessageIDs(sent []telego.Message) []int {
	ids := make([]int, 0, len(sent))
	for _, m := range sent {
		ids = append(ids, m.GetMessageID())
	}
	return ids
}

func setXPostCaption(item telego.InputMedia, caption string) {
	switch m := item.(type) {
	case *telego.InputMediaPhoto:
		m.Caption = caption
	case *telego.InputMediaVideo:
		m.Caption = caption
	}
}

// materializeXPostItem rewrites one URL-photo item into a downloaded
// file item (used when Telegram could not fetch the URL itself). Returns
// ok=false for non-URL items and download failures.
func materializeXPostItem(ctx context.Context, client *http.Client, workDir string, item telego.InputMedia) (telego.InputMedia, bool) {
	photo, ok := item.(*telego.InputMediaPhoto)
	if !ok || photo.Media.URL == "" {
		return nil, false
	}
	path := filepath.Join(workDir, fmt.Sprintf("x-photo-%d.jpg", time.Now().UnixNano()))
	if err := downloadXPostMedia(ctx, client, photo.Media.URL, path); err != nil {
		return nil, false
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	return &telego.InputMediaPhoto{
		Type:  telego.MediaTypePhoto,
		Media: telego.InputFile{File: file},
	}, true
}

// materializeXPostItems is the album-wide variant: every URL photo is
// downloaded, file-based items are passed through untouched.
func materializeXPostItems(ctx context.Context, client *http.Client, workDir string, items []telego.InputMedia) ([]telego.InputMedia, bool) {
	local := make([]telego.InputMedia, 0, len(items))
	for i, item := range items {
		photo, ok := item.(*telego.InputMediaPhoto)
		if !ok || photo.Media.URL == "" {
			local = append(local, item)
			continue
		}
		path := filepath.Join(workDir, fmt.Sprintf("x-photo-%d.jpg", i))
		if err := downloadXPostMedia(ctx, client, photo.Media.URL, path); err != nil {
			return nil, false
		}
		file, err := os.Open(path)
		if err != nil {
			return nil, false
		}
		local = append(local, &telego.InputMediaPhoto{
			Type:  telego.MediaTypePhoto,
			Media: telego.InputFile{File: file},
		})
	}
	return local, true
}

// fetchXPostTweet resolves a status URL through the FixTweet API and
// flattens the payload into the send-ready view.
func fetchXPostTweet(ctx context.Context, client *http.Client, apiBase, postURL string) (*xpostResolvedTweet, error) {
	parsed, err := url.Parse(ensureScheme(postURL))
	if err != nil {
		return nil, fmt.Errorf("parsing status URL: %w", err)
	}
	match := xpostStatusRe.FindStringSubmatch(parsed.EscapedPath())
	if match == nil {
		return nil, fmt.Errorf("unrecognized status path %q", parsed.EscapedPath())
	}
	user := match[1]
	if user == "" {
		user = "i"
	}
	requestURL := strings.TrimRight(apiBase, "/") + "/" + user + "/status/" + match[2]

	fetchCtx, cancel := context.WithTimeout(ctx, xpostMetadataTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	request.Header.Set("User-Agent", "bidlobot/2.0 (xpost)")
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, xpostMetadataLimit+1))
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}
	if int64(len(body)) > xpostMetadataLimit {
		return nil, fmt.Errorf("response exceeds %d bytes", xpostMetadataLimit)
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d", response.StatusCode)
	}

	var payload xpostTweetResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("decoding metadata: %w", err)
	}
	if payload.Tweet == nil || payload.Code != http.StatusOK {
		return nil, errors.New("tweet not found")
	}

	tweet := &xpostResolvedTweet{
		URL:          payload.Tweet.URL,
		Text:         payload.Tweet.Text,
		Lang:         strings.ToLower(payload.Tweet.Lang),
		AuthorName:   payload.Tweet.Author.Name,
		AuthorHandle: payload.Tweet.Author.ScreenName,
	}
	for _, photo := range payload.Tweet.Media.Photos {
		if photo.URL != "" {
			tweet.PhotoURLs = append(tweet.PhotoURLs, photo.URL)
		}
	}
	tweet.Videos = payload.Tweet.Media.Videos
	return tweet, nil
}

// stripXPostLinks removes every X status URL token from the user's own
// text so the reposted caption carries the user's words without the raw
// link (the canonical link is appended separately).
func stripXPostLinks(text string) string {
	var b strings.Builder
	last := 0
	for _, loc := range xpostTokenRe.FindAllStringIndex(text, -1) {
		token := text[loc[0]:loc[1]]
		candidate := strings.TrimRight(strings.TrimLeft(token, "([{"), ")]}"+trailingPunct)
		if !validXPostURL(candidate) {
			continue
		}
		b.WriteString(text[last:loc[0]])
		last = loc[1]
	}
	b.WriteString(text[last:])
	return strings.TrimSpace(b.String())
}

// buildXPostCaption assembles the single-message body. Layout:
//
//	👤 <sender>
//	<user's own text around the link, if any>
//
//	<author name> (@<handle>)
//	<tweet text, truncated with ... to fit>
//
//	<canonical status URL>
//
// Budget rules under the Telegram caption limit: the URL and the author
// line are always kept; the tweet text is truncated; the user's own text
// is dropped entirely before the tweet text is cut.
func buildXPostCaption(senderDisplay, userText, authorName, handle, tweetText, postURL string, limit int) string {
	userBlock := msgXPostHeaderPrefix + senderDisplay
	if userText != "" {
		userBlock += "\n" + userText
	}
	authorLine := authorName
	if handle != "" {
		if authorLine != "" {
			authorLine += " "
		}
		authorLine += "(@" + handle + ")"
	}

	body := func(userBlock, text string) string {
		block := authorLine
		if text != "" {
			block += "\n" + text
		}
		return strings.Join([]string{userBlock, block, postURL}, "\n\n")
	}

	text := truncateUTF16(tweetText, xpostTextBudget(limit, utf16Len(userBlock), authorLine, postURL))
	if utf16Len(body(userBlock, text)) <= limit {
		return body(userBlock, text)
	}
	// Over budget even with the tweet text truncated: drop the user's
	// own text first (the link itself is preserved in postURL).
	userBlock = msgXPostHeaderPrefix + senderDisplay
	text = truncateUTF16(tweetText, xpostTextBudget(limit, utf16Len(userBlock), authorLine, postURL))
	out := body(userBlock, text)
	if utf16Len(out) > limit {
		// Pathological author/sender names: hard-trim the result.
		out = truncateUTF16(out, limit)
	}
	return out
}

// xpostTextBudget is the UTF-16 budget for the tweet text inside
// buildXPostCaption's layout (separators accounted for).
func xpostTextBudget(limit, userBlockLen int, authorLine, postURL string) int {
	budget := limit - utf16Len(authorLine) - utf16Len(postURL) - 3
	if userBlockLen > 0 {
		budget -= userBlockLen + 2
	}
	return budget
}

// utf16Len reports the length of s in UTF-16 code units - the unit
// Telegram's caption/text limits are counted in.
func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		if l := utf16.RuneLen(r); l > 0 {
			n += l
		} else {
			n++
		}
	}
	return n
}

// truncateUTF16 cuts s to at most limit UTF-16 code units on a rune
// boundary, appending three ASCII dots as the cut marker when a cut
// happened (deliberately not the U+2026 ellipsis: the marker's length
// is part of the budget arithmetic).
func truncateUTF16(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if utf16Len(s) <= limit {
		return s
	}
	n := 0
	for i, r := range s {
		l := 1
		if rl := utf16.RuneLen(r); rl > 0 {
			l = rl
		}
		if n+l > limit-3 {
			return s[:i] + "..."
		}
		n += l
	}
	return s
}

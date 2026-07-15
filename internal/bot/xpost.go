package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"

	"github.com/veschin/bidlobot/internal/shared"
)

const (
	xpostBaseURL         = "http://xshot:3210"
	xpostMetadataLimit   = 1 << 20
	xpostScreenshotLimit = 10 << 20
)

var (
	xpostTokenRe    = regexp.MustCompile(`[^\s<>"']+`)
	xpostPathRe     = regexp.MustCompile(`^/(?:[A-Za-z0-9_]+/status|i/web/status)/[0-9]+/?$`)
	xpostSlot       = make(chan struct{}, 1)
	xpostHTTPClient = &http.Client{Timeout: 90 * time.Second}
)

type xpostTweetResponse struct {
	Tweet *struct {
		Media struct {
			Videos []struct {
				URL string `json:"url"`
			} `json:"videos"`
		} `json:"media"`
	} `json:"tweet"`
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

func xpostSidecarURL(raw string) string {
	parsed, err := url.Parse(ensureScheme(raw))
	if err != nil {
		return raw
	}
	parsed.Host = strings.ToLower(parsed.Host)
	return parsed.String()
}

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
				processXPost(context.Background(), snd, a.log, xpostHTTPClient, xpostBaseURL, msg, postURL)
			}()
		default:
			go sendDecline(context.Background(), snd, a.log, msg.Chat.ID, msg.GetMessageID(), publicPureFailure(), "xpost: send failed")
		}
		return thctx.Next(update)
	}
}

func processXPost(
	ctx context.Context,
	snd youtubeMediaSender,
	log *slog.Logger,
	client *http.Client,
	baseURL string,
	msg *telego.Message,
	postURL string,
) {
	chatID := msg.Chat.ID
	messageID := msg.GetMessageID()
	sidecarURL := xpostSidecarURL(postURL)
	videos, err := fetchXPostVideos(ctx, client, baseURL, sidecarURL)
	if err != nil {
		log.Warn("xpost: metadata fetch failed", "chat_id", chatID, "message_id", messageID, "error", err)
		sendDecline(ctx, snd, log, chatID, messageID, publicPureFailure(), "xpost: send failed")
		return
	}

	workDir, err := os.MkdirTemp("", "bidlobot-xpost-")
	if err != nil {
		log.Warn("xpost: send failed", "chat_id", chatID, "message_id", messageID, "error", err)
		sendDecline(ctx, snd, log, chatID, messageID, publicPureFailure(), "xpost: send failed")
		return
	}
	defer os.RemoveAll(workDir)

	partialFailure := false
	if err := sendXPostScreenshot(ctx, snd, client, baseURL, workDir, msg, sidecarURL); err != nil {
		partialFailure = true
		logXPostBranchFailure(log, "xpost: screenshot failed", chatID, messageID, 0, err)
	}

	videosSent := 0
	for i, mediaURL := range videos {
		if err := sendXPostVideo(ctx, snd, client, baseURL, workDir, msg, mediaURL, i+1); err != nil {
			partialFailure = true
			logXPostBranchFailure(log, "xpost: video failed", chatID, messageID, i+1, err)
			continue
		}
		videosSent++
	}

	if partialFailure {
		sendDecline(ctx, snd, log, chatID, messageID, publicPureFailure(), "xpost: send failed")
	}
	log.Info("xpost: processed", "chat_id", chatID, "message_id", messageID, "videos_sent", videosSent, "partial_failure", partialFailure)
}

func fetchXPostVideos(ctx context.Context, client *http.Client, baseURL, postURL string) ([]string, error) {
	requestURL := strings.TrimRight(baseURL, "/") + "/api/tweet?url=" + url.QueryEscape(postURL)
	body, _, err := fetchXPostBody(ctx, client, requestURL, xpostMetadataLimit)
	if err != nil {
		return nil, err
	}
	var response xpostTweetResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("decoding metadata: %w", err)
	}
	if response.Tweet == nil {
		return nil, errors.New("metadata missing tweet")
	}
	videos := make([]string, 0, len(response.Tweet.Media.Videos))
	for _, video := range response.Tweet.Media.Videos {
		videos = append(videos, video.URL)
	}
	return videos, nil
}

func sendXPostScreenshot(
	ctx context.Context,
	snd youtubeMediaSender,
	client *http.Client,
	baseURL, workDir string,
	msg *telego.Message,
	postURL string,
) error {
	requestURL := strings.TrimRight(baseURL, "/") + "/api/screenshot?url=" + url.QueryEscape(postURL) + "&watermark=0"
	body, contentType, err := fetchXPostBody(ctx, client, requestURL, xpostScreenshotLimit)
	if err != nil {
		return err
	}
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil || mediaType != "image/png" {
		return fmt.Errorf("unexpected screenshot content type %q", contentType)
	}
	path := filepath.Join(workDir, "x-post.png")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		return fmt.Errorf("writing screenshot: %w", err)
	}
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("opening screenshot: %w", err)
	}
	defer file.Close()
	_, err = snd.SendPhoto(ctx, &telego.SendPhotoParams{
		ChatID: telego.ChatID{ID: msg.Chat.ID},
		Photo:  telego.InputFile{File: file},
		ReplyParameters: &telego.ReplyParameters{
			MessageID: msg.GetMessageID(),
		},
	})
	if err != nil {
		return fmt.Errorf("sending screenshot: %w", err)
	}
	return nil
}

func sendXPostVideo(
	ctx context.Context,
	snd youtubeMediaSender,
	client *http.Client,
	baseURL, workDir string,
	msg *telego.Message,
	mediaURL string,
	index int,
) error {
	requestURL := strings.TrimRight(baseURL, "/") + "/api/video?url=" + url.QueryEscape(mediaURL)
	path := filepath.Join(workDir, fmt.Sprintf("x-video-%d.mp4", index))
	if err := fetchXPostFile(ctx, client, requestURL, path, maxVideoSize); err != nil {
		return err
	}
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("opening video: %w", err)
	}
	_, sendErr := snd.SendVideo(ctx, &telego.SendVideoParams{
		ChatID: telego.ChatID{ID: msg.Chat.ID},
		Video:  telego.InputFile{File: file},
		ReplyParameters: &telego.ReplyParameters{
			MessageID: msg.GetMessageID(),
		},
	})
	closeErr := file.Close()
	if sendErr != nil {
		return fmt.Errorf("sending video: %w", sendErr)
	}
	if closeErr != nil {
		return fmt.Errorf("closing video: %w", closeErr)
	}
	return nil
}

func fetchXPostBody(ctx context.Context, client *http.Client, requestURL string, limit int64) ([]byte, string, error) {
	response, err := getXPost(ctx, client, requestURL)
	if err != nil {
		return nil, "", err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, "", fmt.Errorf("reading response: %w", err)
	}
	if int64(len(body)) > limit {
		return nil, "", fmt.Errorf("response exceeds %d bytes", limit)
	}
	return body, response.Header.Get("Content-Type"), nil
}

func fetchXPostFile(ctx context.Context, client *http.Client, requestURL, path string, limit int64) error {
	response, err := getXPost(ctx, client, requestURL)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	file, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("creating download: %w", err)
	}
	written, copyErr := io.Copy(file, io.LimitReader(response.Body, limit+1))
	closeErr := file.Close()
	if copyErr != nil {
		return fmt.Errorf("downloading media: %w", copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("closing download: %w", closeErr)
	}
	if written > limit {
		return fmt.Errorf("media exceeds %d bytes", limit)
	}
	return nil
}

func getXPost(ctx context.Context, client *http.Client, requestURL string) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %T", err)
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("request failed: %T", err)
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		return nil, fmt.Errorf("unexpected status %d", response.StatusCode)
	}
	return response, nil
}

func logXPostBranchFailure(log *slog.Logger, event string, chatID int64, messageID, videoIndex int, err error) {
	args := []any{"chat_id", chatID, "message_id", messageID}
	if videoIndex > 0 {
		args = append(args, "video_index", videoIndex)
	}
	args = append(args, "error", err)
	if strings.Contains(err.Error(), "sending ") {
		event = "xpost: send failed"
	}
	log.Warn(event, args...)
}

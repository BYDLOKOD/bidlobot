package bot

// TikTok video source through the public tikwm mirror.
//
// The ISP that carries the bot filters TikTok web by TLS SNI: the
// production log holds 24 DNS failures and 7 TLS-EOF failures from the
// yt-dlp path, which fetches TikTok pages. tikwm does the page fetch on
// its own side and hands back a signed, watermark-free MP4 on a
// *.tiktokcdn-us.com host, which the same egress reaches without
// trouble. So the mirror is tried first and yt-dlp remains the fallback.
//
// Measured from the production egress on 2026-09-12: 6/6 fetches of the
// queued https://vt.tiktok.com/ZSq5d4Rxh video returned 200/1067861
// bytes in 1.1-1.5s.
//
// Responses use the same envelope as the comment endpoints
// (code/msg/data); the comment fetcher and this one share the tikwm
// pacer, so the free-tier rate limit sees one client.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

// tikwmPathVideo is the tikwm media endpoint. Same host and envelope as
// the comment endpoints.
const tikwmPathVideo = "/api/"

// mirrorDownloadTimeout bounds one metadata call plus the CDN transfer.
const mirrorDownloadTimeout = 120 * time.Second

// tiktokMediaResponse is one tikwm media row. Images is the photo-post
// signal: a video row carries no images key at all (measured on the
// production egress 2026-09-12), while a /photo/ post carries one entry
// per slide.
type tiktokMediaResponse struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		ID     string   `json:"id"`
		Play   string   `json:"play"`
		Size   int64    `json:"size"`
		Images []string `json:"images"`
	} `json:"data"`
}

// fetchTikTokMedia asks tikwm to resolve rawURL and returns the media
// row. The pacer spaces the call against the comment fetcher so the
// shared free-tier budget is never exceeded.
func fetchTikTokMedia(ctx context.Context, client *http.Client, rawURL string) (tiktokMediaResponse, error) {
	tikwmPace()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		tikwmAPIHost+tikwmPathVideo+"?url="+url.QueryEscape(ensureScheme(rawURL)), nil)
	if err != nil {
		return tiktokMediaResponse{}, fmt.Errorf("building tikwm request: %w", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/138.0.0.0 Safari/537.36")

	resp, err := client.Do(req)
	if err != nil {
		return tiktokMediaResponse{}, fmt.Errorf("tikwm request: %w", err)
	}
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return tiktokMediaResponse{}, fmt.Errorf("tikwm status %d", resp.StatusCode)
	}
	if readErr != nil {
		return tiktokMediaResponse{}, fmt.Errorf("reading tikwm body: %w", readErr)
	}

	var parsed tiktokMediaResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return tiktokMediaResponse{}, fmt.Errorf("decoding tikwm response: %w", err)
	}
	if parsed.Code != 0 {
		return tiktokMediaResponse{}, fmt.Errorf("tikwm error %d: %s", parsed.Code, parsed.Msg)
	}
	return parsed, nil
}

// downloadFromMirror resolves rawURL through tikwm and writes the video
// into workDir, returning its path. The returned path is owned by the
// caller (os.Remove).
//
// The streamed byte count decides the size, never the reported Size
// field: a wrong or zero Size must not let a 200 MB file through.
func downloadFromMirror(ctx context.Context, client *http.Client, rawURL, workDir string) (string, error) {
	media, err := fetchTikTokMedia(ctx, client, rawURL)
	if err != nil {
		return "", err
	}
	if media.Data.Play == "" {
		if len(media.Data.Images) > 0 {
			return "", errNoVideo
		}
		return "", errors.New("tikwm: no play url")
	}
	if media.Data.Size > maxVideoSize {
		return "", fmt.Errorf("tikwm: reported %d bytes exceeds the %d byte cap", media.Data.Size, maxVideoSize)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, media.Data.Play, nil)
	if err != nil {
		return "", fmt.Errorf("building cdn request: %w", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/138.0.0.0 Safari/537.36")

	resp, err := client.Do(req)
	if err != nil {
		return "", mediaFailure(media, fmt.Errorf("cdn request: %w", err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", mediaFailure(media, fmt.Errorf("cdn status %d", resp.StatusCode))
	}

	path := filepath.Join(workDir, "video.mp4")
	f, err := os.Create(path)
	if err != nil {
		return "", fmt.Errorf("create %s: %w", path, err)
	}
	// One byte past the cap: a transfer that reaches it is too large, and
	// the check below catches it without trusting any header.
	written, copyErr := io.Copy(f, io.LimitReader(resp.Body, maxVideoSize+1))
	closeErr := f.Close()
	if copyErr != nil {
		os.Remove(path)
		return "", mediaFailure(media, fmt.Errorf("reading cdn body: %w", copyErr))
	}
	if closeErr != nil {
		os.Remove(path)
		return "", fmt.Errorf("closing %s: %w", path, closeErr)
	}
	if written > maxVideoSize {
		os.Remove(path)
		return "", fmt.Errorf("tiktok: video too large (%d bytes)", written)
	}
	return path, nil
}

// mediaFailure tags a download failure on a post that carries an image
// list. Such a post has no video stream to repost - measured on the
// production egress 2026-09-12: tikwm answers a /photo/ query with a
// non-empty images array and a play URL on a music host that never
// serves an MP4, so a plain retry ladder would queue the job forever.
// A post without images keeps the raw error and falls back to yt-dlp.
func mediaFailure(media tiktokMediaResponse, err error) error {
	if len(media.Data.Images) == 0 {
		return err
	}
	return fmt.Errorf("%w: %v", errNoVideo, err)
}

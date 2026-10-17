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

// tiktokPost distinguishes photo posts from videos before touching Play:
// for photo posts Play may be an audio track, not an MP4.
type tiktokPost struct {
	ID             string
	VideoPath      string
	Images         []string
	AudioURL       string
	AudioTitle     string
	AudioPerformer string
}

// tiktokMediaResponse is one tikwm media row. Images is the photo-post
// signal: a video row carries no images key at all (measured on the
// production egress 2026-09-12), while a /photo/ post carries one entry
// per slide.
type tiktokMediaResponse struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		ID        string   `json:"id"`
		Play      string   `json:"play"`
		Size      int64    `json:"size"`
		Images    []string `json:"images"`
		Music     string   `json:"music"`
		MusicInfo struct {
			Play   string `json:"play"`
			Title  string `json:"title"`
			Author string `json:"author"`
		} `json:"music_info"`
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

// downloadFromMirror resolves a post. Images take precedence over Play,
// even when the music URL is reachable and the mirror reports a size.
// Video byte limits use the streamed size, not the metadata/header.
func downloadFromMirror(ctx context.Context, client *http.Client, rawURL, workDir string) (tiktokPost, error) {
	media, err := fetchTikTokMedia(ctx, client, rawURL)
	if err != nil {
		return tiktokPost{}, err
	}
	post := tiktokPost{ID: media.Data.ID, Images: media.Data.Images}
	if len(post.Images) > 0 {
		post.AudioURL = media.Data.MusicInfo.Play
		if post.AudioURL == "" {
			post.AudioURL = media.Data.Music
		}
		if post.AudioURL == "" {
			post.AudioURL = media.Data.Play
		}
		post.AudioTitle = media.Data.MusicInfo.Title
		post.AudioPerformer = media.Data.MusicInfo.Author
		return post, nil
	}
	if media.Data.Play == "" {
		return tiktokPost{}, errors.New("tikwm: no play url")
	}
	if media.Data.Size > maxVideoSize {
		return tiktokPost{}, fmt.Errorf("tikwm: reported %d bytes exceeds the %d byte cap", media.Data.Size, maxVideoSize)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, media.Data.Play, nil)
	if err != nil {
		return tiktokPost{}, fmt.Errorf("building cdn request: %w", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/138.0.0.0 Safari/537.36")
	resp, err := client.Do(req)
	if err != nil {
		return tiktokPost{}, fmt.Errorf("cdn request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return tiktokPost{}, fmt.Errorf("cdn status %d", resp.StatusCode)
	}

	path := filepath.Join(workDir, "video.mp4")
	f, err := os.Create(path)
	if err != nil {
		return tiktokPost{}, fmt.Errorf("create %s: %w", path, err)
	}
	written, copyErr := io.Copy(f, io.LimitReader(resp.Body, maxVideoSize+1))
	closeErr := f.Close()
	if copyErr != nil || closeErr != nil || written > maxVideoSize {
		os.Remove(path)
		if copyErr != nil {
			return tiktokPost{}, fmt.Errorf("reading cdn body: %w", copyErr)
		}
		if closeErr != nil {
			return tiktokPost{}, fmt.Errorf("closing %s: %w", path, closeErr)
		}
		return tiktokPost{}, fmt.Errorf("tiktok: video too large (%d bytes)", written)
	}
	post.VideoPath = path
	return post, nil
}

package bot

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
)

var errTikTokAudioTooLarge = errors.New("tiktok: soundtrack exceeds Telegram's audio upload limit")

// downloadTikTokAudio streams the original track unchanged. Telegram music
// uploads share the 50 MiB video cap. The byte count, not Content-Length alone,
// bounds chunked or incorrectly reported bodies; failed files are removed.
func downloadTikTokAudio(ctx context.Context, client *http.Client, rawURL, path string) error {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return errors.New("tiktok: invalid soundtrack URL")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return fmt.Errorf("building audio request: %w", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/138.0.0.0 Safari/537.36")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("audio request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("audio status %d", resp.StatusCode)
	}
	if resp.ContentLength > maxVideoSize {
		return errTikTokAudioTooLarge
	}
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("creating soundtrack: %w", err)
	}
	written, copyErr := io.Copy(f, io.LimitReader(resp.Body, maxVideoSize+1))
	closeErr := f.Close()
	if copyErr != nil || closeErr != nil || written == 0 || written > maxVideoSize {
		os.Remove(path)
		switch {
		case copyErr != nil:
			return fmt.Errorf("reading audio body: %w", copyErr)
		case closeErr != nil:
			return fmt.Errorf("closing soundtrack: %w", closeErr)
		case written > maxVideoSize:
			return errTikTokAudioTooLarge
		default:
			return errors.New("tiktok: empty soundtrack body")
		}
	}
	return nil
}

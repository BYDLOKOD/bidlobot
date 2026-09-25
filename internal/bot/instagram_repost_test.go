package bot

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mymmrac/telego"
	"github.com/mymmrac/telego/telegoapi"

	"github.com/veschin/bidlobot/internal/storage"
)

// igTestMessage builds a telego.Message with the defaults the Instagram
// decision tests share. From is a regular, non-bot user.
func igTestMessage(text string) *telego.Message {
	return &telego.Message{
		MessageID: 42,
		Chat:      telego.Chat{ID: -1001234567890, Type: telego.ChatTypeSupergroup},
		From:      &telego.User{ID: 200, Username: "alice", FirstName: "Alice"},
		Text:      text,
	}
}

func TestInstagramDecision(t *testing.T) {
	tests := []struct {
		name string
		msg  *telego.Message
		want bool
	}{
		{name: "valid reel link", msg: igTestMessage("https://www.instagram.com/reel/C8bXvT1J9yQ/"), want: true},
		{name: "reel link with share query", msg: igTestMessage("https://www.instagram.com/reel/C8bXvT1J9yQ/?igsh=MWZq"), want: true},
		{name: "reels plural form", msg: igTestMessage("https://www.instagram.com/reels/C8bXvT1J9yQ/"), want: true},
		{name: "tv permalink", msg: igTestMessage("https://www.instagram.com/tv/C8bXvT1J9yQ/"), want: true},
		{name: "p permalink", msg: igTestMessage("https://www.instagram.com/p/C8bXvT1J9yQ/"), want: true},
		{name: "m subdomain", msg: igTestMessage("https://m.instagram.com/reel/C8bXvT1J9yQ/"), want: true},
		{name: "legacy instagr.am host", msg: igTestMessage("https://instagr.am/p/C8bXvT1J9yQ/"), want: true},
		{name: "scheme-less bare host", msg: igTestMessage("instagram.com/reel/C8bXvT1J9yQ/"), want: true},
		{name: "non-Instagram URL", msg: igTestMessage("https://www.tiktok.com/@user/video/123"), want: false},
		{name: "lookalike host", msg: igTestMessage("https://instagram.com.evil.example/reel/C8bXvT1J9yQ/"), want: false},
		{name: "profile URL without a post", msg: igTestMessage("https://www.instagram.com/someuser/"), want: false},
		{name: "stories are not permalinks", msg: igTestMessage("https://www.instagram.com/stories/someuser/123456789/"), want: false},
		{name: "explore link", msg: igTestMessage("https://www.instagram.com/explore/tags/golang/"), want: false},
		{name: "empty text", msg: igTestMessage(""), want: false},
		{name: "nil message", msg: nil, want: false},
		{
			name: "nil sender",
			msg: &telego.Message{
				MessageID: 1,
				Chat:      telego.Chat{ID: -100123, Type: telego.ChatTypeSupergroup},
				Text:      "https://www.instagram.com/reel/C8bXvT1J9yQ/",
			},
			want: false,
		},
		{
			name: "bot sender",
			msg: &telego.Message{
				MessageID: 1,
				Chat:      telego.Chat{ID: -100123, Type: telego.ChatTypeSupergroup},
				From:      &telego.User{ID: 100, IsBot: true},
				Text:      "https://www.instagram.com/reel/C8bXvT1J9yQ/",
			},
			want: false,
		},
		{
			name: "anonymous admin sender",
			msg: &telego.Message{
				MessageID: 1,
				Chat:      telego.Chat{ID: -100123, Type: telego.ChatTypeSupergroup},
				From:      &telego.User{ID: 1087968824}, // GroupAnonymousBot
				Text:      "https://www.instagram.com/reel/C8bXvT1J9yQ/",
			},
			want: false,
		},
		{
			name: "channel-as-sender",
			msg: &telego.Message{
				MessageID:  1,
				Chat:       telego.Chat{ID: -100123, Type: telego.ChatTypeSupergroup},
				From:       &telego.User{ID: 200},
				SenderChat: &telego.Chat{ID: -100456},
				Text:       "https://www.instagram.com/reel/C8bXvT1J9yQ/",
			},
			want: false,
		},
		{
			name: "text_link entity with Instagram host",
			msg: &telego.Message{
				MessageID: 1,
				Chat:      telego.Chat{ID: -100123, Type: telego.ChatTypeSupergroup},
				From:      &telego.User{ID: 200},
				Text:      "рилс",
				Entities: []telego.MessageEntity{
					{Type: "text_link", Offset: 0, Length: 4, URL: "https://instagram.com/reel/C8bXvT1J9yQ/"},
				},
			},
			want: true,
		},
		{
			name: "caption with Instagram link",
			msg: &telego.Message{
				MessageID: 1,
				Chat:      telego.Chat{ID: -100123, Type: telego.ChatTypeSupergroup},
				From:      &telego.User{ID: 200},
				Caption:   "https://www.instagram.com/reel/C8bXvT1J9yQ/",
			},
			want: true,
		},
		{
			name: "caption entity with Instagram host",
			msg: &telego.Message{
				MessageID: 1,
				Chat:      telego.Chat{ID: -100123, Type: telego.ChatTypeSupergroup},
				From:      &telego.User{ID: 200},
				Caption:   "рилс",
				CaptionEntities: []telego.MessageEntity{
					{Type: "text_link", Offset: 0, Length: 4, URL: "https://instagram.com/reels/C8bXvT1J9yQ/"},
				},
			},
			want: true,
		},
		{
			name: "entity points at a profile despite Instagram-looking text",
			msg: &telego.Message{
				MessageID: 1,
				Chat:      telego.Chat{ID: -100123, Type: telego.ChatTypeSupergroup},
				From:      &telego.User{ID: 200},
				Text:      "https://www.instagram.com/someuser/",
				Entities: []telego.MessageEntity{
					{Type: "text_link", Offset: 0, Length: 35, URL: "https://www.instagram.com/someuser/"},
				},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			act, url := instagramDecision(tt.msg)
			if act != tt.want {
				t.Fatalf("act = %v, want %v", act, tt.want)
			}
			if tt.want && url == "" {
				t.Error("wanted non-empty URL")
			}
			if !tt.want && url != "" {
				t.Errorf("unexpected URL: %s", url)
			}
		})
	}
}

// TestInstagramDecisionTrimsTrailingPunctuation pins the scan path: the
// token regex is greedy, so sentence punctuation must be trimmed before
// the URL is parsed, otherwise yt-dlp would be handed ".../," .
func TestInstagramDecisionTrimsTrailingPunctuation(t *testing.T) {
	msg := igTestMessage("классный рилс https://www.instagram.com/reel/C8bXvT1J9yQ/, ну как?")
	act, got := instagramDecision(msg)
	if !act {
		t.Fatal("expected detection")
	}
	if got != "https://www.instagram.com/reel/C8bXvT1J9yQ/" {
		t.Errorf("URL = %q, want the link without the trailing comma", got)
	}
}

// TestIgPermanentMarker pins the failure classification: the three
// sentences yt-dlp prints for a post that will never yield a video must be
// recognised (so the job is declined instead of retried for 48h), while a
// transport failure must not be.
func TestIgPermanentMarker(t *testing.T) {
	permanent := []string{
		"ERROR: [Instagram] abc: There is no video in this post",
		"ERROR: [Instagram] abc: No video formats found!; please report this issue",
		"ERROR: [Instagram] abc: Instagram sent an empty media response. Check if this post is accessible",
	}
	for _, out := range permanent {
		if got := igPermanentMarker([]byte(out)); got == "" {
			t.Errorf("output %q must be classified permanent", out)
		}
	}

	transient := []string{
		"ERROR: Unable to download webpage: Could not resolve host: www.instagram.com",
		"ERROR: [Instagram] abc: HTTP Error 500: Internal Server Error",
		"ERROR: [Instagram] abc: Requested content is not available, rate-limit reached or login required",
		"",
	}
	for _, out := range transient {
		if got := igPermanentMarker([]byte(out)); got != "" {
			t.Errorf("output %q must stay retryable, matched %q", out, got)
		}
	}
}

// writeStubYtDlp writes an executable shell script that stands in for
// yt-dlp. Each line runs in order; the script always appends one line to a
// counter file first, so a test can count attempts.
func writeStubYtDlp(t *testing.T, exitCode int, lines ...string) (bin, counter string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("stub downloader is a POSIX shell script")
	}
	dir := t.TempDir()
	counter = filepath.Join(dir, "calls")
	bin = filepath.Join(dir, "yt-dlp-stub")
	body := "#!/bin/sh\n" +
		"printf 'x\\n' >> \"" + counter + "\"\n" +
		strings.Join(lines, "\n") + "\n" +
		"exit " + strconv.Itoa(exitCode) + "\n"
	if err := os.WriteFile(bin, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, counter
}

func countCalls(t *testing.T, path string) int {
	t.Helper()
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(b), "\n")
}

// useStubYtDlp points the shared binary seam at a stub for one test.
func useStubYtDlp(t *testing.T, bin string) {
	t.Helper()
	orig := ytDlpBinary
	ytDlpBinary = bin
	t.Cleanup(func() { ytDlpBinary = orig })
}

// TestDownloadInstagramSuccess verifies the successful path: the file the
// downloader wrote into the work dir is returned after one attempt.
func TestDownloadInstagramSuccess(t *testing.T) {
	workDir := t.TempDir()
	bin, counter := writeStubYtDlp(t, 0, `printf 'video' > "`+workDir+`/video.mp4"`)
	useStubYtDlp(t, bin)

	path, err := downloadInstagram(context.Background(), InstagramDownloadOptions{}, "https://www.instagram.com/reel/C8bXvT1J9yQ/", workDir)
	if err != nil {
		t.Fatalf("downloadInstagram: %v", err)
	}
	if path != filepath.Join(workDir, "video.mp4") {
		t.Errorf("path = %q", path)
	}
	if n := countCalls(t, counter); n != 1 {
		t.Errorf("attempts = %d, want 1", n)
	}
}

// TestDownloadInstagramPermanentStopsAfterFirstAttempt verifies a
// "no video" answer is not retried: three attempts would cost ~140s of
// someone's link and could not change the outcome.
func TestDownloadInstagramPermanentStopsAfterFirstAttempt(t *testing.T) {
	workDir := t.TempDir()
	bin, counter := writeStubYtDlp(t, 1, `echo "ERROR: [Instagram] abc: There is no video in this post" >&2`)
	useStubYtDlp(t, bin)

	_, err := downloadInstagram(context.Background(), InstagramDownloadOptions{}, "https://www.instagram.com/p/abc/", workDir)
	if !errors.Is(err, errNoVideo) {
		t.Fatalf("err = %v, want errNoVideo", err)
	}
	if n := countCalls(t, counter); n != 1 {
		t.Errorf("attempts = %d, want 1 (permanent failures must not retry)", n)
	}
}

// TestDownloadInstagramRetriesTransient verifies a transport failure is
// retried (the retry ladder is what keeps the queue empty in the common
// case).
func TestDownloadInstagramRetriesTransient(t *testing.T) {
	workDir := t.TempDir()
	bin, counter := writeStubYtDlp(t, 1, `echo "ERROR: Unable to download webpage: Could not resolve host" >&2`)
	useStubYtDlp(t, bin)

	_, err := downloadInstagram(context.Background(), InstagramDownloadOptions{}, "https://www.instagram.com/reel/C8bXvT1J9yQ/", workDir)
	if err == nil {
		t.Fatal("expected an error")
	}
	if errors.Is(err, errNoVideo) {
		t.Fatalf("transport failure must not be classified permanent: %v", err)
	}
	if n := countCalls(t, counter); n != downloadAttempts {
		t.Errorf("attempts = %d, want %d", n, downloadAttempts)
	}
}

// TestDownloadInstagramPassesEgressOptions pins the argv contract: the
// proxy and cookie jar reach yt-dlp only when configured, and the
// permalink is handed over with a scheme.
func TestDownloadInstagramPassesEgressOptions(t *testing.T) {
	workDir := t.TempDir()
	argsFile := filepath.Join(workDir, "args")
	bin, _ := writeStubYtDlp(t, 0,
		`printf '%s\n' "$@" > "`+argsFile+`"`,
		`printf 'video' > "`+workDir+`/video.mp4"`,
	)
	useStubYtDlp(t, bin)

	opts := InstagramDownloadOptions{Proxy: "socks5h://127.0.0.1:1080", Cookies: "/etc/bidlobot/ig-cookies.txt"}
	if _, err := downloadInstagram(context.Background(), opts, "https://www.instagram.com/reel/C8bXvT1J9yQ/", workDir); err != nil {
		t.Fatalf("downloadInstagram: %v", err)
	}

	args, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	got := string(args)
	for _, want := range []string{
		"-f\nb[ext=mp4]/b\n",
		"--proxy\nsocks5h://127.0.0.1:1080\n",
		"--cookies\n/etc/bidlobot/ig-cookies.txt\n",
		"--no-playlist\n",
		"https://www.instagram.com/reel/C8bXvT1J9yQ/\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("argv missing %q; got:\n%s", want, got)
		}
	}

	// A scheme-less permalink must be handed to yt-dlp with https://.
	if _, err := downloadInstagram(context.Background(), InstagramDownloadOptions{}, "instagram.com/reel/C8bXvT1J9yQ/", workDir); err != nil {
		t.Fatalf("downloadInstagram: %v", err)
	}
	args, err = os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(args), "--proxy") {
		t.Errorf("unset proxy must not reach yt-dlp; got:\n%s", args)
	}
	if !strings.Contains(string(args), "\nhttps://instagram.com/reel/C8bXvT1J9yQ/\n") {
		t.Errorf("scheme-less link must be normalised; got:\n%s", args)
	}
}

// --- Pipeline ------------------------------------------------------------

// TestProcessInstagramWithSyntheticFile verifies the full pipeline using a
// synthetic temp file (videoPath != "" bypasses the downloader). The
// repost-first contract: SendVideo, then DeleteMessage of the original.
func TestProcessInstagramWithSyntheticFile(t *testing.T) {
	dir := t.TempDir()
	videoPath := filepath.Join(dir, "test.mp4")
	if err := os.WriteFile(videoPath, []byte("fake mp4 content"), 0o644); err != nil {
		t.Fatal(err)
	}

	snd := &recYTSender{}
	owners := &recOwners{}
	msg := igTestMessage("https://www.instagram.com/reel/C8bXvT1J9yQ/")
	msg.Caption = "original caption"

	processInstagram(context.Background(), snd, slog.New(slog.DiscardHandler), nil, owners,
		InstagramDownloadOptions{}, msg, "https://www.instagram.com/reel/C8bXvT1J9yQ/", videoPath)

	if len(snd.Videos) != 1 {
		t.Fatalf("expected 1 SendVideo, got %d", len(snd.Videos))
	}
	v := snd.Videos[0]
	if v.ChatID.ID != msg.Chat.ID {
		t.Errorf("ChatID = %d, want %d", v.ChatID.ID, msg.Chat.ID)
	}
	if v.ParseMode != telego.ModeHTML {
		t.Errorf("ParseMode = %s, want %s", v.ParseMode, telego.ModeHTML)
	}
	if !strings.Contains(v.Caption, "alice") || !strings.Contains(v.Caption, "original caption") {
		t.Errorf("caption = %q, want sender name + original caption", v.Caption)
	}

	if len(snd.Deletes) != 1 {
		t.Fatalf("expected 1 DeleteMessage, got %d", len(snd.Deletes))
	}
	if d := snd.Deletes[0]; d.ChatID.ID != msg.Chat.ID || d.MessageID != msg.MessageID {
		t.Errorf("delete = %d/%d, want %d/%d", d.ChatID.ID, d.MessageID, msg.Chat.ID, msg.MessageID)
	}

	// Reactions on the bot's repost credit the original sender.
	if calls := owners.recorded(); len(calls) != 1 || calls[0] != "-1001234567890:1002:200" {
		t.Fatalf("owner calls = %v", calls)
	}
}

func TestProcessInstagramTooLargeDeclines(t *testing.T) {
	dir := t.TempDir()
	videoPath := filepath.Join(dir, "big.mp4")
	f, err := os.Create(videoPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(maxVideoSize + 1); err != nil {
		f.Close()
		t.Fatal(err)
	}
	f.Close()

	snd := &recYTSender{}
	q := &fakeDeferredQueue{}
	msg := igTestMessage("https://www.instagram.com/reel/C8bXvT1J9yQ/")

	processInstagram(context.Background(), snd, slog.New(slog.DiscardHandler), q, nil,
		InstagramDownloadOptions{}, msg, "https://www.instagram.com/reel/C8bXvT1J9yQ/", videoPath)

	if len(snd.Videos) != 0 {
		t.Errorf("expected 0 SendVideo, got %d", len(snd.Videos))
	}
	if len(q.jobs) != 0 {
		t.Errorf("an oversized file must not be queued, got %d jobs", len(q.jobs))
	}
	if len(snd.Messages) != 1 {
		t.Fatalf("expected 1 decline, got %d", len(snd.Messages))
	}
	if !failureCatalogContains(snd.Messages[0].Text) {
		t.Errorf("decline must be from FailureCatalog; got %q", snd.Messages[0].Text)
	}
	if len(snd.Deletes) != 0 {
		t.Errorf("original must be kept, got %d deletes", len(snd.Deletes))
	}
}

// TestProcessInstagramNoVideoDeclines pins the photo-post rule: a
// permanent "no video" answer gets one public decline and is never queued,
// because no retry could complete it.
func TestProcessInstagramNoVideoDeclines(t *testing.T) {
	origDownload := igDownload
	igDownload = func(context.Context, InstagramDownloadOptions, string, string) (string, error) {
		return "", errNoVideo
	}
	defer func() { igDownload = origDownload }()

	q := &fakeDeferredQueue{}
	snd := &recYTSender{}
	msg := igTestMessage("https://www.instagram.com/p/C8bXvT1J9yQ/")

	processInstagram(context.Background(), snd, slog.New(slog.DiscardHandler), q, nil,
		InstagramDownloadOptions{}, msg, "https://www.instagram.com/p/C8bXvT1J9yQ/", "")

	if len(q.jobs) != 0 {
		t.Errorf("a photo post must not be queued, got %d jobs", len(q.jobs))
	}
	if len(snd.Messages) != 1 {
		t.Fatalf("expected 1 decline, got %d", len(snd.Messages))
	}
	if !failureCatalogContains(snd.Messages[0].Text) {
		t.Errorf("decline must be from FailureCatalog; got %q", snd.Messages[0].Text)
	}
}

// TestProcessInstagramDownloadFailureQueues verifies a transient download
// failure is persisted for /flush with the payload the flush path needs.
func TestProcessInstagramDownloadFailureQueues(t *testing.T) {
	origDownload := igDownload
	igDownload = func(context.Context, InstagramDownloadOptions, string, string) (string, error) {
		return "", errors.New("dial tcp: connection refused")
	}
	defer func() { igDownload = origDownload }()

	q := &fakeDeferredQueue{}
	snd := &recYTSender{}
	msg := igTestMessage("https://www.instagram.com/reel/C8bXvT1J9yQ/")
	msg.Caption = "подпись"

	processInstagram(context.Background(), snd, slog.New(slog.DiscardHandler), q, nil,
		InstagramDownloadOptions{}, msg, "https://www.instagram.com/reel/C8bXvT1J9yQ/", "")

	if len(q.jobs) != 1 {
		t.Fatalf("expected 1 queued job, got %d", len(q.jobs))
	}
	job := q.jobs[0]
	if job.Type != storage.DeferredInstagram {
		t.Errorf("job type = %q, want %q", job.Type, storage.DeferredInstagram)
	}
	if job.UserID != 200 || job.MessageID != 42 {
		t.Errorf("job = %+v", job)
	}
	var p storage.RepostPayload
	if err := json.Unmarshal(job.Payload, &p); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if p.URL != "https://www.instagram.com/reel/C8bXvT1J9yQ/" {
		t.Errorf("payload URL = %q", p.URL)
	}
	if p.Username != "alice" || p.FirstName != "Alice" || p.Caption != "подпись" {
		t.Errorf("payload = %+v, want the sender identity and caption preserved", p)
	}
	if len(snd.Messages) != 0 {
		t.Errorf("a queued retry must not also decline; got %d messages", len(snd.Messages))
	}
	if len(snd.Deletes) != 0 {
		t.Errorf("original must survive a failed download, got %d deletes", len(snd.Deletes))
	}
}

// TestProcessInstagramDownloadFailureNoQueueDeclines covers a minimal App
// without the deferred queue wired: the user must not be left in silence.
func TestProcessInstagramDownloadFailureNoQueueDeclines(t *testing.T) {
	origDownload := igDownload
	igDownload = func(context.Context, InstagramDownloadOptions, string, string) (string, error) {
		return "", errors.New("dial tcp: i/o timeout")
	}
	defer func() { igDownload = origDownload }()

	snd := &recYTSender{}
	msg := igTestMessage("https://www.instagram.com/reel/C8bXvT1J9yQ/")

	processInstagram(context.Background(), snd, slog.New(slog.DiscardHandler), nil, nil,
		InstagramDownloadOptions{}, msg, "https://www.instagram.com/reel/C8bXvT1J9yQ/", "")

	if len(snd.Messages) != 1 {
		t.Fatalf("expected 1 decline, got %d", len(snd.Messages))
	}
	if !failureCatalogContains(snd.Messages[0].Text) {
		t.Errorf("decline must be from FailureCatalog; got %q", snd.Messages[0].Text)
	}
}

// TestProcessInstagramTransportSendFailureQueues covers a transport fault
// during the repost: Telegram never saw the request, so the job is
// replayable and goes to the deferred queue instead of being lost.
func TestProcessInstagramTransportSendFailureQueues(t *testing.T) {
	dir := t.TempDir()
	videoPath := filepath.Join(dir, "test.mp4")
	if err := os.WriteFile(videoPath, []byte("fake mp4"), 0o644); err != nil {
		t.Fatal(err)
	}

	q := &fakeDeferredQueue{}
	snd := &recYTSender{VideoErr: errors.New("fasthttp do request: timeout")}
	msg := igTestMessage("https://www.instagram.com/reel/C8bXvT1J9yQ/")

	processInstagram(context.Background(), snd, slog.New(slog.DiscardHandler), q, nil,
		InstagramDownloadOptions{}, msg, "https://www.instagram.com/reel/C8bXvT1J9yQ/", videoPath)

	if len(q.jobs) != 1 {
		t.Fatalf("expected 1 queued job, got %d", len(q.jobs))
	}
	if len(snd.Messages) != 0 {
		t.Errorf("a queued retry must not also decline; got %d messages", len(snd.Messages))
	}
	if len(snd.Deletes) != 0 {
		t.Errorf("original must be kept, got %d deletes", len(snd.Deletes))
	}
}

// TestProcessInstagramPermanentSendRejectionDeclines covers a 4xx verdict
// from Telegram (bad file, chat forbidden): retrying the same bytes cannot
// help, so the user gets the decline note and nothing is queued.
func TestProcessInstagramPermanentSendRejectionDeclines(t *testing.T) {
	dir := t.TempDir()
	videoPath := filepath.Join(dir, "test.mp4")
	if err := os.WriteFile(videoPath, []byte("fake mp4"), 0o644); err != nil {
		t.Fatal(err)
	}

	q := &fakeDeferredQueue{}
	snd := &recYTSender{VideoErr: &telegoapi.Error{ErrorCode: 400, Description: "Bad Request: file is too big"}}
	msg := igTestMessage("https://www.instagram.com/reel/C8bXvT1J9yQ/")

	processInstagram(context.Background(), snd, slog.New(slog.DiscardHandler), q, nil,
		InstagramDownloadOptions{}, msg, "https://www.instagram.com/reel/C8bXvT1J9yQ/", videoPath)

	if len(q.jobs) != 0 {
		t.Errorf("a 4xx rejection must not be queued, got %d jobs", len(q.jobs))
	}
	if len(snd.Messages) != 1 {
		t.Fatalf("expected 1 decline, got %d", len(snd.Messages))
	}
	if !failureCatalogContains(snd.Messages[0].Text) {
		t.Errorf("decline must be from FailureCatalog; got %q", snd.Messages[0].Text)
	}
}

// TestProcessInstagramDeleteFailsRepostStands pins the repost-first
// contract: when the delete is refused the repost still stands and the
// original is kept (visible duplicate, lesser evil).
func TestProcessInstagramDeleteFailsRepostStands(t *testing.T) {
	dir := t.TempDir()
	videoPath := filepath.Join(dir, "test.mp4")
	if err := os.WriteFile(videoPath, []byte("fake mp4 content"), 0o644); err != nil {
		t.Fatal(err)
	}

	snd := &recYTSender{DeleteErr: errors.New("no delete right")}
	msg := igTestMessage("https://www.instagram.com/reel/C8bXvT1J9yQ/")

	processInstagram(context.Background(), snd, slog.New(slog.DiscardHandler), nil, nil,
		InstagramDownloadOptions{}, msg, "https://www.instagram.com/reel/C8bXvT1J9yQ/", videoPath)

	if len(snd.Videos) != 1 {
		t.Fatalf("expected 1 SendVideo (repost), got %d", len(snd.Videos))
	}
	if len(snd.Deletes) != 1 {
		t.Errorf("expected 1 DeleteMessage attempt, got %d", len(snd.Deletes))
	}
	if len(snd.Messages) != 0 {
		t.Errorf("delete failure is logged, not announced; got %d messages", len(snd.Messages))
	}
}

// TestTryInstagramExportNoVideoDropsJob verifies the flush path drops a
// job whose permalink turned out to carry no video: it reports once and
// returns nil so the caller removes it from the queue.
func TestTryInstagramExportNoVideoDropsJob(t *testing.T) {
	origDownload := igDownload
	igDownload = func(context.Context, InstagramDownloadOptions, string, string) (string, error) {
		return "", errNoVideo
	}
	defer func() { igDownload = origDownload }()

	snd := &recYTSender{}
	err := tryInstagramExport(context.Background(), snd, slog.New(slog.DiscardHandler), nil,
		InstagramDownloadOptions{}, -100123, 42, 200,
		"https://www.instagram.com/p/C8bXvT1J9yQ/", "alice", "Alice", "caption")

	if err != nil {
		t.Fatalf("err = %v, want nil so the job is dropped", err)
	}
	if len(snd.Messages) != 1 {
		t.Fatalf("expected 1 decline in the chat, got %d", len(snd.Messages))
	}
	if !failureCatalogContains(snd.Messages[0].Text) {
		t.Errorf("decline must be from FailureCatalog; got %q", snd.Messages[0].Text)
	}
}

// TestTryInstagramExportRetryableStaysQueued verifies the flush path keeps
// a job whose download failed transiently.
func TestTryInstagramExportRetryableStaysQueued(t *testing.T) {
	origDownload := igDownload
	igDownload = func(context.Context, InstagramDownloadOptions, string, string) (string, error) {
		return "", errors.New("connection reset by peer")
	}
	defer func() { igDownload = origDownload }()

	snd := &recYTSender{}
	err := tryInstagramExport(context.Background(), snd, slog.New(slog.DiscardHandler), nil,
		InstagramDownloadOptions{}, -100123, 42, 200,
		"https://www.instagram.com/reel/C8bXvT1J9yQ/", "alice", "Alice", "caption")

	if err == nil {
		t.Fatal("expected an error so the caller keeps the job")
	}
	if len(snd.Messages) != 0 {
		t.Errorf("a retryable failure must stay silent; got %d messages", len(snd.Messages))
	}
}

// --- Link normalisation --------------------------------------------------

// TestInstagramPostURLNormalisation pins the host rewrite: the yt-dlp
// Instagram extractor matches only https://www.instagram.com/(p|tv|reels?)/,
// so an m. or instagr.am link must reach the downloader rewritten, otherwise
// it falls through to the generic extractor, spends the whole retry ladder
// and lands in the queue.
func TestInstagramPostURLNormalisation(t *testing.T) {
	tests := []struct {
		in   string
		want string
		ok   bool
	}{
		{"https://www.instagram.com/reel/C8bXvT1J9yQ/", "https://www.instagram.com/reel/C8bXvT1J9yQ/", true},
		{"https://instagram.com/p/C8bXvT1J9yQ/", "https://www.instagram.com/p/C8bXvT1J9yQ/", true},
		{"instagram.com/reel/C8bXvT1J9yQ", "https://www.instagram.com/reel/C8bXvT1J9yQ", true},
		{"https://m.instagram.com/reel/C8bXvT1J9yQ/", "https://www.instagram.com/reel/C8bXvT1J9yQ/", true},
		{"https://instagr.am/p/C8bXvT1J9yQ/", "https://www.instagram.com/p/C8bXvT1J9yQ/", true},
		{"https://www.instagram.com/reel/C8bXvT1J9yQ/?igsh=MWZq", "https://www.instagram.com/reel/C8bXvT1J9yQ/?igsh=MWZq", true},
		{"https://www.instagram.com/stories/someuser/123/", "", false},
		{"https://instagram.com.evil.example/reel/C8bXvT1J9yQ/", "", false},
		{"", "", false},
	}
	for _, tt := range tests {
		got, ok := instagramPostURL(tt.in)
		if got != tt.want || ok != tt.ok {
			t.Errorf("instagramPostURL(%q) = (%q, %v), want (%q, %v)", tt.in, got, ok, tt.want, tt.ok)
		}
	}
}

// --- Slot dispatch -------------------------------------------------------

// sigQueue is a DeferredQueuer that signals on every Enqueue, so a test can
// wait for the background goroutine instead of polling.
type sigQueue struct {
	mu   sync.Mutex
	jobs []storage.DeferredJob
	ch   chan struct{}
}

func newSigQueue() *sigQueue { return &sigQueue{ch: make(chan struct{}, 4)} }

func (q *sigQueue) Enqueue(_ context.Context, job storage.DeferredJob) error {
	q.mu.Lock()
	q.jobs = append(q.jobs, job)
	q.mu.Unlock()
	q.ch <- struct{}{}
	return nil
}
func (q *sigQueue) ListByUser(context.Context, int64) ([]storage.DeferredJob, error) {
	return nil, nil
}
func (q *sigQueue) Delete(context.Context, string) error { return nil }
func (q *sigQueue) GarbageCollect(context.Context, time.Time) (int, error) {
	return 0, nil
}

func (q *sigQueue) queued() []storage.DeferredJob {
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]storage.DeferredJob(nil), q.jobs...)
}

func (s *recordMessageSender) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.Messages)
}

// TestDispatchInstagramSlotBusyQueues pins the busy branch: a second link
// arriving while a download runs lands in the deferred queue instead of
// being dropped or downloaded in parallel.
func TestDispatchInstagramSlotBusyQueues(t *testing.T) {
	origDownload := igDownload
	igDownload = func(context.Context, InstagramDownloadOptions, string, string) (string, error) {
		t.Error("a busy slot must not start a second download")
		return "", errors.New("unexpected download")
	}
	defer func() { igDownload = origDownload }()

	igSlot <- struct{}{} // occupy the single download slot
	defer func() { <-igSlot }()

	q := newSigQueue()
	a := &App{sender: &recordMessageSender{}, log: testLogger(), deferredQ: q}

	a.dispatchInstagram(igTestMessage("https://www.instagram.com/reel/C8bXvT1J9yQ/"))

	select {
	case <-q.ch:
	case <-time.After(2 * time.Second):
		t.Fatal("the busy slot must queue the link")
	}
	jobs := q.queued()
	if len(jobs) != 1 || jobs[0].Type != storage.DeferredInstagram {
		t.Fatalf("queued = %+v, want one instagram job", jobs)
	}
}

// TestDispatchInstagramFreeSlotDownloads pins the other branch: with the slot
// free the link goes to the downloader and nothing enters the queue.
func TestDispatchInstagramFreeSlotDownloads(t *testing.T) {
	started := make(chan struct{})
	origDownload := igDownload
	igDownload = func(context.Context, InstagramDownloadOptions, string, string) (string, error) {
		close(started)
		return "", errNoVideo // ends the pipeline with a single decline note
	}
	defer func() { igDownload = origDownload }()

	q := newSigQueue()
	snd := &recordMessageSender{}
	a := &App{sender: snd, log: testLogger(), deferredQ: q}

	a.dispatchInstagram(igTestMessage("https://www.instagram.com/reel/C8bXvT1J9yQ/"))

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("a free slot must start the download")
	}

	deadline := time.Now().Add(2 * time.Second)
	for snd.count() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if n := snd.count(); n != 1 {
		t.Errorf("decline notes = %d, want 1", n)
	}
	if jobs := q.queued(); len(jobs) != 0 {
		t.Errorf("a free slot must not queue, got %+v", jobs)
	}

	select {
	case igSlot <- struct{}{}:
		<-igSlot // the pipeline released the slot
	case <-time.After(2 * time.Second):
		t.Fatal("the download slot was never released")
	}
}

// --- Flush policy --------------------------------------------------------

// TestTryInstagramExport4xxDropsJob pins the flush policy for a permanent
// Telegram rejection: one note, and the job is dropped instead of burning a
// download on every /flush for the rest of the TTL.
func TestTryInstagramExport4xxDropsJob(t *testing.T) {
	origDownload := igDownload
	igDownload = func(_ context.Context, _ InstagramDownloadOptions, _, workDir string) (string, error) {
		p := filepath.Join(workDir, "video.mp4")
		if err := os.WriteFile(p, []byte("fake mp4"), 0o600); err != nil {
			return "", err
		}
		return p, nil
	}
	defer func() { igDownload = origDownload }()

	const url = "https://www.instagram.com/reel/C8bXvT1J9yQ/"

	// 4xx: permanent -> one decline note, job dropped.
	snd := &recYTSender{VideoErr: &telegoapi.Error{ErrorCode: 400, Description: "Bad Request: file is too big"}}
	err := tryInstagramExport(context.Background(), snd, slog.New(slog.DiscardHandler), nil,
		InstagramDownloadOptions{}, -100123, 42, 200, url, "alice", "Alice", "caption")
	if err != nil {
		t.Fatalf("err = %v, want nil so the job is dropped", err)
	}
	if len(snd.Messages) != 1 || !failureCatalogContains(snd.Messages[0].Text) {
		t.Errorf("want one decline note, got %+v", snd.Messages)
	}

	// Transport fault: replayable -> the caller keeps the job.
	snd = &recYTSender{VideoErr: errors.New("fasthttp do request: timeout")}
	err = tryInstagramExport(context.Background(), snd, slog.New(slog.DiscardHandler), nil,
		InstagramDownloadOptions{}, -100123, 42, 200, url, "alice", "Alice", "caption")
	if err == nil {
		t.Fatal("a transport send failure must keep the job")
	}
	if len(snd.Messages) != 0 {
		t.Errorf("a replayable send failure must stay silent, got %+v", snd.Messages)
	}
}

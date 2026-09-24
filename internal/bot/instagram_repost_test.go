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
	"testing"

	"github.com/mymmrac/telego"
	"github.com/mymmrac/telego/telegoapi"

	"github.com/veschin/bidlobot/internal/storage"
)

func TestInstagramDecision(t *testing.T) {
	tests := []struct {
		name string
		msg  *telego.Message
		want bool
	}{
		{
			name: "valid reel link",
			msg:  igTestMessage("https://www.instagram.com/reel/C8bXvT1J9yQ/"),
			want: true,
		},
		{
			name: "reel link with share query",
			msg:  igTestMessage("https://www.instagram.com/reel/C8bXvT1J9yQ/?igsh=MWZq"),
			want: true,
		},
		{
			name: "reels plural form",
			msg:  igTestMessage("https://www.instagram.com/reels/C8bXvT1J9yQ/"),
			want: true,
		},
		{
			name: "tv permalink",
			msg:  igTestMessage("https://www.instagram.com/tv/C8bXvT1J9yQ/"),
			want: true,
		},
		{
			name: "p permalink",
			msg:  igTestMessage("https://www.instagram.com/p/C8bXvT1J9yQ/"),
			want: true,
		},
		{
			name: "m subdomain",
			msg:  igTestMessage("https://m.instagram.com/reel/C8bXvT1J9yQ/"),
			want: true,
		},
		{
			name: "legacy instagr.am host",
			msg:  igTestMessage("https://instagr.am/p/C8bXvT1J9yQ/"),
			want: true,
		},
		{
			name: "scheme-less bare host",
			msg:  igTestMessage("instagram.com/reel/C8bXvT1J9yQ/"),
			want: true,
		},
		{
			name: "link with punctuation around it",
			msg:  igTestMessage("смотри (https://www.instagram.com/reel/C8bXvT1J9yQ/)."),
			want: true,
		},
		{
			name: "non-Instagram URL",
			msg:  igTestMessage("https://www.tiktok.com/@user/video/123"),
			want: false,
		},
		{
			name: "lookalike host",
			msg:  igTestMessage("https://instagram.com.evil.example/reel/C8bXvT1J9yQ/"),
			want: false,
		},
		{
			name: "profile URL without a post",
			msg:  igTestMessage("https://www.instagram.com/someuser/"),
			want: false,
		},
		{
			name: "stories are not permalinks",
			msg:  igTestMessage("https://www.instagram.com/stories/someuser/123456789/"),
			want: false,
		},
		{
			name: "explore link",
			msg:  igTestMessage("https://www.instagram.com/explore/tags/golang/"),
			want: false,
		},
		{
			name: "empty text",
			msg:  igTestMessage(""),
			want: false,
		},
		{
			name: "nil message",
			msg:  nil,
			want: false,
		},
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
			name: "url entity with Instagram host",
			msg: &telego.Message{
				MessageID: 1,
				Chat:      telego.Chat{ID: -100123, Type: telego.ChatTypeSupergroup},
				From:      &telego.User{ID: 200},
				Text:      "check it",
				Entities: []telego.MessageEntity{
					{Type: "url", Offset: 0, Length: 8, URL: "https://www.instagram.com/reel/C8bXvT1J9yQ/"},
				},
			},
			want: true,
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
					{Type: "url", Offset: 0, Length: 4, URL: "https://instagram.com/reels/C8bXvT1J9yQ/"},
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
					{Type: "url", Offset: 0, Length: 35, URL: "https://www.instagram.com/someuser/"},
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

// TestInstagramDecisionStripsTrailingPunctuation pins the scan path: the
// token regex is greedy, so sentence punctuation must be trimmed off
// before the URL is parsed, otherwise yt-dlp would be handed "…/)." .
func TestInstagramDecisionStripsTrailingPunctuation(t *testing.T) {
	msg := igTestMessage("классный рилс https://www.instagram.com/reel/C8bXvT1J9yQ/, ну как?")
	act, got := instagramDecision(msg)
	if !act {
		t.Fatal("expected detection")
	}
	if got != "https://www.instagram.com/reel/C8bXvT1J9yQ/" {
		t.Errorf("URL = %q, want the link without the trailing comma", got)
	}
}

func TestInstagramCaption(t *testing.T) {
	tests := []struct {
		name      string
		username  string
		firstName string
		caption   string
		want      string
	}{
		{
			name:      "username only, no caption",
			username:  "alice",
			firstName: "Alice",
			want:      "\U0001F464 <b>alice</b> писал(а):",
		},
		{
			name:      "no username, no caption",
			firstName: "Alice",
			want:      "\U0001F464 <b>Alice</b> писал(а):",
		},
		{
			name:      "caption is HTML-escaped",
			username:  "alice",
			firstName: "Alice",
			caption:   "5 < 6 & <b>bold</b>",
			want:      "\U0001F464 <b>alice</b> писал(а):\n5 &lt; 6 &amp; &lt;b&gt;bold&lt;/b&gt;",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := instagramCaption(tt.username, tt.firstName, tt.caption); got != tt.want {
				t.Errorf("caption = %q, want %q", got, tt.want)
			}
		})
	}

	// The header must never ping the sender (no @, no tg://user?id=).
	got := instagramCaption("alice", "Alice", "")
	if strings.Contains(got, "@") || strings.Contains(got, "tg://user?id=") {
		t.Errorf("caption must not contain an @ or a user link: %q", got)
	}
}

// --- Download plumbing ---------------------------------------------------

// TestIgPermanentMarker pins the failure classification: the three
// sentences yt-dlp prints for a post that will never yield a video must be
// recognised (so the job is declined instead of retried for 48h), while a
// transport failure must not be.
func TestIgPermanentMarker(t *testing.T) {
	permanent := []string{
		"ERROR: [Instagram] abc: There is no video in this post",
		"ERROR: [Instagram] DFFII3qI503: No video formats found!; please report this issue",
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
		"ERROR: [Instagram] abc: Instagram is rate limiting your requests",
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

// TestDownloadInstagramSuccess verifies the successful path: the file the
// downloader wrote into the work dir is returned.
func TestDownloadInstagramSuccess(t *testing.T) {
	workDir := t.TempDir()
	bin, counter := writeStubYtDlp(t, 0, `printf 'video' > "`+workDir+`/video.mp4"`)

	origBin := igYtDlpBinary
	igYtDlpBinary = bin
	defer func() { igYtDlpBinary = origBin }()

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

	origBin := igYtDlpBinary
	igYtDlpBinary = bin
	defer func() { igYtDlpBinary = origBin }()

	_, err := downloadInstagram(context.Background(), InstagramDownloadOptions{}, "https://www.instagram.com/p/abc/", workDir)
	if !errors.Is(err, errIGNoVideo) {
		t.Fatalf("err = %v, want errIGNoVideo", err)
	}
	if n := countCalls(t, counter); n != 1 {
		t.Errorf("attempts = %d, want 1 (permanent failures must not retry)", n)
	}
}

// TestDownloadInstagramRetriesTransient verifies a transport failure is
// retried (the retry ladder is the whole reason the deferred queue can be
// avoided in the common case).
func TestDownloadInstagramRetriesTransient(t *testing.T) {
	workDir := t.TempDir()
	bin, counter := writeStubYtDlp(t, 1, `echo "ERROR: Unable to download webpage: Could not resolve host" >&2`)

	origBin := igYtDlpBinary
	igYtDlpBinary = bin
	defer func() { igYtDlpBinary = origBin }()

	_, err := downloadInstagram(context.Background(), InstagramDownloadOptions{}, "https://www.instagram.com/reel/C8bXvT1J9yQ/", workDir)
	if err == nil {
		t.Fatal("expected an error")
	}
	if errors.Is(err, errIGNoVideo) {
		t.Fatalf("transport failure must not be classified permanent: %v", err)
	}
	if n := countCalls(t, counter); n != igDownloadAttempts {
		t.Errorf("attempts = %d, want %d", n, igDownloadAttempts)
	}
}

// TestIgYtDlpAttemptPassesEgressOptions pins the argv contract: the proxy
// and cookie jar reach yt-dlp only when configured, and the permalink is
// handed over with a scheme.
func TestIgYtDlpAttemptPassesEgressOptions(t *testing.T) {
	workDir := t.TempDir()
	argsFile := filepath.Join(workDir, "args")
	bin, _ := writeStubYtDlp(t, 0,
		`printf '%s\n' "$@" > "`+argsFile+`"`,
		`printf 'video' > "`+workDir+`/video.mp4"`,
	)

	origBin := igYtDlpBinary
	igYtDlpBinary = bin
	defer func() { igYtDlpBinary = origBin }()

	opts := InstagramDownloadOptions{Proxy: "socks5h://127.0.0.1:1080", Cookies: "/etc/bidlobot/ig-cookies.txt"}
	if _, err := igYtDlpAttempt(context.Background(), opts, "https://www.instagram.com/reel/C8bXvT1J9yQ/", workDir); err != nil {
		t.Fatalf("igYtDlpAttempt: %v", err)
	}

	args, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	got := string(args)
	for _, want := range []string{
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
	if _, err := igYtDlpAttempt(context.Background(), InstagramDownloadOptions{}, "instagram.com/reel/C8bXvT1J9yQ/", workDir); err != nil {
		t.Fatalf("igYtDlpAttempt: %v", err)
	}
	args, err = os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(args), "\nhttps://instagram.com/reel/C8bXvT1J9yQ/\n") {
		t.Errorf("scheme-less link must be normalised; got:\n%s", args)
	}
}

// --- Pipeline ------------------------------------------------------------

// TestProcessInstagramWithSyntheticVideo verifies the full pipeline using
// a synthetic temp file (videoPath != "" bypasses the downloader). The
// repost-first contract: SendVideo, then DeleteMessage of the original.
func TestProcessInstagramWithSyntheticVideo(t *testing.T) {
	dir := t.TempDir()
	videoPath := filepath.Join(dir, "test.mp4")
	if err := os.WriteFile(videoPath, []byte("fake mp4 content"), 0644); err != nil {
		t.Fatal(err)
	}

	snd := &recYTSender{}
	owners := &recOwners{}
	msg := igTestMessage("https://www.instagram.com/reel/C8bXvT1J9yQ/")
	msg.MessageID = 42
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

func TestProcessInstagramVideoTooLargeDeclines(t *testing.T) {
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
	msg := igTestMessage("https://www.instagram.com/reel/C8bXvT1J9yQ/")

	processInstagram(context.Background(), snd, slog.New(slog.DiscardHandler), nil, nil,
		InstagramDownloadOptions{}, msg, "https://www.instagram.com/reel/C8bXvT1J9yQ/", videoPath)

	if len(snd.Videos) != 0 {
		t.Errorf("expected 0 SendVideo, got %d", len(snd.Videos))
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
		return "", errIGNoVideo
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
	msg.MessageID = 42
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
	var p storage.InstagramPayload
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
	if err := os.WriteFile(videoPath, []byte("fake mp4"), 0644); err != nil {
		t.Fatal(err)
	}

	q := &fakeDeferredQueue{}
	snd := &recYTSender{VideoErr: errors.New("fasthttp do request: timeout")}
	msg := igTestMessage("https://www.instagram.com/reel/C8bXvT1J9yQ/")
	msg.MessageID = 42

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
	if err := os.WriteFile(videoPath, []byte("fake mp4"), 0644); err != nil {
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
	if err := os.WriteFile(videoPath, []byte("fake mp4 content"), 0644); err != nil {
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
		return "", errIGNoVideo
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

// igTestMessage builds a telego.Message with the common defaults used by
// the Instagram decision tests. From is a regular, non-bot user.
func igTestMessage(text string) *telego.Message {
	return &telego.Message{
		MessageID: 42,
		Chat:      telego.Chat{ID: -1001234567890, Type: telego.ChatTypeSupergroup},
		From:      &telego.User{ID: 200, Username: "alice", FirstName: "Alice"},
		Text:      text,
	}
}

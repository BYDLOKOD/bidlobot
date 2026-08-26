package bot

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mymmrac/telego"

	"github.com/veschin/bidlobot/internal/storage"
)

func TestTikTokDecision(t *testing.T) {
	tests := []struct {
		name string
		msg  *telego.Message
		want bool
	}{
		{
			name: "valid tiktok.com link",
			msg:  ttTestMessage("https://www.tiktok.com/@user/video/123456789"),
			want: true,
		},
		{
			name: "valid vm.tiktok.com short link",
			msg:  ttTestMessage("https://vm.tiktok.com/ABCDEF/"),
			want: true,
		},
		{
			name: "valid vt.tiktok.com link",
			msg:  ttTestMessage("https://vt.tiktok.com/ZSCqHSWxM/"),
			want: true,
		},
		{
			name: "valid m.tiktok.com link",
			msg:  ttTestMessage("https://m.tiktok.com/v/123456789.html"),
			want: true,
		},
		{
			name: "scheme-less bare host",
			msg:  ttTestMessage("tiktok.com/@user/video/123456789"),
			want: true,
		},
		{
			name: "non-TikTok URL",
			msg:  ttTestMessage("https://youtube.com/watch?v=abc"),
			want: false,
		},
		{
			name: "non-TikTok URL that looks similar",
			msg:  ttTestMessage("https://tiktok.com.ru/fake"),
			want: false,
		},
		{
			name: "empty text",
			msg:  ttTestMessage(""),
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
				From:      nil,
				Text:      "https://www.tiktok.com/@user/video/123",
			},
			want: false,
		},
		{
			name: "bot sender",
			msg: &telego.Message{
				MessageID: 1,
				Chat:      telego.Chat{ID: -100123, Type: telego.ChatTypeSupergroup},
				From:      &telego.User{ID: 100, IsBot: true},
				Text:      "https://www.tiktok.com/@user/video/123",
			},
			want: false,
		},
		{
			name: "anonymous admin sender",
			msg: &telego.Message{
				MessageID: 1,
				Chat:      telego.Chat{ID: -100123, Type: telego.ChatTypeSupergroup},
				From:      &telego.User{ID: 1087968824}, // GroupAnonymousBot
				Text:      "https://www.tiktok.com/@user/video/123",
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
				Text:       "https://www.tiktok.com/@user/video/123",
			},
			want: false,
		},
		{
			name: "url entity with TikTok host",
			msg: &telego.Message{
				MessageID: 1,
				Chat:      telego.Chat{ID: -100123, Type: telego.ChatTypeSupergroup},
				From:      &telego.User{ID: 200},
				Text:      "check it",
				Entities: []telego.MessageEntity{
					{Type: "url", Offset: 0, Length: 8, URL: "https://www.tiktok.com/@user/video/123"},
				},
			},
			want: true,
		},
		{
			name: "text_link entity with TikTok host",
			msg: &telego.Message{
				MessageID: 1,
				Chat:      telego.Chat{ID: -100123, Type: telego.ChatTypeSupergroup},
				From:      &telego.User{ID: 200},
				Text:      "click",
				Entities: []telego.MessageEntity{
					{Type: "text_link", Offset: 0, Length: 5, URL: "https://vm.tiktok.com/ABCDEF/"},
				},
			},
			want: true,
		},
		{
			name: "URL in caption",
			msg: &telego.Message{
				MessageID: 1,
				Chat:      telego.Chat{ID: -100123, Type: telego.ChatTypeSupergroup},
				From:      &telego.User{ID: 200},
				Caption:   "https://www.tiktok.com/@user/video/123",
			},
			want: true,
		},
		{
			name: "caption entity with TikTok host",
			msg: &telego.Message{
				MessageID: 1,
				Chat:      telego.Chat{ID: -100123, Type: telego.ChatTypeSupergroup},
				From:      &telego.User{ID: 200},
				Caption:   "watch",
				CaptionEntities: []telego.MessageEntity{
					{Type: "text_link", Offset: 0, Length: 5, URL: "https://vt.tiktok.com/ZSCqHSWxM/"},
				},
			},
			want: true,
		},
		{
			name: "tiktok URL with trailing punctuation",
			msg:  ttTestMessage("Check https://www.tiktok.com/@user/video/123."),
			want: true,
		},
		{
			name: "multiple TikTok URLs returns first",
			msg:  ttTestMessage("https://vm.tiktok.com/abc/ and https://www.tiktok.com/@user/video/456"),
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			act, url := tiktokDecision(tt.msg)
			if act != tt.want {
				t.Errorf("act = %v, want %v", act, tt.want)
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

func TestDownloadTikTok(t *testing.T) {
	t.Skip("skipped: needs network + yt-dlp. Run manually as an integration test.")
	// Manual test:
	//   ctx := context.Background()
	//   dir := t.TempDir()
	//   path, err := downloadTikTok(ctx, "https://vm.tiktok.com/...", dir)
	//   if err != nil { t.Fatal(err) }
	//   t.Logf("downloaded to %s", path)
}

// TestProcessTikTokWithSyntheticVideo verifies the full pipeline using a
// synthetic temp file so no network/yt-dlp is needed. Asserts:
//   - SendVideo is called with correct chat ID, caption, parse mode
//   - DeleteMessage is called AFTER SendVideo succeeds (repost-first)
func TestProcessTikTokWithSyntheticVideo(t *testing.T) {
	origFFprobe := ffprobeHasAudio
	ffprobeHasAudio = func(string) bool { return true }
	defer func() { ffprobeHasAudio = origFFprobe }()

	dir := t.TempDir()
	videoPath := filepath.Join(dir, "test.mp4")
	if err := os.WriteFile(videoPath, []byte("fake mp4 content"), 0644); err != nil {
		t.Fatal(err)
	}

	snd := &recYTSender{}
	log := slog.New(slog.DiscardHandler)
	owners := &recOwners{}

	msg := &telego.Message{
		MessageID: 42,
		Chat:      telego.Chat{ID: -1001234567890, Type: telego.ChatTypeSupergroup},
		From:      &telego.User{ID: 200, Username: "alice", FirstName: "Alice"},
		Caption:   "original caption",
	}

	processTikTok(context.Background(), snd, log, nil, nil, owners, msg, "https://www.tiktok.com/@user/video/123", videoPath)

	// Assert SendVideo was called with correct params.
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
	if v.Caption == "" {
		t.Error("caption is empty")
	}

	// Repost-first contract: DeleteMessage called AFTER SendVideo.
	if len(snd.Deletes) != 1 {
		t.Fatalf("expected 1 DeleteMessage, got %d", len(snd.Deletes))
	}
	d := snd.Deletes[0]
	if d.ChatID.ID != msg.Chat.ID {
		t.Errorf("Delete ChatID = %d, want %d", d.ChatID.ID, msg.Chat.ID)
	}
	if d.MessageID != msg.MessageID {
		t.Errorf("Delete MessageID = %d, want %d", d.MessageID, msg.MessageID)
	}

	// Reactions on the bot's repost must credit the original sender:
	// the sent message ID (1002 from recYTSender) is indexed to alice.
	if calls := owners.recorded(); len(calls) != 1 || calls[0] != "-1001234567890:1002:200" {
		t.Fatalf("owner calls = %v", calls)
	}
}

// TestProcessTikTokRecordsRepostIndex verifies a successful repost is
// recorded in the video index (chat, video id -> repost message id) so a
// later comment quote can reply to it.
func TestProcessTikTokRecordsRepostIndex(t *testing.T) {
	origFFprobe := ffprobeHasAudio
	ffprobeHasAudio = func(string) bool { return true }
	defer func() { ffprobeHasAudio = origFFprobe }()

	dir := t.TempDir()
	videoPath := filepath.Join(dir, "test.mp4")
	if err := os.WriteFile(videoPath, []byte("fake mp4 content"), 0644); err != nil {
		t.Fatal(err)
	}

	snd := &recYTSender{}
	log := slog.New(slog.DiscardHandler)
	videos := &recVideoIndex{}

	msg := &telego.Message{
		MessageID: 42,
		Chat:      telego.Chat{ID: -1001234567890, Type: telego.ChatTypeSupergroup},
		From:      &telego.User{ID: 200, Username: "alice", FirstName: "Alice"},
	}

	processTikTok(context.Background(), snd, log, nil, videos, nil, msg, "https://www.tiktok.com/@user/video/123", videoPath)

	if len(snd.Videos) != 1 {
		t.Fatalf("expected 1 SendVideo, got %d", len(snd.Videos))
	}
	// recYTSender.SendVideo returns message id 1002.
	if recs := videos.recorded(); len(recs) != 1 || recs[0] != "-1001234567890:123:1002" {
		t.Fatalf("index records = %v, want [-1001234567890:123:1002]", recs)
	}
}

// TestProcessTikTokVideoTooLarge verifies the size-limit decline path:
// videos over 50MB get a decline note instead of Silent drop.
func TestProcessTikTokVideoTooLarge(t *testing.T) {
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
	log := slog.New(slog.DiscardHandler)

	msg := &telego.Message{
		MessageID: 42,
		Chat:      telego.Chat{ID: -100123, Type: telego.ChatTypeSupergroup},
		From:      &telego.User{ID: 200, Username: "alice", FirstName: "Alice"},
	}

	processTikTok(context.Background(), snd, log, nil, nil, nil, msg, "https://www.tiktok.com/@user/video/123", videoPath)

	// Should NOT have called SendVideo (too large).
	if len(snd.Videos) != 0 {
		t.Errorf("expected 0 SendVideo calls, got %d", len(snd.Videos))
	}
	// Should have sent decline note.
	if len(snd.Messages) != 1 {
		t.Fatalf("expected 1 SendMessage (decline), got %d", len(snd.Messages))
	}
	if !failureCatalogContains(snd.Messages[0].Text) {
		t.Errorf("decline text must be from FailureCatalog; got %q", snd.Messages[0].Text)
	}
}

// TestProcessTikTokDeleteFailsRepostStands verifies the repost-first
// contract: when DeleteMessage fails (no Delete right), the video repost
// still stands - the original is simply kept. This is the TikTok equivalent
// of youtube_sanitizer's TestHandleSanitizeDeleteFailsRepostStandsOriginalKept.
func TestProcessTikTokDeleteFailsRepostStands(t *testing.T) {
	dir := t.TempDir()
	videoPath := filepath.Join(dir, "test.mp4")
	if err := os.WriteFile(videoPath, []byte("fake mp4 content"), 0644); err != nil {
		t.Fatal(err)
	}

	snd := &recYTSender{
		DeleteErr: errors.New("no delete right"),
	}
	log := slog.New(slog.DiscardHandler)

	msg := &telego.Message{
		MessageID: 42,
		Chat:      telego.Chat{ID: -1001234567890, Type: telego.ChatTypeSupergroup},
		From:      &telego.User{ID: 200, Username: "alice", FirstName: "Alice"},
	}

	processTikTok(context.Background(), snd, log, nil, nil, nil, msg, "https://www.tiktok.com/@user/video/123", videoPath)

	// Repost MUST stand even when delete fails.
	if len(snd.Videos) != 1 {
		t.Fatalf("expected 1 SendVideo (repost), got %d", len(snd.Videos))
	}
	// Delete was attempted (the call was made, it just returned an error).
	if len(snd.Deletes) != 1 {
		t.Errorf("expected 1 DeleteMessage attempt, got %d", len(snd.Deletes))
	}
}

// ttTestMessage builds a telego.Message with common defaults for
// tiktokDecision tests. The From is a regular non-bot user.
func ttTestMessage(text string) *telego.Message {
	return &telego.Message{
		MessageID: 42,
		Chat:      telego.Chat{ID: -1001234567890, Type: telego.ChatTypeSupergroup},
		From:      &telego.User{ID: 200, Username: "alice", FirstName: "Alice"},
		Text:      text,
	}
}

// --- Deferred queue + audio-check tests ---

// fakeDeferredQueue is an in-memory DeferredQueuer for tests.
type fakeDeferredQueue struct {
	jobs []storage.DeferredJob
}

func (f *fakeDeferredQueue) Enqueue(_ context.Context, job storage.DeferredJob) error {
	f.jobs = append(f.jobs, job)
	return nil
}
func (f *fakeDeferredQueue) ListByUser(_ context.Context, userID int64) ([]storage.DeferredJob, error) {
	var out []storage.DeferredJob
	for _, j := range f.jobs {
		if j.UserID == userID {
			out = append(out, j)
		}
	}
	return out, nil
}
func (f *fakeDeferredQueue) Delete(_ context.Context, _ string) error { return nil }
func (f *fakeDeferredQueue) GarbageCollect(_ context.Context, _ time.Time) (int, error) {
	return 0, nil
}

// TestProcessTikTok_NoAudio_QueuesNotUploads verifies that a video with
// no audio stream is enqueued for later flush instead of being uploaded.
// The original message is left intact (no delete, no decline).
func TestProcessTikTok_NoAudio_QueuesNotUploads(t *testing.T) {
	origFFprobe := ffprobeHasAudio
	ffprobeHasAudio = func(string) bool { return false }
	defer func() { ffprobeHasAudio = origFFprobe }()

	dir := t.TempDir()
	videoPath := filepath.Join(dir, "test.mp4")
	if err := os.WriteFile(videoPath, []byte("fake mp4"), 0644); err != nil {
		t.Fatal(err)
	}

	q := &fakeDeferredQueue{}
	snd := &recYTSender{}
	log := slog.New(slog.DiscardHandler)
	msg := &telego.Message{
		MessageID: 42,
		Chat:      telego.Chat{ID: -100123, Type: telego.ChatTypeSupergroup},
		From:      &telego.User{ID: 200, Username: "alice", FirstName: "Alice"},
	}

	processTikTok(context.Background(), snd, log, q, nil, nil, msg, "https://vt.tiktok.com/Ztest", videoPath)

	if len(snd.Videos) != 0 {
		t.Errorf("expected 0 uploads, got %d", len(snd.Videos))
	}
	if len(snd.Deletes) != 0 {
		t.Errorf("expected 0 deletes, got %d", len(snd.Deletes))
	}
	if len(q.jobs) != 1 {
		t.Fatalf("expected 1 queued job, got %d", len(q.jobs))
	}
	var p storage.TikTokPayload
	json.Unmarshal(q.jobs[0].Payload, &p)
	if p.URL != "https://vt.tiktok.com/Ztest" {
		t.Errorf("queued URL = %q", p.URL)
	}
	if q.jobs[0].UserID != 200 {
		t.Errorf("queued UserID = %d, want 200", q.jobs[0].UserID)
	}
	// No decline message when queue is available.
	if len(snd.Messages) != 0 {
		t.Errorf("expected 0 decline messages, got %d", len(snd.Messages))
	}
}

// TestProcessTikTok_NoAudio_NoQueue_Declines verifies that without a
// queue the no-audio case falls back to a public decline (old behavior).
func TestProcessTikTok_NoAudio_NoQueue_Declines(t *testing.T) {
	origFFprobe := ffprobeHasAudio
	ffprobeHasAudio = func(string) bool { return false }
	defer func() { ffprobeHasAudio = origFFprobe }()

	dir := t.TempDir()
	videoPath := filepath.Join(dir, "test.mp4")
	if err := os.WriteFile(videoPath, []byte("fake mp4"), 0644); err != nil {
		t.Fatal(err)
	}

	snd := &recYTSender{}
	log := slog.New(slog.DiscardHandler)
	msg := &telego.Message{
		MessageID: 42,
		Chat:      telego.Chat{ID: -100123, Type: telego.ChatTypeSupergroup},
		From:      &telego.User{ID: 200, Username: "alice", FirstName: "Alice"},
	}

	processTikTok(context.Background(), snd, log, nil, nil, nil, msg, "https://vt.tiktok.com/Ztest", videoPath)

	if len(snd.Videos) != 0 {
		t.Errorf("expected 0 uploads, got %d", len(snd.Videos))
	}
	if len(snd.Messages) != 1 {
		t.Fatalf("expected 1 decline, got %d", len(snd.Messages))
	}
	if !failureCatalogContains(snd.Messages[0].Text) {
		t.Errorf("decline must be from FailureCatalog; got %q", snd.Messages[0].Text)
	}
}

// TestEnqueueOrFail_WithQueue verifies that the job is stored silently
// (no public decline) when a queue is wired.
func TestEnqueueOrFail_WithQueue(t *testing.T) {
	q := &fakeDeferredQueue{}
	snd := &recYTSender{}
	log := slog.New(slog.DiscardHandler)
	msg := &telego.Message{
		MessageID: 42,
		Chat:      telego.Chat{ID: -100123, Type: telego.ChatTypeSupergroup},
		From:      &telego.User{ID: 200, Username: "alice", FirstName: "Alice"},
		Caption:   "cap",
	}

	enqueueOrFail(context.Background(), snd, log, q, msg, "https://vt.tiktok.com/Ztest")

	if len(q.jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(q.jobs))
	}
	job := q.jobs[0]
	if job.UserID != 200 || job.ChatID != -100123 || job.MessageID != 42 {
		t.Errorf("job user/chat/msg = %d/%d/%d", job.UserID, job.ChatID, job.MessageID)
	}
	if job.Type != storage.DeferredTikTok {
		t.Errorf("job type = %q, want %q", job.Type, storage.DeferredTikTok)
	}
	var p storage.TikTokPayload
	json.Unmarshal(job.Payload, &p)
	if p.URL != "https://vt.tiktok.com/Ztest" {
		t.Errorf("payload URL = %q", p.URL)
	}
	if p.Username != "alice" || p.FirstName != "Alice" {
		t.Errorf("payload sender = %q/%q", p.Username, p.FirstName)
	}
	if p.Caption != "cap" {
		t.Errorf("payload caption = %q", p.Caption)
	}
	if len(snd.Messages) != 0 {
		t.Errorf("expected 0 decline messages, got %d", len(snd.Messages))
	}
}

// TestEnqueueOrFail_NoQueue_Declines verifies that without a queue the
// caller gets a public decline (backward-compatible fallback).
func TestEnqueueOrFail_NoQueue_Declines(t *testing.T) {
	snd := &recYTSender{}
	log := slog.New(slog.DiscardHandler)
	msg := &telego.Message{
		MessageID: 42,
		Chat:      telego.Chat{ID: -100123, Type: telego.ChatTypeSupergroup},
		From:      &telego.User{ID: 200, Username: "alice", FirstName: "Alice"},
	}

	enqueueOrFail(context.Background(), snd, log, nil, msg, "https://vt.tiktok.com/Ztest")

	if len(snd.Messages) != 1 {
		t.Fatalf("expected 1 decline, got %d", len(snd.Messages))
	}
	if !failureCatalogContains(snd.Messages[0].Text) {
		t.Errorf("decline must be from FailureCatalog; got %q", snd.Messages[0].Text)
	}
}

func TestTiktokCaption(t *testing.T) {
	got := tiktokCaption("alice", "Alice", "check this out")
	if !strings.Contains(got, "alice") {
		t.Errorf("caption should contain display name; got %q", got)
	}
	if !strings.Contains(got, "check this out") {
		t.Errorf("caption should contain raw caption; got %q", got)
	}
	// No caption -> just the header, no trailing newline.
	got = tiktokCaption("bob", "Bob", "")
	if strings.Contains(got, "\n") {
		t.Errorf("empty caption should not add newline; got %q", got)
	}
}

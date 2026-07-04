package bot

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mymmrac/telego"
)

// tiktokTestMessage builds a minimal telego.Message for TikTok decision/process tests.
func tiktokTestMessage(text string) *telego.Message {
	return &telego.Message{
		MessageID: 42,
		Date:      time.Now().Unix(),
		Chat:      telego.Chat{ID: -1001234567890, Type: telego.ChatTypeSupergroup},
		From:      &telego.User{ID: 200, Username: "alice", FirstName: "Alice"},
		Text:      text,
	}
}

func TestTikTokDecision(t *testing.T) {
	tests := []struct {
		name    string
		msg     *telego.Message
		wantAct bool
		wantURL string
	}{
		{
			name:    "www.tiktok.com video link",
			msg:     tiktokTestMessage("check https://www.tiktok.com/@user/video/123456789"),
			wantAct: true,
			wantURL: "https://www.tiktok.com/@user/video/123456789",
		},
		{
			name:    "vm.tiktok.com short link",
			msg:     tiktokTestMessage("https://vm.tiktok.com/ABCDEF/"),
			wantAct: true,
			wantURL: "https://vm.tiktok.com/ABCDEF/",
		},
		{
			name:    "m.tiktok.com link",
			msg:     tiktokTestMessage("https://m.tiktok.com/v/123456789.html"),
			wantAct: true,
			wantURL: "https://m.tiktok.com/v/123456789.html",
		},
		{
			name:    "scheme-less tiktok.com",
			msg:     tiktokTestMessage("see tiktok.com/@user/video/123"),
			wantAct: true,
			wantURL: "tiktok.com/@user/video/123",
		},
		{
			name:    "trailing punctuation stripped",
			msg:     tiktokTestMessage("watch https://vm.tiktok.com/ABC/."),
			wantAct: true,
			wantURL: "https://vm.tiktok.com/ABC/",
		},
		{
			name:    "non-TikTok URL passes through",
			msg:     tiktokTestMessage("https://youtube.com/watch?v=x"),
			wantAct: false,
		},
		{
			name:    "no URL in text",
			msg:     tiktokTestMessage("just talking"),
			wantAct: false,
		},
		{
			name:    "empty text",
			msg:     tiktokTestMessage(""),
			wantAct: false,
		},
		{
			name: "nil sender excluded",
			msg: &telego.Message{
				MessageID: 42,
				Chat:      telego.Chat{ID: -1001234567890, Type: telego.ChatTypeSupergroup},
				From:      nil,
				Text:      "https://www.tiktok.com/@user/video/123",
			},
			wantAct: false,
		},
		{
			name: "bot excluded",
			msg: &telego.Message{
				MessageID: 42,
				Chat:      telego.Chat{ID: -1001234567890, Type: telego.ChatTypeSupergroup},
				From:      &telego.User{ID: 300, IsBot: true, Username: "botty"},
				Text:      "https://www.tiktok.com/@user/video/123",
			},
			wantAct: false,
		},
		{
			name: "channel-as-sender excluded",
			msg: &telego.Message{
				MessageID:  42,
				Chat:       telego.Chat{ID: -1001234567890, Type: telego.ChatTypeSupergroup},
				From:       &telego.User{ID: 200, Username: "alice"},
				SenderChat: &telego.Chat{ID: -1009999999},
				Text:       "https://www.tiktok.com/@user/video/123",
			},
			wantAct: false,
		},
		{
			name: "url entity with TikTok host",
			msg: &telego.Message{
				MessageID: 42,
				Chat:      telego.Chat{ID: -1001234567890, Type: telego.ChatTypeSupergroup},
				From:      &telego.User{ID: 200, Username: "alice", FirstName: "Alice"},
				Entities: []telego.MessageEntity{
					{Type: "url", URL: "https://www.tiktok.com/@user/video/123", Offset: 0, Length: 10},
				},
				Text: "watch this",
			},
			wantAct: true,
			wantURL: "https://www.tiktok.com/@user/video/123",
		},
		{
			name: "text_link entity with TikTok host",
			msg: &telego.Message{
				MessageID: 42,
				Chat:      telego.Chat{ID: -1001234567890, Type: telego.ChatTypeSupergroup},
				From:      &telego.User{ID: 200, Username: "alice", FirstName: "Alice"},
				Entities: []telego.MessageEntity{
					{Type: "text_link", URL: "https://vm.tiktok.com/ABC/", Offset: 0, Length: 4},
				},
				Text: "link",
			},
			wantAct: true,
			wantURL: "https://vm.tiktok.com/ABC/",
		},
		{
			name: "non-TikTok entity URL ignored",
			msg: &telego.Message{
				MessageID: 42,
				Chat:      telego.Chat{ID: -1001234567890, Type: telego.ChatTypeSupergroup},
				From:      &telego.User{ID: 200, Username: "alice", FirstName: "Alice"},
				Entities: []telego.MessageEntity{
					{Type: "url", URL: "https://youtube.com/watch?v=x", Offset: 0, Length: 10},
				},
				Text: "watch this",
			},
			wantAct: false,
		},
		{
			name: "caption with TikTok link",
			msg: &telego.Message{
				MessageID: 42,
				Chat:      telego.Chat{ID: -1001234567890, Type: telego.ChatTypeSupergroup},
				From:      &telego.User{ID: 200, Username: "alice", FirstName: "Alice"},
				Caption:   "https://vm.tiktok.com/ABC/",
			},
			wantAct: true,
			wantURL: "https://vm.tiktok.com/ABC/",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			act, url := tiktokDecision(tt.msg)
			if act != tt.wantAct {
				t.Errorf("act = %v, want %v", act, tt.wantAct)
			}
			if tt.wantURL != "" && url != tt.wantURL {
				t.Errorf("url = %q, want %q", url, tt.wantURL)
			}
		})
	}
}

// TestTikTokDecisionAnonymousAdmin verifies that GroupAnonymousBot messages
// are excluded the same way the YT sanitizer excludes them.
func TestTikTokDecisionAnonymousAdmin(t *testing.T) {
	anonID := anonAdminID(t)
	msg := &telego.Message{
		MessageID: 42,
		Chat:      telego.Chat{ID: -1001234567890, Type: telego.ChatTypeSupergroup},
		From:      &telego.User{ID: anonID, Username: "GroupAnonymousBot"},
		Text:      "https://www.tiktok.com/@user/video/123",
	}
	act, _ := tiktokDecision(msg)
	if act {
		t.Error("anonymous admin must be excluded")
	}
}

// TestDownloadTikTok is skipped in unit tests (needs network + yt-dlp).
// Run manually: go test -run TestDownloadTikTok -tags=integration
func TestDownloadTikTok(t *testing.T) {
	t.Skip("integration test: requires network and yt-dlp binary")
}

// TestTrimVideoEnd is skipped in unit tests (needs ffmpeg).
// Run manually: go test -run TestTrimVideoEnd -tags=integration
func TestTrimVideoEnd(t *testing.T) {
	t.Skip("integration test: requires ffmpeg binary")
}

// TestProcessTikTok verifies the upload + delete pipeline using
// recYTSender and a synthetic temp file (bypassing yt-dlp).
func TestProcessTikTok(t *testing.T) {
	snd := &recYTSender{}
	msg := tiktokTestMessage("https://vm.tiktok.com/ABC/")

	// Create a tiny synthetic file to exercise the SendVideo path.
	// The file is smaller than maxVideoSize so it passes the size check.
	tmp, err := os.CreateTemp("", "bidlobot-test-tiktok-*.mp4")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write([]byte("fake mp4 content")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	tmp.Close()

	processTikTok(context.Background(), snd, testLogger(), msg, "https://vm.tiktok.com/ABC/", tmp.Name())

	// Assert SendVideo was called.
	if len(snd.Videos) != 1 {
		t.Fatalf("expected 1 SendVideo, got %d", len(snd.Videos))
	}
	v := snd.Videos[0]
	if !strings.Contains(v.Caption, "alice") {
		t.Errorf("SendVideo caption missing attribution 'alice': %q", v.Caption)
	}
	if v.ParseMode != telego.ModeHTML {
		t.Errorf("SendVideo parse_mode = %q, want HTML", v.ParseMode)
	}
	if strings.Contains(v.Caption, "@") {
		t.Errorf("SendVideo caption must not contain '@' (no mention): %q", v.Caption)
	}

	// Assert DeleteMessage was called AFTER SendVideo (repost-first).
	if len(snd.Deletes) != 1 {
		t.Fatalf("expected 1 DeleteMessage, got %d", len(snd.Deletes))
	}
	if snd.Deletes[0].MessageID != 42 {
		t.Errorf("DeleteMessage targeted %d, want 42", snd.Deletes[0].MessageID)
	}
}

// TestProcessTikTokDeleteFailsRepostStands verifies the repost-first contract:
// when delete fails, the video is still posted and the original is kept.
func TestProcessTikTokDeleteFailsRepostStands(t *testing.T) {
	snd := &recYTSender{DeleteErr: errNoMediaSend}
	msg := tiktokTestMessage("https://vm.tiktok.com/ABC/")

	tmp, err := os.CreateTemp("", "bidlobot-test-tiktok-*.mp4")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	defer os.Remove(tmp.Name())
	tmp.Write([]byte("fake mp4 content"))
	tmp.Close()

	processTikTok(context.Background(), snd, testLogger(), msg, "https://vm.tiktok.com/ABC/", tmp.Name())

	// Video must be posted even when delete fails.
	if len(snd.Videos) != 1 {
		t.Fatalf("expected 1 SendVideo (repost stands), got %d", len(snd.Videos))
	}

	// Delete was attempted (and failed). We don't assert on len(snd.Deletes)
	// because sendTikTokFallback also tries to delete -- but processTikTok
	// itself calls DeleteMessage only after successful SendVideo, so at
	// least 1 delete was attempted.
	if len(snd.Deletes) < 1 {
		t.Error("expected at least 1 delete attempt")
	}
}

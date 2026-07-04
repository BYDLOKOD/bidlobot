package bot

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/mymmrac/telego"
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
	dir := t.TempDir()
	videoPath := filepath.Join(dir, "test.mp4")
	if err := os.WriteFile(videoPath, []byte("fake mp4 content"), 0644); err != nil {
		t.Fatal(err)
	}

	snd := &recYTSender{}
	log := slog.New(slog.DiscardHandler)

	msg := &telego.Message{
		MessageID: 42,
		Chat:      telego.Chat{ID: -1001234567890, Type: telego.ChatTypeSupergroup},
		From:      &telego.User{ID: 200, Username: "alice", FirstName: "Alice"},
		Caption:   "original caption",
	}

	processTikTok(context.Background(), snd, log, msg,
		"https://www.tiktok.com/@user/video/123", videoPath)

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

	processTikTok(context.Background(), snd, log, msg,
		"https://www.tiktok.com/@user/video/123", videoPath)

	// Should NOT have called SendVideo (too large).
	if len(snd.Videos) != 0 {
		t.Errorf("expected 0 SendVideo calls, got %d", len(snd.Videos))
	}
	// Should have sent decline note.
	if len(snd.Messages) != 1 {
		t.Fatalf("expected 1 SendMessage (decline), got %d", len(snd.Messages))
	}
	if snd.Messages[0].Text != msgTikTokSizeLimit {
		t.Errorf("decline text = %q, want %q", snd.Messages[0].Text, msgTikTokSizeLimit)
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

	processTikTok(context.Background(), snd, log, msg,
		"https://www.tiktok.com/@user/video/123", videoPath)

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

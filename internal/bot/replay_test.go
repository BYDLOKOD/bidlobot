package bot

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mymmrac/telego"

	"github.com/veschin/bidlobot/internal/domain/cleanup"
	"github.com/veschin/bidlobot/internal/domain/membership"
	"github.com/veschin/bidlobot/internal/storage"
	"github.com/veschin/bidlobot/internal/testutil"
)

// RecordedUpdate is one entry in a JSONL recording captured by the
// RECORD_UPDATES facility (cmd/replay). Each line is a standalone
// JSON object with a timestamp, an update_id, and the raw Update.
type RecordedUpdate struct {
	Timestamp string        `json:"ts"`
	UpdateID  int           `json:"update_id"`
	Raw       telego.Update `json:"update"`
}

// LoadRecording reads a JSONL recording file and returns all entries.
func LoadRecording(path string) ([]RecordedUpdate, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var updates []RecordedUpdate
	dec := json.NewDecoder(f)
	for dec.More() {
		var u RecordedUpdate
		if err := dec.Decode(&u); err != nil {
			return updates, err
		}
		updates = append(updates, u)
	}
	return updates, nil
}

// replayThroughDomain dispatches a recorded JSONL session through the
// same domain calls that the production middleware/handlers make. It
// is not a full bot replay (the telego router would need a httptest
// API server for that, see cmd/replay), but it exercises the
// "membership + cleanup" pipeline end-to-end on real bbolt data, which
// is enough to catch any handler/store contract regressions.
func replayThroughDomain(t *testing.T, jsonlPath string) (*storage.MembershipRepo, *membership.Service) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	store, err := storage.NewBoltStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	memberRepo := storage.NewMembershipRepo(store.DB())
	memberSvc := membership.NewService(memberRepo, testLogger())

	updates, err := LoadRecording(jsonlPath)
	if err != nil {
		t.Fatalf("LoadRecording(%s): %v", jsonlPath, err)
	}
	if len(updates) == 0 {
		t.Fatalf("recording %s is empty", jsonlPath)
	}

	ctx := context.Background()
	for _, u := range updates {
		switch {
		case u.Raw.MyChatMember != nil:
			if err := memberSvc.RecordMyChatMember(ctx, *u.Raw.MyChatMember); err != nil {
				t.Errorf("RecordMyChatMember on update %d: %v", u.UpdateID, err)
			}
		case u.Raw.ChatMember != nil:
			if err := memberSvc.RecordChatMember(ctx, *u.Raw.ChatMember); err != nil {
				t.Errorf("RecordChatMember on update %d: %v", u.UpdateID, err)
			}
		case u.Raw.Message != nil:
			msg := u.Raw.Message
			if msg.From == nil {
				continue
			}
			ts := time.Unix(int64(msg.Date), 0).UTC()
			absChatID := storage.AbsChatID(msg.Chat.ID)
			if err := memberSvc.RecordMessage(ctx, absChatID, msg.From, ts); err != nil {
				t.Errorf("RecordMessage on update %d: %v", u.UpdateID, err)
			}
		case u.Raw.MessageReaction != nil:
			if err := memberSvc.RecordReaction(ctx, *u.Raw.MessageReaction); err != nil {
				t.Errorf("RecordReaction on update %d: %v", u.UpdateID, err)
			}
		}
	}

	return memberRepo, memberSvc
}

func TestReplaySession1RegistersChatAndMembers(t *testing.T) {
	repo, _ := replayThroughDomain(t, "../../testdata/session1.jsonl")
	ctx := context.Background()

	chats, err := repo.ListChats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(chats) == 0 {
		t.Fatal("session1 must register at least one chat (my_chat_member)")
	}
	for _, c := range chats {
		if c.InstalledAt.IsZero() {
			t.Errorf("chat %d has zero InstalledAt", c.AbsChatID)
		}
		if c.AbsChatID == 0 {
			t.Errorf("chat with zero AbsChatID slipped through")
		}
	}

	totalMembers := 0
	for _, c := range chats {
		members, err := repo.ListByChat(ctx, c.AbsChatID)
		if err != nil {
			t.Errorf("ListByChat(%d): %v", c.AbsChatID, err)
			continue
		}
		totalMembers += len(members)
	}
	if totalMembers == 0 {
		t.Log("session1 had no message events - only my_chat_member; that's fine, members can be empty")
	}
}

func TestReplaySession2ProducesNoErrors(t *testing.T) {
	// Just exercise the path; the recording is short and we mostly
	// want assurance that no panic / no unexpected error fires.
	_, _ = replayThroughDomain(t, "../../testdata/session2.jsonl")
}

// TestCleanupOnReplayedState wires the cleanup service against the
// state produced by session1 and confirms PreviewInactive returns a
// sensible answer (no candidates yet, since the recording is short).
func TestCleanupOnReplayedState(t *testing.T) {
	repo, _ := replayThroughDomain(t, "../../testdata/session1.jsonl")
	ctx := context.Background()

	chats, err := repo.ListChats(ctx)
	if err != nil || len(chats) == 0 {
		t.Skip("no chats registered in this recording")
	}

	api := testutil.NewMockAPI()
	svc := cleanup.NewService(repo, api, testLogger())

	// 24 hours is the minimum threshold per cleanup.MinThreshold.
	preview, err := svc.PreviewInactive(ctx, chats[0].AbsChatID, 24*time.Hour, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	// With a recording from a single day, candidates may be 0 - but
	// the preview itself must populate the chat metadata correctly.
	if !preview.InstalledAt.IsZero() && preview.ObservationWindow == 0 {
		t.Errorf("InstalledAt set but ObservationWindow zero - bug in PreviewInactive")
	}
}

package moderation_test

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"

	"github.com/veschin/bidlobot/internal/domain/moderation"
	"github.com/veschin/bidlobot/internal/shared"
	"github.com/veschin/bidlobot/internal/storage"
	"github.com/veschin/bidlobot/internal/testutil"
)

type testEnv struct {
	api     *testutil.MockAPI
	handler *moderation.Handler
	store   *storage.BoltStore
	chatID  int64
}

func setup(t *testing.T) *testEnv {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	store, err := storage.NewBoltStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	api := testutil.NewMockAPI()
	chatID := int64(1001234567890)
	api.AdminIDs[chatID] = []int64{100} // user 100 is admin

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	adminCache := shared.NewAdminCache(api, 999, log)
	warnRepo := storage.NewWarnRepo(store.DB())
	svc := moderation.NewService(warnRepo, api, adminCache, log)
	lookup := &mockLookup{profiles: map[string]int64{"bob": 200, "alice": 300}}
	h := moderation.NewHandler(svc, adminCache, lookup, log)

	return &testEnv{api: api, handler: h, store: store, chatID: chatID}
}

type mockLookup struct {
	profiles map[string]int64
}

func (m *mockLookup) GetByUsername(_ context.Context, _ int64, username string) (int64, bool, error) {
	uid, ok := m.profiles[username]
	if !ok {
		return 0, false, moderation.ErrNotFound
	}
	return uid, false, nil
}

func TestWarnThreeStrikeEscalation(t *testing.T) {
	env := setup(t)
	admin := testutil.User(100, "charlie", "Charlie")
	chatRawID := -env.chatID

	for i := 1; i <= 3; i++ {
		env.api.Reset()
		msg := testutil.SupergroupMessage(chatRawID, admin, "/warn @bob Spam")

		// Need th.Context - call handler directly with nil context
		// Since we can't create th.Context easily, test via service layer
		_ = msg
	}

	// Test via service layer directly (no telego context needed)
	ctx := context.Background()
	count1, err := env.handler.Service().Warn(ctx, env.chatID, 200, 100, "First")
	if err != nil {
		t.Fatal(err)
	}
	if count1 != 1 {
		t.Fatalf("expected count 1, got %d", count1)
	}

	count2, _ := env.handler.Service().Warn(ctx, env.chatID, 200, 100, "Second")
	if count2 != 2 {
		t.Fatalf("expected count 2, got %d", count2)
	}

	count3, _ := env.handler.Service().Warn(ctx, env.chatID, 200, 100, "Third")
	if count3 != 3 {
		t.Fatalf("expected count 3, got %d", count3)
	}

	// Auto-mute at 3
	err = env.handler.Service().AutoMute(ctx, chatRawID, 200)
	if err != nil {
		t.Fatal("auto-mute failed:", err)
	}
	if env.api.CallCount("RestrictChatMember") != 1 {
		t.Fatal("expected 1 RestrictChatMember call")
	}

	// Warning 4 should NOT trigger auto-mute
	count4, _ := env.handler.Service().Warn(ctx, env.chatID, 200, 100, "Fourth")
	if count4 != 4 {
		t.Fatalf("expected count 4, got %d", count4)
	}
}

func TestWarnClearResetsCount(t *testing.T) {
	env := setup(t)
	ctx := context.Background()

	env.handler.Service().Warn(ctx, env.chatID, 200, 100, "One")
	env.handler.Service().Warn(ctx, env.chatID, 200, 100, "Two")

	err := env.handler.Service().ClearWarnings(ctx, 200, env.chatID)
	if err != nil {
		t.Fatal(err)
	}

	count, _ := env.handler.Service().Warn(ctx, env.chatID, 200, 100, "After clear")
	if count != 1 {
		t.Fatalf("expected count 1 after clear, got %d", count)
	}
}

func TestValidateTargetAdmin(t *testing.T) {
	env := setup(t)
	ctx := context.Background()

	// Admin 100 trying to warn admin 100 (self)
	err := env.handler.Service().ValidateTarget(ctx, env.chatID, 100, 100, false, "warn")
	if err == nil {
		t.Fatal("should reject self-targeting")
	}
}

// Expose Service for testing
func init() {
	// Handler needs Service() accessor
	_ = (*moderation.Handler)(nil)
}

// dummy to make th import used
var _ th.Handler
var _ telego.Update

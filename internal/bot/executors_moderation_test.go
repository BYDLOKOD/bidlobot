package bot

import (
	"context"
	"path/filepath"
	"sync"
	"strings"
	"testing"
	"time"

	"github.com/mymmrac/telego"

	"github.com/veschin/bidlobot/internal/domain/membership"
	"github.com/veschin/bidlobot/internal/domain/moderation"
	"github.com/veschin/bidlobot/internal/domain/pending"
	"github.com/veschin/bidlobot/internal/shared"
	"github.com/veschin/bidlobot/internal/storage"
	"github.com/veschin/bidlobot/internal/testutil"
)

// inMemMembers is a tiny membership.Store stub for executor tests so we
// don't pay bbolt startup cost per test.
type inMemMembers struct {
	mu      sync.Mutex
	byKey   map[string]*membership.Member
	byUname map[string]*membership.Member
}

func newInMemMembers() *inMemMembers {
	return &inMemMembers{
		byKey:   make(map[string]*membership.Member),
		byUname: make(map[string]*membership.Member),
	}
}

func (s *inMemMembers) put(m membership.Member) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := m
	s.byKey[memKey(m.UserID, m.AbsChatID)] = &cp
	if m.Username != "" {
		s.byUname[unameKey(m.AbsChatID, m.Username)] = &cp
	}
}

func memKey(u, c int64) string          { return "k:" + i64s(u) + ":" + i64s(c) }
func unameKey(c int64, u string) string { return "u:" + i64s(c) + ":" + u }

func i64s(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func (s *inMemMembers) UpsertMember(_ context.Context, p membership.MemberPatch) (*membership.Member, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.byKey[memKey(p.UserID, p.AbsChatID)]
	if m == nil {
		m = &membership.Member{UserID: p.UserID, AbsChatID: p.AbsChatID}
	}
	s.byKey[memKey(p.UserID, p.AbsChatID)] = m
	return m, nil
}

func (s *inMemMembers) GetMember(_ context.Context, u, c int64) (*membership.Member, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.byKey[memKey(u, c)]
	if m == nil {
		return nil, membership.ErrNotFound
	}
	return m, nil
}

func (s *inMemMembers) GetMemberByUsername(_ context.Context, c int64, u string) (*membership.Member, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.byUname[unameKey(c, u)]
	if m == nil {
		return nil, membership.ErrNotFound
	}
	return m, nil
}

func (s *inMemMembers) ListByChat(_ context.Context, _ int64) ([]membership.Member, error) {
	return nil, nil
}

func (s *inMemMembers) UpsertChat(_ context.Context, _ membership.Chat) error { return nil }

func (s *inMemMembers) GetChat(_ context.Context, _ int64) (*membership.Chat, error) {
	return nil, membership.ErrChatNotFound
}

func (s *inMemMembers) ListChats(_ context.Context) ([]membership.Chat, error) { return nil, nil }

// fakeAdminWithBot is an AdminChecker that knows a fixed admin set per
// chat - used to drive ValidateTarget through the real moderation
// service without spinning up shared.AdminCache + a Telegram API mock.
// Wait: moderation.Service still uses *shared.AdminCache for IsAdmin
// during ValidateTarget. We need a real AdminCache backed by MockAPI
// to make the executor end-to-end test happen.

func newModExecEnv(t *testing.T, adminIDs []int64) (*ModerationExecutor, *inMemMembers, *testutil.MockAPI, int64) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	store, err := storage.NewBoltStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	warnRepo := storage.NewWarnRepo(store.DB())

	api := testutil.NewMockAPI()
	chatID := int64(1001234567890)
	api.AdminIDs[chatID] = adminIDs

	log := testLogger()
	adminCache := shared.NewAdminCache(api, 999, log)
	modSvc := moderation.NewService(warnRepo, api, adminCache, log)

	members := newInMemMembers()
	exec := NewModerationExecutor(modSvc, members, adminCache, log)

	return exec, members, api, chatID
}

func mkQuery(actorID, signedChatID int64) telego.CallbackQuery {
	return telego.CallbackQuery{
		ID:   "cb1",
		From: telego.User{ID: actorID},
		Message: &telego.Message{
			MessageID: 42,
			Chat:      telego.Chat{ID: signedChatID, Type: telego.ChatTypeSupergroup},
		},
	}
}

func TestExecutorWarnHappyPath(t *testing.T) {
	exec, members, api, chatID := newModExecEnv(t, []int64{100})
	members.put(membership.Member{UserID: 200, AbsChatID: chatID, Username: "bob"})

	action := &pending.Action{
		Kind: pending.KindWarn, AbsChatID: chatID,
		ActorUserID: 100, TargetDisplay: "@bob", Reason: "spam",
		ExpiresAt: time.Now().Add(time.Hour),
	}
	resp := exec.ExecuteWarn(context.Background(), mkQuery(100, -chatID), action)
	if resp.EditedText == "" {
		t.Fatalf("expected edited message: %+v", resp)
	}
	if api.CallCount("RestrictChatMember") != 0 {
		t.Fatalf("first warn must not auto-mute, got %d restricts", api.CallCount("RestrictChatMember"))
	}
}

func TestExecutorWarnUnknownTarget(t *testing.T) {
	exec, _, _, chatID := newModExecEnv(t, []int64{100})

	action := &pending.Action{
		Kind: pending.KindWarn, AbsChatID: chatID,
		ActorUserID: 100, TargetDisplay: "@ghost",
		ExpiresAt: time.Now().Add(time.Hour),
	}
	resp := exec.ExecuteWarn(context.Background(), mkQuery(100, -chatID), action)
	if !resp.ShowAlert {
		t.Fatal("unknown target must show alert")
	}
	if resp.EditedText != "" {
		t.Fatal("must not edit message on alert")
	}
	if strings.Contains(resp.AnswerText, "@ghost") {
		t.Fatalf("unknown target alert must not include Telegram mention: %q", resp.AnswerText)
	}
}

func TestExecutorWarnRejectsAdminTarget(t *testing.T) {
	exec, members, _, chatID := newModExecEnv(t, []int64{100, 300})
	members.put(membership.Member{UserID: 300, AbsChatID: chatID, Username: "carol"})

	action := &pending.Action{
		Kind: pending.KindWarn, AbsChatID: chatID,
		ActorUserID: 100, TargetDisplay: "@carol",
		ExpiresAt: time.Now().Add(time.Hour),
	}
	resp := exec.ExecuteWarn(context.Background(), mkQuery(100, -chatID), action)
	if !resp.ShowAlert {
		t.Fatalf("admin target must be rejected, got %+v", resp)
	}
}

func TestExecutorWarnRejectsBotTarget(t *testing.T) {
	exec, members, _, chatID := newModExecEnv(t, []int64{100})
	members.put(membership.Member{UserID: 200, AbsChatID: chatID, Username: "spambot", IsBot: true})

	action := &pending.Action{
		Kind: pending.KindWarn, AbsChatID: chatID,
		ActorUserID: 100, TargetDisplay: "@spambot",
		ExpiresAt: time.Now().Add(time.Hour),
	}
	resp := exec.ExecuteWarn(context.Background(), mkQuery(100, -chatID), action)
	if !resp.ShowAlert {
		t.Fatalf("bot target must be rejected: %+v", resp)
	}
}

func TestExecutorWarnRejectsSelf(t *testing.T) {
	exec, members, _, chatID := newModExecEnv(t, []int64{100})
	members.put(membership.Member{UserID: 100, AbsChatID: chatID, Username: "alice"})

	action := &pending.Action{
		Kind: pending.KindWarn, AbsChatID: chatID,
		ActorUserID: 100, TargetDisplay: "@alice",
		ExpiresAt: time.Now().Add(time.Hour),
	}
	resp := exec.ExecuteWarn(context.Background(), mkQuery(100, -chatID), action)
	if !resp.ShowAlert {
		t.Fatalf("self target must be rejected: %+v", resp)
	}
}

func TestExecutorWarnTriggersAutoMute(t *testing.T) {
	exec, members, api, chatID := newModExecEnv(t, []int64{100})
	members.put(membership.Member{UserID: 200, AbsChatID: chatID, Username: "bob"})

	for i := 0; i < 3; i++ {
		action := &pending.Action{
			Kind: pending.KindWarn, AbsChatID: chatID,
			ActorUserID: 100, TargetDisplay: "@bob", Reason: "spam",
			ExpiresAt: time.Now().Add(time.Hour),
		}
		_ = exec.ExecuteWarn(context.Background(), mkQuery(100, -chatID), action)
	}
	if api.CallCount("RestrictChatMember") != 1 {
		t.Fatalf("3rd warn must trigger AutoMute exactly once, got %d", api.CallCount("RestrictChatMember"))
	}
}

func TestExecutorMuteHappyPath(t *testing.T) {
	exec, members, api, chatID := newModExecEnv(t, []int64{100})
	members.put(membership.Member{UserID: 200, AbsChatID: chatID, Username: "bob"})

	action := &pending.Action{
		Kind: pending.KindMute, AbsChatID: chatID,
		ActorUserID: 100, TargetDisplay: "@bob",
		Duration:  30 * time.Minute,
		ExpiresAt: time.Now().Add(time.Hour),
	}
	resp := exec.ExecuteMute(context.Background(), mkQuery(100, -chatID), action)
	if resp.EditedText == "" {
		t.Fatal("expected edited text")
	}
	if api.CallCount("RestrictChatMember") != 1 {
		t.Fatalf("expected 1 restrict call, got %d", api.CallCount("RestrictChatMember"))
	}
}

func TestExecutorBanHappyPath(t *testing.T) {
	exec, members, api, chatID := newModExecEnv(t, []int64{100})
	members.put(membership.Member{UserID: 200, AbsChatID: chatID, Username: "bob"})

	action := &pending.Action{
		Kind: pending.KindBan, AbsChatID: chatID,
		ActorUserID: 100, TargetDisplay: "@bob", Reason: "raid",
		ExpiresAt: time.Now().Add(time.Hour),
	}
	resp := exec.ExecuteBan(context.Background(), mkQuery(100, -chatID), action)
	if resp.EditedText == "" {
		t.Fatal("expected edited text")
	}
	if api.CallCount("BanChatMember") != 1 {
		t.Fatalf("expected 1 ban call, got %d", api.CallCount("BanChatMember"))
	}
}

func TestExecutorUnmuteHappyPath(t *testing.T) {
	exec, members, api, chatID := newModExecEnv(t, []int64{100})
	members.put(membership.Member{UserID: 200, AbsChatID: chatID, Username: "bob"})

	action := &pending.Action{
		Kind: pending.KindUnmute, AbsChatID: chatID,
		ActorUserID: 100, TargetDisplay: "@bob",
		ExpiresAt: time.Now().Add(time.Hour),
	}
	resp := exec.ExecuteUnmute(context.Background(), mkQuery(100, -chatID), action)
	if resp.EditedText == "" {
		t.Fatal("expected edited text")
	}
	if api.CallCount("RestrictChatMember") != 1 {
		t.Fatalf("expected 1 restrict (with default perms) call, got %d", api.CallCount("RestrictChatMember"))
	}
}

func TestExecutorUnbanHappyPath(t *testing.T) {
	exec, members, api, chatID := newModExecEnv(t, []int64{100})
	members.put(membership.Member{UserID: 200, AbsChatID: chatID, Username: "bob"})
	api.ChatMembers["1001234567890:200"] = "kicked"

	action := &pending.Action{
		Kind: pending.KindUnban, AbsChatID: chatID,
		ActorUserID: 100, TargetDisplay: "@bob",
		ExpiresAt: time.Now().Add(time.Hour),
	}
	resp := exec.ExecuteUnban(context.Background(), mkQuery(100, -chatID), action)
	if resp.EditedText == "" {
		t.Fatal("expected edited text")
	}
	if api.CallCount("UnbanChatMember") != 1 {
		t.Fatalf("expected 1 unban call, got %d", api.CallCount("UnbanChatMember"))
	}
}

func TestExecutorUnbanRejectsNotBanned(t *testing.T) {
	exec, members, _, chatID := newModExecEnv(t, []int64{100})
	members.put(membership.Member{UserID: 200, AbsChatID: chatID, Username: "bob"})
	// chat member status defaults to "member" -> Unban must refuse

	action := &pending.Action{
		Kind: pending.KindUnban, AbsChatID: chatID,
		ActorUserID: 100, TargetDisplay: "@bob",
		ExpiresAt: time.Now().Add(time.Hour),
	}
	resp := exec.ExecuteUnban(context.Background(), mkQuery(100, -chatID), action)
	if !resp.ShowAlert {
		t.Fatal("unban of non-banned user should alert")
	}
}

func TestExecutorRegisterAllRoutes(t *testing.T) {
	exec, _, _, _ := newModExecEnv(t, []int64{100})
	store := newFakePending()
	d := NewCallbackDispatcher(store, stubAdminCache(true), nil, testLogger())
	exec.RegisterAll(d)

	for _, kind := range []pending.Kind{
		pending.KindWarn, pending.KindMute, pending.KindUnmute,
		pending.KindBan, pending.KindUnban,
	} {
		key := string(kind) + ":" + cbApply
		if d.executors[key] == nil {
			t.Errorf("executor not registered for %s", key)
		}
	}
}

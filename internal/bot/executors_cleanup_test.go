package bot

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mymmrac/telego"

	"github.com/veschin/bidlobot/internal/domain/cleanup"
	"github.com/veschin/bidlobot/internal/domain/membership"
	"github.com/veschin/bidlobot/internal/domain/pending"
	"github.com/veschin/bidlobot/internal/testutil"
)

// recordingEditor captures EditMessageText calls so worker tests can
// synchronize with the goroutine and assert message progression.
type recordingEditor struct {
	mu    sync.Mutex
	edits []string
	done  chan struct{}
}

func newRecordingEditor(expected int) *recordingEditor {
	return &recordingEditor{done: make(chan struct{}, expected)}
}

func (r *recordingEditor) EditMessageText(_ context.Context, p *telego.EditMessageTextParams) (*telego.Message, error) {
	r.mu.Lock()
	r.edits = append(r.edits, p.Text)
	r.mu.Unlock()
	select {
	case r.done <- struct{}{}:
	default:
	}
	return &telego.Message{}, nil
}

func (r *recordingEditor) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.edits))
	copy(out, r.edits)
	return out
}

// newInMem and put() are defined in cleanup_test.go's package, but
// it's package cleanup_test, not bot. So define a slim local store
// here as well.
type inMemForCleanup struct {
	mu      sync.Mutex
	members map[int64]map[int64]membership.Member
	chats   map[int64]membership.Chat
}

func newInMem() *inMemForCleanup {
	return &inMemForCleanup{
		members: make(map[int64]map[int64]membership.Member),
		chats:   make(map[int64]membership.Chat),
	}
}

func (s *inMemForCleanup) put(m membership.Member) {
	s.mu.Lock()
	defer s.mu.Unlock()
	chat, ok := s.members[m.AbsChatID]
	if !ok {
		chat = make(map[int64]membership.Member)
		s.members[m.AbsChatID] = chat
	}
	chat[m.UserID] = m
}

func (s *inMemForCleanup) UpsertMember(_ context.Context, p membership.MemberPatch) (*membership.Member, error) {
	return &membership.Member{UserID: p.UserID, AbsChatID: p.AbsChatID}, nil
}

func (s *inMemForCleanup) GetMember(_ context.Context, u, c int64) (*membership.Member, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	chat, ok := s.members[c]
	if !ok {
		return nil, membership.ErrNotFound
	}
	m, ok := chat[u]
	if !ok {
		return nil, membership.ErrNotFound
	}
	return &m, nil
}

func (s *inMemForCleanup) GetMemberByUsername(_ context.Context, _ int64, _ string) (*membership.Member, error) {
	return nil, membership.ErrNotFound
}

func (s *inMemForCleanup) ListByChat(_ context.Context, c int64) ([]membership.Member, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	chat, ok := s.members[c]
	if !ok {
		return nil, nil
	}
	out := make([]membership.Member, 0, len(chat))
	for _, m := range chat {
		out = append(out, m)
	}
	return out, nil
}

func (s *inMemForCleanup) UpsertChat(_ context.Context, c membership.Chat) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.chats[c.AbsChatID] = c
	return nil
}

func (s *inMemForCleanup) GetChat(_ context.Context, c int64) (*membership.Chat, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ch, ok := s.chats[c]
	if !ok {
		return nil, membership.ErrChatNotFound
	}
	return &ch, nil
}

func (s *inMemForCleanup) ListChats(_ context.Context) ([]membership.Chat, error) {
	return nil, nil
}

func mkCleanupQuery(actor int64, signed int64, msgID int) telego.CallbackQuery {
	return telego.CallbackQuery{
		ID:   "cb",
		From: telego.User{ID: actor},
		Message: &telego.Message{
			MessageID: msgID,
			Chat:      telego.Chat{ID: signed, Type: telego.ChatTypeSupergroup},
		},
	}
}

func newCleanupExec(t *testing.T, members []membership.Member) (*CleanupExecutor, *recordingEditor, *fakePendingStore) {
	t.Helper()
	store := newInMem()
	for _, m := range members {
		store.put(m)
	}
	api := testutil.NewMockAPI()
	svc := cleanup.NewService(store, api, testLogger())
	svc.SetKickInterval(time.Millisecond)
	editor := newRecordingEditor(100)
	pendingStore := newFakePending()
	exec := NewCleanupExecutor(svc, pendingStore, editor, testLogger())
	return exec, editor, pendingStore
}

func TestCleanupPreviewEmpty(t *testing.T) {
	exec, _, pendingStore := newCleanupExec(t, nil)
	pendingStore.data["abc"] = &pending.Action{
		ID: "abc", Kind: pending.KindCleanup, AbsChatID: 100,
		ActorUserID: 1, Threshold: 30 * 24 * time.Hour,
		ExpiresAt: time.Now().Add(time.Hour),
	}

	resp := exec.ExecutePreview(context.Background(), mkCleanupQuery(1, -100, 42), pendingStore.data["abc"])
	if !strings.Contains(resp.EditedText, "Кандидатов на чистку нет") {
		t.Fatalf("expected empty-preview body, got %q", resp.EditedText)
	}
	if _, ok := pendingStore.data["abc"]; ok {
		t.Fatal("pending must be deleted on empty preview")
	}
}

func TestCleanupPreviewWithCandidates(t *testing.T) {
	now := time.Now().UTC()
	members := []membership.Member{
		{UserID: 200, AbsChatID: 100, Status: membership.StatusMember, Username: "ghost"},
		{UserID: 300, AbsChatID: 100, Status: membership.StatusMember, Username: "lurker", LastSeenAt: now.Add(-200 * 24 * time.Hour)},
	}
	exec, _, pendingStore := newCleanupExec(t, members)
	action := &pending.Action{
		ID: "abc", Kind: pending.KindCleanup, AbsChatID: 100,
		ActorUserID: 1, Threshold: 30 * 24 * time.Hour,
		ExpiresAt: now.Add(time.Hour),
	}
	pendingStore.data["abc"] = action

	resp := exec.ExecutePreview(context.Background(), mkCleanupQuery(1, -100, 42), action)
	if !strings.Contains(resp.EditedText, "Кандидаты на чистку") {
		t.Fatalf("expected candidate body, got %q", resp.EditedText)
	}
	if resp.ReplyMarkup == nil || len(resp.ReplyMarkup.InlineKeyboard) == 0 {
		t.Fatal("preview must carry confirm keyboard")
	}
	// inline keyboard must contain apply + cancel
	hasApply, hasCancel := false, false
	for _, row := range resp.ReplyMarkup.InlineKeyboard {
		for _, btn := range row {
			if strings.Contains(btn.CallbackData, "apply") {
				hasApply = true
			}
			if strings.Contains(btn.CallbackData, "cancel") {
				hasCancel = true
			}
		}
	}
	if !hasApply || !hasCancel {
		t.Fatal("preview keyboard must have both apply and cancel")
	}
}

func TestCleanupPreviewCapsListLength(t *testing.T) {
	now := time.Now().UTC()
	var members []membership.Member
	for i := int64(1); i <= 50; i++ {
		members = append(members, membership.Member{
			UserID: i, AbsChatID: 100, Status: membership.StatusMember,
			LastSeenAt: now.Add(time.Duration(-i) * 24 * time.Hour),
		})
	}
	exec, _, pendingStore := newCleanupExec(t, members)
	action := &pending.Action{
		ID: "abc", Kind: pending.KindCleanup, AbsChatID: 100,
		ActorUserID: 1, Threshold: 30 * 24 * time.Hour,
		ExpiresAt: now.Add(time.Hour),
	}
	pendingStore.data["abc"] = action

	resp := exec.ExecutePreview(context.Background(), mkCleanupQuery(1, -100, 42), action)
	if !strings.Contains(resp.EditedText, "и ещё") {
		t.Fatalf("preview must indicate truncation, got %q", resp.EditedText)
	}
}

func TestCleanupApplyEmptyCandidates(t *testing.T) {
	exec, _, pendingStore := newCleanupExec(t, nil)
	pendingStore.data["abc"] = &pending.Action{
		ID: "abc", Kind: pending.KindCleanup, AbsChatID: 100,
		ActorUserID: 1, Threshold: 30 * 24 * time.Hour,
		ExpiresAt: time.Now().Add(time.Hour),
	}

	resp := exec.ExecuteKick(context.Background(), mkCleanupQuery(1, -100, 42), pendingStore.data["abc"])
	if !strings.Contains(resp.EditedText, "Кандидатов на чистку нет") {
		t.Fatalf("expected empty body, got %q", resp.EditedText)
	}
	if _, ok := pendingStore.data["abc"]; ok {
		t.Fatal("pending must be deleted")
	}
}

func TestCleanupApplyStartsWorker(t *testing.T) {
	now := time.Now().UTC()
	members := []membership.Member{
		{UserID: 200, AbsChatID: 100, Status: membership.StatusMember, Username: "ghost"},
		{UserID: 300, AbsChatID: 100, Status: membership.StatusMember, Username: "lurker"},
	}
	exec, editor, pendingStore := newCleanupExec(t, members)
	action := &pending.Action{
		ID: "abc", Kind: pending.KindCleanup, AbsChatID: 100,
		ActorUserID: 1, Threshold: 30 * 24 * time.Hour,
		ExpiresAt: now.Add(time.Hour),
	}
	pendingStore.data["abc"] = action

	resp := exec.ExecuteKick(context.Background(), mkCleanupQuery(1, -100, 42), action)
	if !strings.Contains(resp.EditedText, "Чистка запущена") {
		t.Fatalf("expected starting body, got %q", resp.EditedText)
	}
	// Wait for the worker to finish (final edit) - should be quick with 1ms interval
	deadline := time.After(2 * time.Second)
	for {
		select {
		case <-editor.done:
			edits := editor.snapshot()
			if len(edits) > 0 && strings.Contains(edits[len(edits)-1], "Чистка завершена") {
				return
			}
		case <-deadline:
			t.Fatalf("worker did not finish in time, edits so far: %v", editor.snapshot())
		}
	}
}

func TestRenderProgressBarFull(t *testing.T) {
	bar := progressBar(10, 10)
	if !strings.Contains(bar, "█") {
		t.Fatal("full bar should contain filled blocks")
	}
}

func TestRenderProgressBarEmpty(t *testing.T) {
	bar := progressBar(0, 10)
	if strings.Contains(bar, "█") {
		t.Fatal("zero progress bar must not contain filled blocks")
	}
}

func TestRenderFinalReportShowsErrors(t *testing.T) {
	r := &cleanup.Report{
		Total: 3, Kicked: 1, Failed: 2,
		StartedAt:  time.Now(),
		FinishedAt: time.Now().Add(10 * time.Second),
		Entries: []cleanup.ExecutionEntry{
			{UserID: 1, Display: "@a", Outcome: cleanup.OutcomeKicked},
			{UserID: 2, Display: "@b", Outcome: cleanup.OutcomeFailed, APIError: "not enough rights"},
			{UserID: 3, Display: "@c", Outcome: cleanup.OutcomeFailed, APIError: "user is admin"},
		},
	}
	body := renderFinalReport(r)
	for _, want := range []string{"Чистка завершена", "Кикнуто:", "<b>1</b>", "Ошибок:", "@b", "not enough rights"} {
		if !strings.Contains(body, want) {
			t.Errorf("final report missing %q. Got:\n%s", want, body)
		}
	}
}

func TestOutcomeIconLabels(t *testing.T) {
	cases := []struct {
		o         cleanup.Outcome
		wantIcon  string
		wantLabel string
	}{
		{cleanup.OutcomeKicked, "✅", "кикнут"},
		{cleanup.OutcomeSkippedAdmin, "👑", "пропуск"},
		{cleanup.OutcomeSkippedBot, "🤖", "пропуск"},
		{cleanup.OutcomeSkippedAlready, "🚪", "уже"},
		{cleanup.OutcomeFailed, "❌", "ошибка"},
	}
	for _, c := range cases {
		icon, label := outcomeIconLabel(c.o)
		if icon != c.wantIcon {
			t.Errorf("icon for %s: got %q, want %q", c.o, icon, c.wantIcon)
		}
		if !strings.Contains(label, c.wantLabel) {
			t.Errorf("label for %s: got %q, want fragment %q", c.o, label, c.wantLabel)
		}
	}
}

func TestCleanupRegisterAll(t *testing.T) {
	exec, _, _ := newCleanupExec(t, nil)
	d := NewCallbackDispatcher(newFakePending(), stubAdminCache(true), nil, testLogger())
	exec.RegisterAll(d)
	if d.executors["cleanup:preview"] == nil {
		t.Error("preview executor not registered")
	}
	if d.executors["cleanup:apply"] == nil {
		t.Error("apply executor not registered")
	}
}

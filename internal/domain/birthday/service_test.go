package birthday

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mymmrac/telego"

	"github.com/veschin/bidlobot/internal/domain/membership"
)

var testMSK = time.FixedZone("MSK", 3*60*60)

func at(y int, m time.Month, d, h int) time.Time {
	return time.Date(y, m, d, h, 0, 0, 0, testMSK)
}

type fakeStore struct {
	membership.Store
	chats   []membership.Chat
	members map[int64][]membership.Member
}

func (f *fakeStore) ListChats(context.Context) ([]membership.Chat, error) {
	return f.chats, nil
}

func (f *fakeStore) ListByChat(_ context.Context, absChatID int64) ([]membership.Member, error) {
	return f.members[absChatID], nil
}

type fakeMarkers struct {
	mu    sync.Mutex
	marks map[string]time.Time
}

func newFakeMarkers() *fakeMarkers {
	return &fakeMarkers{marks: make(map[string]time.Time)}
}

func (f *fakeMarkers) key(absChatID, userID int64, day string) string {
	return fmt.Sprintf("%d:%d:%s", absChatID, userID, day)
}

func (f *fakeMarkers) IsGreeted(_ context.Context, absChatID, userID int64, day string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.marks[f.key(absChatID, userID, day)]
	return ok, nil
}

func (f *fakeMarkers) MarkGreeted(_ context.Context, absChatID, userID int64, day string, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.marks[f.key(absChatID, userID, day)] = at
	return nil
}

func (f *fakeMarkers) ClearGreeted(_ context.Context, absChatID, userID int64, day string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.marks, f.key(absChatID, userID, day))
	return nil
}

func (f *fakeMarkers) Prune(_ context.Context, absChatID int64, before time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	prefix := fmt.Sprintf("%d:", absChatID)
	for k, markedAt := range f.marks {
		if strings.HasPrefix(k, prefix) && markedAt.Before(before) {
			delete(f.marks, k)
		}
	}
	return nil
}

type fakeAPI struct {
	mu           sync.Mutex
	chats        map[int64]*telego.ChatFullInfo
	chatErrs     map[int64]error
	memberStatus map[int64]string
	memberErrs   map[int64]error
	sent         []*telego.SendMessageParams
	sendErr      error
	sendCalls    int
	getChatCalls int
}

func newFakeAPI() *fakeAPI {
	return &fakeAPI{
		chats:        make(map[int64]*telego.ChatFullInfo),
		chatErrs:     make(map[int64]error),
		memberStatus: make(map[int64]string),
		memberErrs:   make(map[int64]error),
	}
}

func (f *fakeAPI) GetChat(_ context.Context, params *telego.GetChatParams) (*telego.ChatFullInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.getChatCalls++
	if err := f.chatErrs[params.ChatID.ID]; err != nil {
		return nil, err
	}
	if info, ok := f.chats[params.ChatID.ID]; ok {
		return info, nil
	}
	return &telego.ChatFullInfo{ID: params.ChatID.ID}, nil
}

func (f *fakeAPI) GetChatMember(_ context.Context, params *telego.GetChatMemberParams) (telego.ChatMember, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.memberErrs[params.UserID]; err != nil {
		return nil, err
	}
	switch f.memberStatus[params.UserID] {
	case "left":
		return &telego.ChatMemberLeft{User: telego.User{ID: params.UserID}}, nil
	case "kicked":
		return &telego.ChatMemberBanned{User: telego.User{ID: params.UserID}}, nil
	case "restricted":
		return &telego.ChatMemberRestricted{User: telego.User{ID: params.UserID}, IsMember: false}, nil
	default:
		return &telego.ChatMemberMember{User: telego.User{ID: params.UserID}}, nil
	}
}

func (f *fakeAPI) SendMessage(_ context.Context, params *telego.SendMessageParams) (*telego.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sendCalls++
	if f.sendErr != nil {
		return nil, f.sendErr
	}
	f.sent = append(f.sent, params)
	return &telego.Message{MessageID: len(f.sent)}, nil
}

func newTestService(now time.Time, store *fakeStore, api *fakeAPI, markers *fakeMarkers) *Service {
	return NewService(store, api, markers,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		Config{Now: func() time.Time { return now }})
}

func oneMemberStore() *fakeStore {
	return &fakeStore{
		chats: []membership.Chat{{AbsChatID: 100, BotStatus: membership.StatusAdministrator}},
		members: map[int64][]membership.Member{
			100: {{UserID: 1, AbsChatID: 100, FirstName: "Алиса", Status: membership.StatusMember}},
		},
	}
}

func TestRunOnceSendsOneGreeting(t *testing.T) {
	store := oneMemberStore()
	api := newFakeAPI()
	api.chats[1] = &telego.ChatFullInfo{ID: 1, Birthdate: &telego.Birthdate{Day: 27, Month: 10}}
	markers := newFakeMarkers()
	now := at(2026, time.October, 27, 10)
	svc := newTestService(now, store, api, markers)

	svc.RunOnce(context.Background(), now)

	if len(api.sent) != 1 {
		t.Fatalf("sent = %d, want 1", len(api.sent))
	}
	if api.sent[0].ChatID.ID != -100 {
		t.Errorf("chat id = %d, want -100", api.sent[0].ChatID.ID)
	}
	if api.sent[0].ParseMode != telego.ModeHTML {
		t.Errorf("parse mode = %q, want HTML", api.sent[0].ParseMode)
	}
	if !strings.Contains(api.sent[0].Text, "tg://user?id=1") {
		t.Errorf("text %q lacks a mention", api.sent[0].Text)
	}
	if !strings.Contains(api.sent[0].Text, "Алиса") {
		t.Errorf("text %q lacks the name", api.sent[0].Text)
	}
	greeted, _ := markers.IsGreeted(context.Background(), 100, 1, "2026-10-27")
	if !greeted {
		t.Error("marker not written")
	}

	svc.RunOnce(context.Background(), now)
	if len(api.sent) != 1 {
		t.Fatalf("second run sent = %d, want 1", len(api.sent))
	}
}

func TestRunOnceSkipsMissingBirthdate(t *testing.T) {
	store := oneMemberStore()
	api := newFakeAPI()
	api.chats[1] = &telego.ChatFullInfo{ID: 1}
	markers := newFakeMarkers()
	now := at(2026, time.October, 27, 10)
	svc := newTestService(now, store, api, markers)

	svc.RunOnce(context.Background(), now)

	if len(api.sent) != 0 {
		t.Fatalf("sent = %d, want 0", len(api.sent))
	}
}

func TestRunOnceSkipsLocalLeftMember(t *testing.T) {
	store := oneMemberStore()
	store.members[100][0].Status = membership.StatusLeft
	api := newFakeAPI()
	api.chats[1] = &telego.ChatFullInfo{ID: 1, Birthdate: &telego.Birthdate{Day: 27, Month: 10}}
	now := at(2026, time.October, 27, 10)
	svc := newTestService(now, store, api, newFakeMarkers())

	svc.RunOnce(context.Background(), now)

	if api.getChatCalls != 0 {
		t.Errorf("getChat calls = %d, want 0", api.getChatCalls)
	}
	if len(api.sent) != 0 {
		t.Errorf("sent = %d, want 0", len(api.sent))
	}
}

func TestRunOnceSkipsMemberWhoLeftLive(t *testing.T) {
	store := oneMemberStore()
	api := newFakeAPI()
	api.chats[1] = &telego.ChatFullInfo{ID: 1, Birthdate: &telego.Birthdate{Day: 27, Month: 10}}
	api.memberStatus[1] = "left"
	now := at(2026, time.October, 27, 10)
	svc := newTestService(now, store, api, newFakeMarkers())

	svc.RunOnce(context.Background(), now)

	if len(api.sent) != 0 {
		t.Errorf("sent = %d, want 0", len(api.sent))
	}
}

func TestRunOnceLeapDay(t *testing.T) {
	tests := []struct {
		name string
		now  time.Time
		want int
	}{
		{"feb 28 non-leap", at(2026, time.February, 28, 10), 1},
		{"feb 29 leap", at(2028, time.February, 29, 10), 1},
		{"mar 1 non-leap", at(2026, time.March, 1, 10), 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := oneMemberStore()
			api := newFakeAPI()
			api.chats[1] = &telego.ChatFullInfo{ID: 1, Birthdate: &telego.Birthdate{Day: 29, Month: 2}}
			svc := newTestService(tt.now, store, api, newFakeMarkers())
			svc.RunOnce(context.Background(), tt.now)
			if len(api.sent) != tt.want {
				t.Fatalf("sent = %d, want %d", len(api.sent), tt.want)
			}
		})
	}
}

func TestRunOnceCombinesSeveralBirthdays(t *testing.T) {
	store := &fakeStore{
		chats: []membership.Chat{{AbsChatID: 100, BotStatus: membership.StatusAdministrator}},
		members: map[int64][]membership.Member{
			100: {
				{UserID: 1, AbsChatID: 100, FirstName: "Алиса", Status: membership.StatusMember},
				{UserID: 2, AbsChatID: 100, FirstName: "Боб", Status: membership.StatusMember},
			},
		},
	}
	api := newFakeAPI()
	api.chats[1] = &telego.ChatFullInfo{ID: 1, Birthdate: &telego.Birthdate{Day: 27, Month: 10}}
	api.chats[2] = &telego.ChatFullInfo{ID: 2, Birthdate: &telego.Birthdate{Day: 27, Month: 10}}
	now := at(2026, time.October, 27, 10)
	svc := newTestService(now, store, api, newFakeMarkers())

	svc.RunOnce(context.Background(), now)

	if len(api.sent) != 1 {
		t.Fatalf("sent = %d, want 1", len(api.sent))
	}
	text := api.sent[0].Text
	if !strings.Contains(text, "tg://user?id=1") || !strings.Contains(text, "tg://user?id=2") {
		t.Errorf("text %q lacks both mentions", text)
	}
	if !strings.Contains(text, " и ") {
		t.Errorf("text %q lacks the conjunction", text)
	}
}

func TestRunOnceEscapesName(t *testing.T) {
	store := oneMemberStore()
	store.members[100][0].FirstName = `<b>A&B</b>`
	api := newFakeAPI()
	api.chats[1] = &telego.ChatFullInfo{ID: 1, Birthdate: &telego.Birthdate{Day: 27, Month: 10}}
	now := at(2026, time.October, 27, 10)
	svc := newTestService(now, store, api, newFakeMarkers())

	svc.RunOnce(context.Background(), now)

	if len(api.sent) != 1 {
		t.Fatalf("sent = %d, want 1", len(api.sent))
	}
	if strings.Contains(api.sent[0].Text, "<b>A&B</b>") {
		t.Errorf("name was not escaped: %q", api.sent[0].Text)
	}
	if !strings.Contains(api.sent[0].Text, "&lt;b&gt;A&amp;B&lt;/b&gt;") {
		t.Errorf("escaped name missing: %q", api.sent[0].Text)
	}
}

func TestRunOnceDoesNotLeakImportedName(t *testing.T) {
	store := oneMemberStore()
	store.members[100][0].KnownVia = membership.SourceImport
	store.members[100][0].FirstName = "Private Label"
	store.members[100][0].Username = ""
	api := newFakeAPI()
	api.chats[1] = &telego.ChatFullInfo{ID: 1, Birthdate: &telego.Birthdate{Day: 27, Month: 10}}
	now := at(2026, time.October, 27, 10)
	svc := newTestService(now, store, api, newFakeMarkers())

	svc.RunOnce(context.Background(), now)

	if len(api.sent) != 1 {
		t.Fatalf("sent = %d, want 1", len(api.sent))
	}
	if strings.Contains(api.sent[0].Text, "Private Label") {
		t.Errorf("imported address-book label leaked: %q", api.sent[0].Text)
	}
	if !strings.Contains(api.sent[0].Text, "именинник") {
		t.Errorf("neutral fallback missing: %q", api.sent[0].Text)
	}
}

func TestRunOnceSendErrorClearsMarker(t *testing.T) {
	store := oneMemberStore()
	api := newFakeAPI()
	api.chats[1] = &telego.ChatFullInfo{ID: 1, Birthdate: &telego.Birthdate{Day: 27, Month: 10}}
	api.sendErr = errors.New("boom")
	markers := newFakeMarkers()
	now := at(2026, time.October, 27, 10)
	svc := newTestService(now, store, api, markers)

	svc.RunOnce(context.Background(), now)

	greeted, _ := markers.IsGreeted(context.Background(), 100, 1, "2026-10-27")
	if greeted {
		t.Error("marker survived a failed send")
	}
	if api.sendCalls != 1 {
		t.Fatalf("send calls = %d, want 1", api.sendCalls)
	}

	api.sendErr = nil
	svc.RunOnce(context.Background(), now)
	if len(api.sent) != 1 {
		t.Fatalf("retry sent = %d, want 1", len(api.sent))
	}
}

func TestRunOnceSkipsLeftBotChat(t *testing.T) {
	store := oneMemberStore()
	store.chats[0].BotStatus = membership.StatusLeft
	api := newFakeAPI()
	api.chats[1] = &telego.ChatFullInfo{ID: 1, Birthdate: &telego.Birthdate{Day: 27, Month: 10}}
	now := at(2026, time.October, 27, 10)
	svc := newTestService(now, store, api, newFakeMarkers())

	svc.RunOnce(context.Background(), now)

	if api.getChatCalls != 0 {
		t.Errorf("getChat calls = %d, want 0", api.getChatCalls)
	}
}

func TestRunOncePrunesLeftBotChatMarkers(t *testing.T) {
	store := oneMemberStore()
	store.chats[0].BotStatus = membership.StatusLeft
	markers := newFakeMarkers()
	now := at(2026, time.October, 27, 10)
	if err := markers.MarkGreeted(context.Background(), 100, 1, "2026-10-24", now.Add(-72*time.Hour)); err != nil {
		t.Fatal(err)
	}
	svc := newTestService(now, store, newFakeAPI(), markers)

	svc.RunOnce(context.Background(), now)

	greeted, _ := markers.IsGreeted(context.Background(), 100, 1, "2026-10-24")
	if greeted {
		t.Error("stale marker in a left chat was not pruned")
	}
}

func TestNextRun(t *testing.T) {
	tests := []struct {
		now  time.Time
		want time.Time
	}{
		{at(2026, time.October, 27, 9), at(2026, time.October, 27, 10)},
		{at(2026, time.October, 27, 10), at(2026, time.October, 28, 10)},
		{at(2026, time.October, 27, 11), at(2026, time.October, 28, 10)},
		{at(2026, time.October, 27, 21), at(2026, time.October, 28, 10)},
	}
	for _, tt := range tests {
		if got := nextRun(tt.now); !got.Equal(tt.want) {
			t.Errorf("nextRun(%s) = %s, want %s", tt.now, got, tt.want)
		}
	}
}

func TestMatches(t *testing.T) {
	tests := []struct {
		name string
		b    *telego.Birthdate
		now  time.Time
		want bool
	}{
		{"matching day", &telego.Birthdate{Day: 27, Month: 10}, at(2026, time.October, 27, 0), true},
		{"other day", &telego.Birthdate{Day: 26, Month: 10}, at(2026, time.October, 27, 0), false},
		{"nil", nil, at(2026, time.October, 27, 0), false},
		{"leap day on feb 28", &telego.Birthdate{Day: 29, Month: 2}, at(2026, time.February, 28, 0), true},
		{"leap day on feb 29", &telego.Birthdate{Day: 29, Month: 2}, at(2028, time.February, 29, 0), true},
		{"leap day on mar 1", &telego.Birthdate{Day: 29, Month: 2}, at(2026, time.March, 1, 0), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := matches(tt.b, tt.now); got != tt.want {
				t.Errorf("matches(%v, %s) = %v, want %v", tt.b, tt.now, got, tt.want)
			}
		})
	}
}

func TestJoinNames(t *testing.T) {
	tests := []struct {
		names []string
		want  string
	}{
		{[]string{"A"}, "A"},
		{[]string{"A", "B"}, "A и B"},
		{[]string{"A", "B", "C"}, "A, B и C"},
	}
	for _, tt := range tests {
		if got := joinNames(tt.names); got != tt.want {
			t.Errorf("joinNames(%v) = %q, want %q", tt.names, got, tt.want)
		}
	}
}

func TestIsMember(t *testing.T) {
	if isMember(&telego.ChatMemberLeft{}) {
		t.Error("left member reported as present")
	}
	if isMember(&telego.ChatMemberBanned{}) {
		t.Error("banned member reported as present")
	}
	if isMember(&telego.ChatMemberRestricted{IsMember: false}) {
		t.Error("restricted non-member reported as present")
	}
	if !isMember(&telego.ChatMemberRestricted{IsMember: true}) {
		t.Error("restricted member reported as absent")
	}
	if !isMember(&telego.ChatMemberMember{}) {
		t.Error("member reported as absent")
	}
}

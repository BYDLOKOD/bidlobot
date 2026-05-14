package membership_test

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mymmrac/telego"

	"github.com/veschin/bidlobot/internal/domain/membership"
	"github.com/veschin/bidlobot/internal/storage"
)

func newSvc(t *testing.T) (*membership.Service, *storage.MembershipRepo) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	st, err := storage.NewBoltStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	repo := storage.NewMembershipRepo(st.DB())
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	return membership.NewService(repo, log), repo
}

func TestServiceRecordMessageHappyPath(t *testing.T) {
	svc, repo := newSvc(t)
	ctx := context.Background()
	ts := time.Now().UTC()

	from := &telego.User{ID: 111, Username: "alice", FirstName: "Alice", IsPremium: true}
	if err := svc.RecordMessage(ctx, 100, from, ts); err != nil {
		t.Fatal(err)
	}

	m, err := repo.GetMember(ctx, 111, 100)
	if err != nil {
		t.Fatal(err)
	}
	if m.MessageCount != 1 {
		t.Fatalf("expected count 1, got %d", m.MessageCount)
	}
	if m.Username != "alice" {
		t.Fatalf("got username %q", m.Username)
	}
	if !m.IsPremium {
		t.Fatal("IsPremium should be set")
	}
	if m.Status != membership.StatusMember {
		t.Fatalf("expected status member, got %s", m.Status)
	}
	if !m.LastMessageAt.Equal(ts) {
		t.Fatalf("LastMessageAt mismatch")
	}
}

func TestServiceRecordMessageNilFromIsNoop(t *testing.T) {
	svc, _ := newSvc(t)
	if err := svc.RecordMessage(context.Background(), 100, nil, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestServiceRecordMessageZeroIDsAreNoop(t *testing.T) {
	svc, _ := newSvc(t)
	ts := time.Now()
	if err := svc.RecordMessage(context.Background(), 0, &telego.User{ID: 111}, ts); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordMessage(context.Background(), 100, &telego.User{ID: 0}, ts); err != nil {
		t.Fatal(err)
	}
}

func TestServiceRecordReactionHappyPath(t *testing.T) {
	svc, repo := newSvc(t)
	ctx := context.Background()
	ts := time.Now().UTC()

	r := telego.MessageReactionUpdated{
		Chat: telego.Chat{ID: -100},
		User: &telego.User{ID: 111, Username: "alice", FirstName: "Alice"},
		Date: ts.Unix(),
		NewReaction: []telego.ReactionType{
			&telego.ReactionTypeEmoji{Emoji: "👍"},
		},
	}
	if err := svc.RecordReaction(ctx, r); err != nil {
		t.Fatal(err)
	}

	m, err := repo.GetMember(ctx, 111, 100)
	if err != nil {
		t.Fatal(err)
	}
	if m.ReactionCount != 1 {
		t.Fatalf("expected reaction count 1, got %d", m.ReactionCount)
	}
	if m.LastReactionAt.IsZero() {
		t.Fatal("LastReactionAt should be set")
	}
	if !m.LastReactionAt.Equal(time.Unix(ts.Unix(), 0).UTC()) {
		t.Fatalf("LastReactionAt mismatch: %v", m.LastReactionAt)
	}
}

func TestServiceRecordReactionAnonymousIgnored(t *testing.T) {
	svc, repo := newSvc(t)
	ctx := context.Background()
	r := telego.MessageReactionUpdated{
		Chat:      telego.Chat{ID: -100},
		User:      nil,
		ActorChat: &telego.Chat{ID: -100},
		Date:      time.Now().Unix(),
	}
	if err := svc.RecordReaction(ctx, r); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.GetMember(ctx, 1, 100); err != membership.ErrNotFound {
		t.Fatal("should not record anonymous reactions")
	}
}

func TestServiceRecordReactionBotIgnored(t *testing.T) {
	svc, repo := newSvc(t)
	ctx := context.Background()
	r := telego.MessageReactionUpdated{
		Chat: telego.Chat{ID: -100},
		User: &telego.User{ID: 999, IsBot: true, Username: "somebot"},
		Date: time.Now().Unix(),
	}
	if err := svc.RecordReaction(ctx, r); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.GetMember(ctx, 999, 100); err != membership.ErrNotFound {
		t.Fatal("bot reactions must not be recorded")
	}
}

func TestServiceRecordChatMemberJoinSetsJoinedAt(t *testing.T) {
	svc, repo := newSvc(t)
	ctx := context.Background()
	ts := time.Now().UTC()

	cmu := telego.ChatMemberUpdated{
		Chat: telego.Chat{ID: -100, Type: telego.ChatTypeSupergroup},
		Date: ts.Unix(),
		NewChatMember: &telego.ChatMemberMember{
			User: telego.User{ID: 111, Username: "alice", FirstName: "Alice"},
		},
	}
	if err := svc.RecordChatMember(ctx, cmu); err != nil {
		t.Fatal(err)
	}
	m, err := repo.GetMember(ctx, 111, 100)
	if err != nil {
		t.Fatal(err)
	}
	if m.Status != membership.StatusMember {
		t.Fatalf("expected status member, got %s", m.Status)
	}
	if m.JoinedAt.IsZero() {
		t.Fatal("JoinedAt should be set on member status")
	}
	if !m.LeftAt.IsZero() {
		t.Fatal("LeftAt should be zero on join")
	}
}

func TestServiceRecordChatMemberLeaveSetsLeftAt(t *testing.T) {
	svc, repo := newSvc(t)
	ctx := context.Background()
	ts := time.Now().UTC()

	cmu := telego.ChatMemberUpdated{
		Chat: telego.Chat{ID: -100, Type: telego.ChatTypeSupergroup},
		Date: ts.Unix(),
		NewChatMember: &telego.ChatMemberLeft{
			User: telego.User{ID: 111, Username: "alice"},
		},
	}
	if err := svc.RecordChatMember(ctx, cmu); err != nil {
		t.Fatal(err)
	}
	m, err := repo.GetMember(ctx, 111, 100)
	if err != nil {
		t.Fatal(err)
	}
	if m.Status != membership.StatusLeft {
		t.Fatalf("expected status left, got %s", m.Status)
	}
	if m.LeftAt.IsZero() {
		t.Fatal("LeftAt should be set on leave status")
	}
}

func TestServiceRecordMyChatMemberRegistersChat(t *testing.T) {
	svc, repo := newSvc(t)
	ctx := context.Background()
	ts := time.Now().UTC()

	cmu := telego.ChatMemberUpdated{
		Chat: telego.Chat{ID: -100, Type: telego.ChatTypeSupergroup, Title: "Test Chat"},
		Date: ts.Unix(),
		NewChatMember: &telego.ChatMemberAdministrator{
			User:               telego.User{ID: 999, IsBot: true},
			CanRestrictMembers: true,
			CanDeleteMessages:  true,
		},
	}
	if err := svc.RecordMyChatMember(ctx, cmu); err != nil {
		t.Fatal(err)
	}
	c, err := repo.GetChat(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	if c.Title != "Test Chat" {
		t.Fatalf("title mismatch: %q", c.Title)
	}
	if c.BotStatus != membership.StatusAdministrator {
		t.Fatalf("expected admin, got %s", c.BotStatus)
	}
	if !c.CanRestrict {
		t.Fatal("CanRestrict should be true")
	}
	if !c.CanDelete {
		t.Fatal("CanDelete should be true")
	}
	if c.InstalledAt.IsZero() {
		t.Fatal("InstalledAt must be set on first record")
	}
}

func TestServiceRecordMyChatMemberPreservesInstalledAt(t *testing.T) {
	svc, repo := newSvc(t)
	ctx := context.Background()
	t1 := time.Now().UTC()
	t2 := t1.Add(time.Hour)

	cmu1 := telego.ChatMemberUpdated{
		Chat: telego.Chat{ID: -100, Type: telego.ChatTypeSupergroup, Title: "Original"},
		Date: t1.Unix(),
		NewChatMember: &telego.ChatMemberAdministrator{
			User: telego.User{ID: 999, IsBot: true}, CanRestrictMembers: true,
		},
	}
	cmu2 := cmu1
	cmu2.Chat.Title = "Renamed"
	cmu2.Date = t2.Unix()

	_ = svc.RecordMyChatMember(ctx, cmu1)
	_ = svc.RecordMyChatMember(ctx, cmu2)

	c, err := repo.GetChat(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	if c.Title != "Renamed" {
		t.Fatalf("title should be Renamed, got %q", c.Title)
	}
	if c.InstalledAt.Unix() != t1.Unix() {
		t.Fatalf("InstalledAt must persist t1, got %v", c.InstalledAt)
	}
}

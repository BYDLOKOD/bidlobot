package storage_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/veschin/bidlobot/internal/domain/referral"
	"github.com/veschin/bidlobot/internal/storage"
)

func newReferralRepo(t *testing.T) *storage.ReferralRepo {
	t.Helper()
	path := filepath.Join(t.TempDir(), "referrals.db")
	s, err := storage.NewBoltStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return storage.NewReferralRepo(s.DB())
}

func mustCreate(t *testing.T, repo *storage.ReferralRepo, ctx context.Context, chat int64, svc referral.Service, ref referral.Referral) (*referral.Service, *referral.Referral) {
	t.Helper()
	s, r, err := repo.Create(ctx, chat, svc, ref)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return s, r
}

func TestReferralRepo_CreateNewServiceAndReferral(t *testing.T) {
	repo := newReferralRepo(t)
	ctx := context.Background()
	const chat int64 = 100

	svc, ref := mustCreate(t, repo, ctx, chat,
		referral.Service{Name: "ZAI Coding Plan", Effect: "+5 баксов"},
		referral.Referral{OwnerUserID: 1, OwnerDisplay: "alice", URL: "https://z.ai/ref/alice"})

	if svc.ID == 0 {
		t.Fatal("service ID must be allocated")
	}
	if ref.ID == 0 {
		t.Fatal("referral ID must be allocated")
	}
	if svc.AbsChatID != chat || ref.AbsChatID != chat {
		t.Fatalf("AbsChatID must be set from chat arg: svc=%d ref=%d", svc.AbsChatID, ref.AbsChatID)
	}
	if ref.ServiceID != svc.ID {
		t.Fatalf("referral ServiceID %d != svc.ID %d", ref.ServiceID, svc.ID)
	}
	if svc.NameKey != "zaicodingplan" {
		t.Errorf("NameKey = %q, want zaicodingplan", svc.NameKey)
	}
}

func TestReferralRepo_ExactNameReuse(t *testing.T) {
	repo := newReferralRepo(t)
	ctx := context.Background()
	const chat int64 = 100

	svc1, _ := mustCreate(t, repo, ctx, chat,
		referral.Service{Name: "ZAI Coding Plan"},
		referral.Referral{OwnerUserID: 1, OwnerDisplay: "alice", URL: "https://z.ai/ref/alice"})

	// Same name, different owner+URL: must reuse svc1 when caller
	// passes svc1.ID.
	svc2, _ := mustCreate(t, repo, ctx, chat,
		referral.Service{ID: svc1.ID, Name: "ZAI Coding Plan"},
		referral.Referral{OwnerUserID: 2, OwnerDisplay: "bob", URL: "https://z.ai/ref/bob"})

	if svc2.ID != svc1.ID {
		t.Fatalf("expected reuse of service %d, got new id %d", svc1.ID, svc2.ID)
	}
}

func TestReferralRepo_NewServiceWithExistingNameRejects(t *testing.T) {
	repo := newReferralRepo(t)
	ctx := context.Background()
	const chat int64 = 100

	mustCreate(t, repo, ctx, chat,
		referral.Service{Name: "ZAI Coding Plan"},
		referral.Referral{OwnerUserID: 1, OwnerDisplay: "alice", URL: "https://z.ai/ref/alice"})

	// Caller asks for a NEW service (ID=0) with the same normalized
	// name. The repo must refuse with ErrServiceExists so the handler
	// can re-prompt with the existing service.
	_, _, err := repo.Create(ctx, chat,
		referral.Service{Name: "z.ai coding-plan"},
		referral.Referral{OwnerUserID: 2, OwnerDisplay: "bob", URL: "https://z.ai/ref/bob"})
	if !errors.Is(err, referral.ErrServiceExists) {
		t.Fatalf("expected ErrServiceExists, got %v", err)
	}
}

func TestReferralRepo_OwnerServiceDuplicate(t *testing.T) {
	repo := newReferralRepo(t)
	ctx := context.Background()
	const chat int64 = 100

	svc1, _ := mustCreate(t, repo, ctx, chat,
		referral.Service{Name: "ZAI Coding Plan"},
		referral.Referral{OwnerUserID: 1, OwnerDisplay: "alice", URL: "https://z.ai/ref/alice"})

	_, _, err := repo.Create(ctx, chat,
		referral.Service{ID: svc1.ID, Name: "ZAI Coding Plan"},
		referral.Referral{OwnerUserID: 1, OwnerDisplay: "alice", URL: "https://z.ai/ref/alice2"})
	if !errors.Is(err, referral.ErrOwnerServiceExists) {
		t.Fatalf("expected ErrOwnerServiceExists, got %v", err)
	}
}

func TestReferralRepo_URLDuplicate(t *testing.T) {
	repo := newReferralRepo(t)
	ctx := context.Background()
	const chat int64 = 100

	cursorSvc, _ := mustCreate(t, repo, ctx, chat,
		referral.Service{Name: "ZAI Coding Plan"},
		referral.Referral{OwnerUserID: 1, OwnerDisplay: "alice", URL: "https://z.ai/ref/alice"})
	mustCreate(t, repo, ctx, chat,
		referral.Service{Name: "Cursor"},
		referral.Referral{OwnerUserID: 2, OwnerDisplay: "bob", URL: "https://cursor.com/bob"})

	// Carol tries to post the SAME URL that Alice already posted, even
	// under a different service: must be rejected chat-wide. Reuse
	// cursorSvc by ID to keep this an exact-URL test, not a duplicate-
	// service test.
	_, _, err := repo.Create(ctx, chat,
		referral.Service{ID: cursorSvc.ID, Name: "ZAI Coding Plan"},
		referral.Referral{OwnerUserID: 3, OwnerDisplay: "carol", URL: "https://z.ai/ref/alice"})
	if !errors.Is(err, referral.ErrURLExists) {
		t.Fatalf("expected ErrURLExists, got %v", err)
	}
}

func TestReferralRepo_ChatIsolation(t *testing.T) {
	repo := newReferralRepo(t)
	ctx := context.Background()

	// Same URL, same owner, different chats: both succeed.
	const a, b int64 = 100, 200
	mustCreate(t, repo, ctx, a,
		referral.Service{Name: "ZAI"},
		referral.Referral{OwnerUserID: 1, OwnerDisplay: "alice", URL: "https://z.ai/ref"})
	mustCreate(t, repo, ctx, b,
		referral.Service{Name: "ZAI"},
		referral.Referral{OwnerUserID: 1, OwnerDisplay: "alice", URL: "https://z.ai/ref"})

	ga, err := repo.List(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	if len(ga) != 1 || len(ga[0].Referrals) != 1 {
		t.Fatalf("chat A: want 1 group/1 ref, got %+v", ga)
	}
	gb, err := repo.List(ctx, b)
	if err != nil {
		t.Fatal(err)
	}
	if len(gb) != 1 || len(gb[0].Referrals) != 1 {
		t.Fatalf("chat B: want 1 group/1 ref, got %+v", gb)
	}
}

func TestReferralRepo_ListSorting(t *testing.T) {
	repo := newReferralRepo(t)
	ctx := context.Background()
	const chat int64 = 100

	// Insert in non-alphabetical order with multiple referrals per svc.
	cursorSvc, _ := mustCreate(t, repo, ctx, chat,
		referral.Service{Name: "Cursor"},
		referral.Referral{OwnerUserID: 2, OwnerDisplay: "bob", URL: "https://c/2"})
	mustCreate(t, repo, ctx, chat,
		referral.Service{ID: cursorSvc.ID, Name: "Cursor"},
		referral.Referral{OwnerUserID: 3, OwnerDisplay: "carol", URL: "https://c/3"})
	mustCreate(t, repo, ctx, chat,
		referral.Service{ID: cursorSvc.ID, Name: "Cursor"},
		referral.Referral{OwnerUserID: 1, OwnerDisplay: "alice", URL: "https://c/1"})
	mustCreate(t, repo, ctx, chat,
		referral.Service{Name: "Anthropic"},
		referral.Referral{OwnerUserID: 4, OwnerDisplay: "dave", URL: "https://a/4"})

	groups, err := repo.List(ctx, chat)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 2 {
		t.Fatalf("want 2 groups, got %d", len(groups))
	}
	if groups[0].Service.Name != "Anthropic" {
		t.Errorf("first group should be Anthropic, got %q", groups[0].Service.Name)
	}
	if groups[1].Service.Name != "Cursor" {
		t.Errorf("second group should be Cursor, got %q", groups[1].Service.Name)
	}
	if len(groups[1].Referrals) != 3 {
		t.Fatalf("Cursor should have 3 referrals, got %d", len(groups[1].Referrals))
	}
	for i := 1; i < len(groups[1].Referrals); i++ {
		if groups[1].Referrals[i-1].ID >= groups[1].Referrals[i].ID {
			t.Errorf("referrals must be sorted by ID ascending")
		}
	}
}

func TestReferralRepo_DeletePrunesServiceOnLastReferral(t *testing.T) {
	repo := newReferralRepo(t)
	ctx := context.Background()
	const chat int64 = 100

	_, ref := mustCreate(t, repo, ctx, chat,
		referral.Service{Name: "ZAI"},
		referral.Referral{OwnerUserID: 1, OwnerDisplay: "alice", URL: "https://z.ai/1"})

	if err := repo.DeleteReferral(ctx, chat, ref.ID); err != nil {
		t.Fatalf("DeleteReferral: %v", err)
	}

	groups, err := repo.List(ctx, chat)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 0 {
		t.Fatalf("after last-referral delete the service must be pruned, got %+v", groups)
	}

	// Re-creating the same service name now must succeed as a new
	// service (proving the name index entry was also pruned).
	svc2, _ := mustCreate(t, repo, ctx, chat,
		referral.Service{Name: "ZAI"},
		referral.Referral{OwnerUserID: 1, OwnerDisplay: "alice", URL: "https://z.ai/2"})
	if svc2.ID == 0 {
		t.Fatal("re-created service must have a fresh ID")
	}
}

func TestReferralRepo_DeleteKeepsServiceWhenReferralsRemain(t *testing.T) {
	repo := newReferralRepo(t)
	ctx := context.Background()
	const chat int64 = 100

	svc1, ref1 := mustCreate(t, repo, ctx, chat,
		referral.Service{Name: "ZAI"},
		referral.Referral{OwnerUserID: 1, OwnerDisplay: "alice", URL: "https://z.ai/1"})
	_, _ = mustCreate(t, repo, ctx, chat,
		referral.Service{ID: svc1.ID, Name: "ZAI"},
		referral.Referral{OwnerUserID: 2, OwnerDisplay: "bob", URL: "https://z.ai/2"})

	if err := repo.DeleteReferral(ctx, chat, ref1.ID); err != nil {
		t.Fatalf("DeleteReferral: %v", err)
	}

	groups, err := repo.List(ctx, chat)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || len(groups[0].Referrals) != 1 {
		t.Fatalf("service must survive with 1 remaining referral, got %+v", groups)
	}
}

func TestReferralRepo_GetReferralMissing(t *testing.T) {
	repo := newReferralRepo(t)
	ctx := context.Background()
	if _, err := repo.GetReferral(ctx, 100, 999); !errors.Is(err, referral.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestReferralRepo_DeleteMissing(t *testing.T) {
	repo := newReferralRepo(t)
	ctx := context.Background()
	if err := repo.DeleteReferral(ctx, 100, 999); !errors.Is(err, referral.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestReferralRepo_SelectedServiceMissing(t *testing.T) {
	repo := newReferralRepo(t)
	ctx := context.Background()
	// Caller claims an existing service ID that does not exist in the
	// chat. Must surface ErrNotFound so the handler can re-prompt.
	_, _, err := repo.Create(ctx, 100,
		referral.Service{ID: 42, Name: "Whatever"},
		referral.Referral{OwnerUserID: 1, OwnerDisplay: "alice", URL: "https://x"})
	if !errors.Is(err, referral.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for pruned service, got %v", err)
	}
}

func TestMigrateChatID_ReferralsRekeyedAndSurvive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "migrate-ref.db")
	s, err := storage.NewBoltStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	const oldAbs, newAbs int64 = 1000, 1001

	repo := storage.NewReferralRepo(s.DB())
	mustCreate(t, repo, ctx, oldAbs,
		referral.Service{Name: "ZAI Coding Plan", Effect: "+5"},
		referral.Referral{OwnerUserID: 1, OwnerDisplay: "alice", URL: "https://z.ai/1"})
	mustCreate(t, repo, ctx, oldAbs,
		referral.Service{Name: "Cursor"},
		referral.Referral{OwnerUserID: 2, OwnerDisplay: "bob", URL: "https://cursor/2"})

	report, err := storage.MigrateChatID(ctx, s.DB(), oldAbs, newAbs)
	if err != nil {
		t.Fatalf("MigrateChatID: %v", err)
	}
	if report.ReferralServices != 2 || report.Referrals != 2 {
		t.Fatalf("migration counters: want 2/2, got %d/%d", report.ReferralServices, report.Referrals)
	}

	// Old chat must be empty.
	if groups, _ := repo.List(ctx, oldAbs); len(groups) != 0 {
		t.Fatalf("old chat must be empty post-migration, got %+v", groups)
	}
	// New chat must contain both services and referrals.
	groups, err := repo.List(ctx, newAbs)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 2 {
		t.Fatalf("new chat: want 2 groups, got %d", len(groups))
	}
	total := 0
	for _, g := range groups {
		total += len(g.Referrals)
	}
	if total != 2 {
		t.Fatalf("new chat: want 2 total referrals, got %d", total)
	}
}
func TestMigrateChatID_ReferralURLDuplicateDroppedAtDestination(t *testing.T) {
	path := filepath.Join(t.TempDir(), "migrate-dup.db")
	s, err := storage.NewBoltStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	const oldAbs, newAbs int64 = 2000, 2001

	oldRepo := storage.NewReferralRepo(s.DB())
	mustCreate(t, oldRepo, ctx, oldAbs,
		referral.Service{Name: "ZAI"},
		referral.Referral{OwnerUserID: 1, OwnerDisplay: "alice", URL: "https://z.ai/shared"})

	// Destination already has the same URL under the same NameKey. The
	// source service is re-pointed, the duplicate source referral is
	// dropped, and the destination's own referral is preserved.
	newRepo := storage.NewReferralRepo(s.DB())
	mustCreate(t, newRepo, ctx, newAbs,
		referral.Service{Name: "ZAI"},
		referral.Referral{OwnerUserID: 9, OwnerDisplay: "zoe", URL: "https://z.ai/shared"})

	if _, err := storage.MigrateChatID(ctx, s.DB(), oldAbs, newAbs); err != nil {
		t.Fatal(err)
	}

	groups, err := newRepo.List(ctx, newAbs)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || len(groups[0].Referrals) != 1 {
		t.Fatalf("duplicate URL must be dropped at destination; got %+v", groups)
	}
	if groups[0].Referrals[0].URL != "https://z.ai/shared" {
		t.Errorf("unexpected URL: %q", groups[0].Referrals[0].URL)
	}
}

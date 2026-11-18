package farm

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/veschin/bidlobot/internal/gameengine"
)

func testEngine(t *testing.T) *bolt.DB {
	t.Helper()
	db, err := bolt.Open(filepath.Join(t.TempDir(), "farm.db"), 0600, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func apply(t *testing.T, db *bolt.DB, chat, user int64, key, verb, arg string, now time.Time) gameengine.Result {
	t.Helper()
	engine := gameengine.New(db, map[string]gameengine.Module{Module: Apply})
	result, err := engine.Apply(Module, gameengine.Action{
		ChatID: chat, UserID: user, Key: key, Verb: verb, Arg: arg, Now: now,
	})
	if err != nil {
		t.Fatalf("%s %s: %v", verb, arg, err)
	}
	return result
}

func applyErr(t *testing.T, db *bolt.DB, chat, user int64, key, verb, arg string, now time.Time) error {
	t.Helper()
	engine := gameengine.New(db, map[string]gameengine.Module{Module: Apply})
	_, err := engine.Apply(Module, gameengine.Action{
		ChatID: chat, UserID: user, Key: key, Verb: verb, Arg: arg, Now: now,
	})
	return err
}

func advance(t *testing.T, db *bolt.DB, now time.Time) []gameengine.Settlement {
	t.Helper()
	engine := gameengine.New(db, map[string]gameengine.Module{Module: Apply})
	settled, err := engine.Advance(now)
	if err != nil {
		t.Fatal(err)
	}
	return settled
}

func boardView(t *testing.T, db *bolt.DB, chat, user int64, now time.Time) *View {
	t.Helper()
	var view any
	err := db.View(func(tx *bolt.Tx) error {
		var err error
		view, err = Board(tx, chat, user, now)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	v, ok := view.(*View)
	if !ok {
		t.Fatalf("board returned %T", view)
	}
	return v
}

func seedInventory(t *testing.T, db *bolt.DB, chat, user int64, good string, qty int) {
	t.Helper()
	if err := db.Update(func(tx *bolt.Tx) error {
		s, err := openStore(tx, chat, true)
		if err != nil {
			return err
		}
		p, ok, err := s.loadPlayer(user)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("player %d not joined", user)
		}
		p.add(good, qty)
		return s.savePlayer(user, p)
	}); err != nil {
		t.Fatal(err)
	}
}

func seedWallet(t *testing.T, db *bolt.DB, chat, user int64, available, score int) {
	t.Helper()
	abs := chat
	if abs < 0 {
		abs = -abs
	}
	key := fmt.Sprintf("r:%020d:%020d", abs, user)
	raw := fmt.Sprintf(`{"available":%d,"reserved":0,"score":%d}`, available, score)
	if err := db.Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists([]byte("reputation"))
		if err != nil {
			return err
		}
		return b.Put([]byte(key), []byte(raw))
	}); err != nil {
		t.Fatal(err)
	}
}

func TestBoardShopButtonUsesSharedGrammarAndDoesNotCharge(t *testing.T) {
	db := testEngine(t)
	now := time.Unix(1770000000, 0).UTC()
	board := apply(t, db, 100, 1, "board", "board", "", now)
	for _, button := range board.Buttons {
		if button.Verb != "shop" {
			continue
		}
		shop := apply(t, db, 100, 1, "callback", button.Verb, button.Arg, now)
		if len(shop.Buttons) != 5 || !strings.Contains(shop.Text, "Магазин") {
			t.Fatalf("shop receipt: %+v", shop)
		}
		for _, item := range shop.Buttons {
			verb, _, err := Parse(item.Verb + " " + item.Arg)
			if err != nil || verb != item.Verb {
				t.Fatalf("invalid shop button: %+v: %v", item, err)
			}
		}
		view := boardView(t, db, 100, 1, now)
		if view.Wallet.Available != 10 || !view.Me.StarterSeed || view.Me.Plots != 1 || view.Me.Inventory[goodSeed] != 0 {
			t.Fatalf("viewing shop changed the wallet or yard: %+v %+v", view.Wallet, view.Me)
		}
		return
	}
	t.Fatal("board has no shop button")
}

func TestBoardHidesExpiredListingsBeforeSettlement(t *testing.T) {
	db := testEngine(t)
	now := time.Unix(1770000000, 0).UTC()
	apply(t, db, 100, 1, "seed", "seed", "1", now)
	apply(t, db, 100, 1, "sell", "sell", "seed 1 3", now)
	before := boardView(t, db, 100, 1, now.Add(kListingTTL-time.Second))
	if len(before.Market.Listings) != 1 || before.Me.Reserved[goodSeed] != 1 {
		t.Fatalf("listing disappeared before its deadline: %+v %+v", before.Market, before.Me)
	}
	after := boardView(t, db, 100, 1, now.Add(kListingTTL))
	if len(after.Market.Listings) != 0 || after.Me.Reserved[goodSeed] != 0 || after.Me.Inventory[goodSeed] != 1 {
		t.Fatalf("expired listing still shown or stock missing: %+v %+v", after.Market, after.Me)
	}
}

func TestPlantSettlementAndReplay(t *testing.T) {
	db := testEngine(t)
	chat, user := int64(100), int64(1)
	now := time.Unix(1770000000, 0).UTC()

	apply(t, db, chat, user, "join", "join", "", now)
	apply(t, db, chat, user, "plant", "plant", "", now)

	engine := gameengine.New(db, map[string]gameengine.Module{Module: Apply})
	replay := gameengine.Action{ChatID: chat, UserID: user, Key: "plant", Verb: "plant", Arg: "", Now: now.Add(time.Hour)}
	if _, err := engine.Apply(Module, replay); err != nil {
		t.Fatalf("replay: %v", err)
	}
	if err := db.View(func(tx *bolt.Tx) error {
		s, err := openStore(tx, chat, false)
		if err != nil {
			return err
		}
		p, ok, err := s.loadPlayer(user)
		if err != nil || !ok {
			return err
		}
		if p.PlotJobs[0] == nil || p.StarterSeed {
			t.Errorf("replay changed state: seed=%v job=%v", p.StarterSeed, p.PlotJobs[0])
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Apply(Module, gameengine.Action{ChatID: chat, UserID: user, Key: "plant", Verb: "collect", Now: now}); err == nil {
		t.Fatal("reused key accepted a different action")
	}

	settled := advance(t, db, now.Add(5*time.Hour))
	if len(settled) != 1 || !strings.Contains(settled[0].Result.Text, "зерно") {
		t.Fatalf("scheduled settlement: %+v", settled)
	}
	view := boardView(t, db, chat, user, now.Add(5*time.Hour))
	if view.Me == nil || view.Me.Inventory[goodGrain] != kPlantYield {
		t.Fatalf("inventory after settlement: %+v", view.Me)
	}
	if view.Wallet.Available != 10 {
		t.Fatalf("collect charged reputation: %+v", view.Wallet)
	}
	advance(t, db, now.Add(6*time.Hour))
	view = boardView(t, db, chat, user, now.Add(6*time.Hour))
	if view.Me.Inventory[goodGrain] != kPlantYield {
		t.Fatalf("duplicate settlement: %+v", view.Me.Inventory)
	}
}

func TestConcurrentBuyFillsListingOnce(t *testing.T) {
	db := testEngine(t)
	chat, seller := int64(200), int64(1)
	buyers := []int64{2, 3}
	now := time.Unix(1770100000, 0).UTC()

	apply(t, db, chat, seller, "join", "join", "", now)
	apply(t, db, chat, seller, "plant", "plant", "", now)
	advance(t, db, now.Add(4*time.Hour))
	for _, buyer := range buyers {
		apply(t, db, chat, buyer, "join", "join", "", now.Add(4*time.Hour))
	}
	sellResult := apply(t, db, chat, seller, "sell", "sell", "grain 3 2", now.Add(4*time.Hour))
	if !strings.Contains(sellResult.Text, "l1") {
		t.Fatalf("listing receipt: %q", sellResult.Text)
	}

	engine := gameengine.New(db, map[string]gameengine.Module{Module: Apply})
	var wg sync.WaitGroup
	results := make([]error, len(buyers))
	for i, buyer := range buyers {
		wg.Add(1)
		go func(i int, buyer int64) {
			defer wg.Done()
			_, results[i] = engine.Apply(Module, gameengine.Action{
				ChatID: chat, UserID: buyer, Key: fmt.Sprintf("buy-%d", buyer),
				Verb: "buy", Arg: "l1", Now: now.Add(4 * time.Hour),
			})
		}(i, buyer)
	}
	wg.Wait()
	successes := 0
	for _, err := range results {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("successful fills = %d, errors = %v", successes, results)
	}
	view := boardView(t, db, chat, seller, now.Add(4*time.Hour))
	if view.Wallet.Available != 10+6 {
		t.Fatalf("seller wallet: %+v", view.Wallet)
	}
	if len(view.Market.Listings) != 0 {
		t.Fatalf("listing remained after full fill: %+v", view.Market.Listings)
	}
	stock := 0
	for _, yard := range view.Yards {
		stock += yard.Inventory[goodGrain]
	}
	if stock != 3 {
		t.Fatalf("grain not delivered exactly once: %d", stock)
	}
}

func TestFailedPurchaseLeavesStateUnchanged(t *testing.T) {
	db := testEngine(t)
	chat, seller, buyer := int64(300), int64(1), int64(2)
	now := time.Unix(1770200000, 0).UTC()

	apply(t, db, chat, seller, "join", "join", "", now)
	apply(t, db, chat, buyer, "join", "join", "", now)
	seedInventory(t, db, chat, seller, goodGrain, 4)
	apply(t, db, chat, seller, "sell", "sell", "grain 4 3", now)

	before := boardView(t, db, chat, buyer, now)
	if err := applyErr(t, db, chat, buyer, "buy", "buy", "l1 4", now); err == nil {
		t.Fatal("purchase without funds accepted")
	}
	after := boardView(t, db, chat, buyer, now)
	if after.Wallet.Available != before.Wallet.Available {
		t.Fatalf("failed purchase changed buyer wallet: %d -> %d", before.Wallet.Available, after.Wallet.Available)
	}
	sellerView := boardView(t, db, chat, seller, now)
	if len(sellerView.Market.Listings) != 1 || sellerView.Market.Listings[0].Qty != 4 {
		t.Fatalf("failed purchase changed listing: %+v", sellerView.Market.Listings)
	}
	stale, err := applyStaleBuy(t, db, chat, buyer, "l1:4:99", now)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stale.Text, "Условия изменились") {
		t.Fatalf("stale fill text: %q", stale.Text)
	}
	sellerView = boardView(t, db, chat, seller, now)
	if len(sellerView.Market.Listings) != 1 || sellerView.Market.Listings[0].Qty != 4 {
		t.Fatalf("stale fill changed listing: %+v", sellerView.Market.Listings)
	}
}

func applyStaleBuy(t *testing.T, db *bolt.DB, chat, user int64, arg string, now time.Time) (gameengine.Result, error) {
	t.Helper()
	engine := gameengine.New(db, map[string]gameengine.Module{Module: Apply})
	return engine.Apply(Module, gameengine.Action{
		ChatID: chat, UserID: user, Key: "stale-buy", Verb: "buy", Arg: arg, Now: now,
	})
}

func TestListingExpiryReturnsStock(t *testing.T) {
	db := testEngine(t)
	chat, seller := int64(400), int64(1)
	now := time.Unix(1770300000, 0).UTC()

	apply(t, db, chat, seller, "join", "join", "", now)
	seedInventory(t, db, chat, seller, goodGrain, 5)
	apply(t, db, chat, seller, "sell", "sell", "grain 5 2", now)

	later := now.Add(25 * time.Hour)
	apply(t, db, chat, seller, "market", "market", "", later)
	view := boardView(t, db, chat, seller, later)
	if len(view.Market.Listings) != 0 {
		t.Fatalf("expired listing visible: %+v", view.Market.Listings)
	}
	if view.Me == nil || view.Me.Inventory[goodGrain] != 5 {
		t.Fatalf("expiry did not return stock: %+v", view.Me)
	}
}

func TestBuildConsumesInputsAndWalletsAtomically(t *testing.T) {
	db := testEngine(t)
	chat, user := int64(500), int64(1)
	now := time.Unix(1770400000, 0).UTC()
	seedWallet(t, db, chat, user, 35, 0)

	apply(t, db, chat, user, "join", "join", "", now)
	if err := applyErr(t, db, chat, user, "build-mill", "build", "mill", now); err == nil {
		t.Fatal("mill built without grain")
	}
	view := boardView(t, db, chat, user, now)
	if view.Wallet.Available != 35 {
		t.Fatalf("failed build charged wallet: %+v", view.Wallet)
	}

	seedInventory(t, db, chat, user, goodGrain, 2)
	apply(t, db, chat, user, "build-mill", "build", "mill", now)
	view = boardView(t, db, chat, user, now)
	if view.Wallet.Available != 5 {
		t.Fatalf("mill cost: %+v", view.Wallet)
	}
	if view.Me.Inventory[goodGrain] != 0 {
		t.Fatalf("mill did not consume grain: %+v", view.Me.Inventory)
	}
	if err := applyErr(t, db, chat, user, "early-mill", "mill", "1", now); err == nil {
		t.Fatal("job started before construction finished")
	}
	later := now.Add(2 * time.Hour)
	seedInventory(t, db, chat, user, goodGrain, 2)
	apply(t, db, chat, user, "ready-mill", "mill", "1", later)
	view = boardView(t, db, chat, user, later)
	if view.Wallet.Available != 5 {
		t.Fatalf("job changed wallet: %+v", view.Wallet)
	}
	if view.Me.Inventory[goodGrain] != 0 {
		t.Fatalf("mill job did not consume grain: %+v", view.Me.Inventory)
	}
}

func TestLeaseAcceptanceSettlesToInitiator(t *testing.T) {
	db := testEngine(t)
	chat, owner, renter := int64(600), int64(1), int64(2)
	now := time.Unix(1770500000, 0).UTC()

	seedWallet(t, db, chat, owner, 100, 36)
	seedWallet(t, db, chat, renter, 100, 0)
	apply(t, db, chat, owner, "join", "join", "", now)
	apply(t, db, chat, owner, "plant", "plant", "", now)
	advance(t, db, now.Add(4*time.Hour))
	apply(t, db, chat, owner, "build", "build", "mill", now.Add(4*time.Hour))
	ready := now.Add(5 * time.Hour)
	apply(t, db, chat, owner, "offer", "offer", "mill 1 12 5", ready)

	stale, err := acceptOffer(t, db, chat, renter, "r1:99", ready)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stale.Text, "Условия изменились") {
		t.Fatalf("stale lease text: %q", stale.Text)
	}
	accepted, err := acceptOffer(t, db, chat, renter, "r1:1", ready)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(accepted.Text, "Аренда началась") {
		t.Fatalf("lease receipt: %q", accepted.Text)
	}

	apply(t, db, chat, renter, "rjoin", "join", "", ready)
	apply(t, db, chat, renter, "rplant", "plant", "", ready)
	advance(t, db, ready.Add(4*time.Hour))
	start := ready.Add(4 * time.Hour)
	apply(t, db, chat, renter, "rmill", "mill", "u1:m1", start)
	advance(t, db, start.Add(2*time.Hour))

	view := boardView(t, db, chat, renter, start.Add(2*time.Hour))
	if view.Me.Inventory[goodFlour] != kMillOutput {
		t.Fatalf("leased mill output: %+v", view.Me.Inventory)
	}
	ownerView := boardView(t, db, chat, owner, start.Add(2*time.Hour))
	if ownerView.Me.Inventory[goodFlour] != 0 {
		t.Fatalf("lease output credited owner: %+v", ownerView.Me.Inventory)
	}
	if ownerView.Wallet.Available != 75 {
		t.Fatalf("owner rent credit: %+v", ownerView.Wallet)
	}
	if view.Wallet.Available != 95 {
		t.Fatalf("renter rent debit: %+v", view.Wallet)
	}
	if err := applyErr(t, db, chat, owner, "owner-early", "mill", "1", start.Add(2*time.Hour)); err == nil {
		t.Fatal("owner started a leased slot")
	}
}

func acceptOffer(t *testing.T, db *bolt.DB, chat, user int64, arg string, now time.Time) (gameengine.Result, error) {
	t.Helper()
	engine := gameengine.New(db, map[string]gameengine.Module{Module: Apply})
	return engine.Apply(Module, gameengine.Action{
		ChatID: chat, UserID: user, Key: "accept-" + arg, Verb: "accept", Arg: arg, Now: now,
	})
}

func TestCoopDailyBudgetAndBakery(t *testing.T) {
	db := testEngine(t)
	chat, user := int64(700), int64(1)
	now := time.Unix(1770600000, 0).UTC()

	apply(t, db, chat, user, "join", "join", "", now)
	seedInventory(t, db, chat, user, goodGrain, 3)
	apply(t, db, chat, user, "deliver1", "deliver", "grain", now)
	view := boardView(t, db, chat, user, now)
	if view.Wallet.Available != 11 || view.Coop.BudgetRemaining != 11 || view.Coop.FillsToday != 1 {
		t.Fatalf("first grain delivery: wallet=%+v coop=%+v", view.Wallet, view.Coop)
	}
	seedInventory(t, db, chat, user, goodGrain, 3)
	apply(t, db, chat, user, "deliver2", "deliver", "grain", now)
	seedInventory(t, db, chat, user, goodGrain, 3)
	if err := applyErr(t, db, chat, user, "deliver3", "deliver", "grain", now); err == nil {
		t.Fatal("third daily delivery accepted")
	}
	view = boardView(t, db, chat, user, now)
	if view.Wallet.Available != 12 || view.Me.Inventory[goodGrain] != 3 {
		t.Fatalf("daily limit changed state: wallet=%+v inventory=%+v", view.Wallet, view.Me.Inventory)
	}

	nextDay := now.Add(24 * time.Hour)
	apply(t, db, chat, user, "deliver-next", "deliver", "grain", nextDay)
	view = boardView(t, db, chat, user, nextDay)
	if view.Wallet.Available != 13 || view.Coop.FillsToday != 1 || view.Coop.BudgetRemaining != 11 {
		t.Fatalf("daily reset: wallet=%+v coop=%+v", view.Wallet, view.Coop)
	}

	if err := db.Update(func(tx *bolt.Tx) error {
		s, err := openStore(tx, chat, true)
		if err != nil {
			return err
		}
		if err := s.setMetaInt("bread_total", kBakeryDeliveries-1); err != nil {
			return err
		}
		p, _, err := s.loadPlayer(user)
		if err != nil {
			return err
		}
		p.add(goodBread, kCoopBreadQty)
		return s.savePlayer(user, p)
	}); err != nil {
		t.Fatal(err)
	}
	apply(t, db, chat, user, "deliver-bread", "deliver", "bread", nextDay)
	view = boardView(t, db, chat, user, nextDay)
	if !view.Coop.BakeryUnlocked || view.Coop.BreadDelivered != kBakeryDeliveries {
		t.Fatalf("bakery milestone: %+v", view.Coop)
	}
}

func TestParseGrammar(t *testing.T) {
	cases := []struct {
		input string
		verb  string
		arg   string
		bad   bool
	}{
		{"", "board", "", false},
		{"/farm", "board", "", false},
		{"/farm plant p1", "plant", "1", false},
		{"sell grain 3 2", "sell", "grain 3 2", false},
		{"buy l1 2", "buy", "l1 2", false},
		{"offer mill 1 12 5", "offer", "mill 1 12 5", false},
		{"mill u123 1", "mill", "u123:1", false},
		{"deliver bread", "deliver", "bread", false},
		{"deliver stone", "", "", true},
		{"settle", "", "", true},
		{"unknown", "", "", true},
	}
	for _, tc := range cases {
		verb, arg, err := Parse(tc.input)
		if tc.bad {
			if err == nil {
				t.Errorf("%q accepted", tc.input)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: %v", tc.input, err)
			continue
		}
		if verb != tc.verb || arg != tc.arg {
			t.Errorf("%q = (%q,%q), want (%q,%q)", tc.input, verb, arg, tc.verb, tc.arg)
		}
	}
}

func TestInternalVerbRequiresSchedulerActor(t *testing.T) {
	db := testEngine(t)
	chat, user := int64(800), int64(1)
	now := time.Unix(1770700000, 0).UTC()
	apply(t, db, chat, user, "join", "join", "", now)
	if err := applyErr(t, db, chat, user, "typed-settle", "settle", "u1", now); err == nil {
		t.Fatal("typed settle accepted")
	}
}

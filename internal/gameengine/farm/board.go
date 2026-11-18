package farm

import (
	"fmt"
	"sort"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/veschin/bidlobot/internal/domain/reputation"
	"github.com/veschin/bidlobot/internal/gameengine"
)

const (
	yardWidth  = 3
	yardHeight = 3
)

// View is the JSON-ready shared farm board for one viewer. Positions are
// fixed per yard so the Mini App can render the same spatial layout.
type View struct {
	ChatID         int64          `json:"chat_id"`
	ViewerID       int64          `json:"viewer_id"`
	Now            int64          `json:"now"`
	Joined         bool           `json:"joined"`
	Wallet         WalletView     `json:"wallet"`
	MayRentOut     bool           `json:"may_rent_out"`
	Prices         ShopPrices     `json:"prices"`
	Me             *YardView      `json:"me,omitempty"`
	Yards          []YardView     `json:"yards"`
	OpenCapacity   int            `json:"open_capacity"`
	NextCompletion *DeadlineView  `json:"next_completion,omitempty"`
	Market         MarketView     `json:"market"`
	Coop           CoopView       `json:"coop"`
	Milestones     MilestonesView `json:"milestones"`
	Recovery       string         `json:"recovery,omitempty"`
	Instructions   string         `json:"instructions"`
}

type ShopPrices struct {
	Seed      int `json:"seed"`
	Plot      int `json:"plot"`
	Mill      int `json:"mill"`
	MillGrain int `json:"mill_grain"`
	Oven      int `json:"oven"`
	OvenFlour int `json:"oven_flour"`
}

type WalletView struct {
	Available int `json:"available"`
	Reserved  int `json:"reserved"`
	Score     int `json:"score"`
}

type DeadlineView struct {
	At    int64  `json:"at"`
	Label string `json:"label"`
}

type YardView struct {
	OwnerID     int64          `json:"owner_id"`
	OwnerName   string         `json:"owner_name,omitempty"`
	YardX       int            `json:"yard_x"`
	YardY       int            `json:"yard_y"`
	Width       int            `json:"width"`
	Height      int            `json:"height"`
	Plots       int            `json:"plots"`
	StarterSeed bool           `json:"starter_seed"`
	DailyFills  int            `json:"daily_fills"`
	Inventory   map[string]int `json:"inventory"`
	Reserved    map[string]int `json:"reserved"`
	Tiles       []TileView     `json:"tiles"`
}

type TileView struct {
	ID          string     `json:"id"`
	Kind        string     `json:"kind"`
	OwnerID     int64      `json:"owner_id"`
	X           int        `json:"x"`
	Y           int        `json:"y"`
	State       string     `json:"state"`
	Built       bool       `json:"built"`
	ReadyAt     int64      `json:"ready_at,omitempty"`
	AvailableAt int64      `json:"available_at,omitempty"`
	Job         *JobView   `json:"job,omitempty"`
	Lease       *LeaseView `json:"lease,omitempty"`
	Offer       *OfferView `json:"offer,omitempty"`
	Actions     []string   `json:"actions,omitempty"`
}

type JobView struct {
	Kind    string `json:"kind"`
	ActorID int64  `json:"actor_id"`
	Start   int64  `json:"start"`
	End     int64  `json:"end"`
	Good    string `json:"good"`
	Qty     int    `json:"qty"`
}

type LeaseView struct {
	RenterID int64 `json:"renter_id"`
	Price    int   `json:"price"`
	Start    int64 `json:"start"`
	End      int64 `json:"end"`
}

type OfferView struct {
	ID        string `json:"id"`
	OwnerID   int64  `json:"owner_id"`
	Kind      string `json:"kind"`
	Slot      int    `json:"slot"`
	Hours     int    `json:"hours"`
	Price     int    `json:"price"`
	ExpiresAt int64  `json:"expires_at"`
	Version   int    `json:"version"`
}

type MarketView struct {
	Listings []ListingView `json:"listings"`
	Recent   []RecentView  `json:"recent"`
}

type ListingView struct {
	ID        string `json:"id"`
	SellerID  int64  `json:"seller_id"`
	Good      string `json:"good"`
	Qty       int    `json:"qty"`
	UnitPrice int    `json:"unit_price"`
	CreatedAt int64  `json:"created_at"`
	ExpiresAt int64  `json:"expires_at"`
	Version   int    `json:"version"`
}

type RecentView struct {
	Good      string `json:"good"`
	Qty       int    `json:"qty"`
	UnitPrice int    `json:"unit_price"`
	At        int64  `json:"at"`
}

type CoopView struct {
	Day             string         `json:"day"`
	BudgetTotal     int            `json:"budget_total"`
	BudgetRemaining int            `json:"budget_remaining"`
	GrainQty        int            `json:"grain_qty"`
	GrainPay        int            `json:"grain_pay"`
	GrainLeft       int            `json:"grain_left"`
	BreadQty        int            `json:"bread_qty"`
	BreadPay        int            `json:"bread_pay"`
	BreadLeft       int            `json:"bread_left"`
	Bread2Left      int            `json:"bread2_left"`
	BakeryUnlocked  bool           `json:"bakery_unlocked"`
	BreadDelivered  int            `json:"bread_delivered"`
	BakeryNeeded    int            `json:"bakery_needed"`
	FillsToday      int            `json:"fills_today"`
	MaxFills        int            `json:"max_fills"`
	History         []CoopFillView `json:"history"`
}

type CoopFillView struct {
	At     int64  `json:"at"`
	UserID int64  `json:"user_id"`
	Good   string `json:"good"`
	Qty    int    `json:"qty"`
	Pay    int    `json:"pay"`
}

type MilestonesView struct {
	Bakery         bool `json:"bakery"`
	BreadDelivered int  `json:"bread_delivered"`
	BreadNeeded    int  `json:"bread_needed"`
}

type slotKey struct {
	owner int64
	kind  string
	slot  int
}

// Board reads one chat without walking other chats. Expired listings and
// offers are projected away; their stock is included in effective holdings
// even before the next write transaction persists the release.
func Board(tx *bolt.Tx, chatID, userID int64, now time.Time) (any, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	unix := now.Unix()
	s, err := openStore(tx, chatID, false)
	if err != nil {
		return nil, err
	}
	account, err := gameengine.Wallet(tx, chatID, userID)
	if err != nil {
		return nil, err
	}
	view := &View{
		ChatID:       chatID,
		ViewerID:     userID,
		Now:          unix,
		Wallet:       WalletView{Available: account.Available, Reserved: account.Reserved, Score: account.Score},
		MayRentOut:   account.Score >= reputation.MasterThreshold,
		Prices:       ShopPrices{Seed: kSeedPrice, Plot: kPlotPrice, Mill: kMillCostRep, MillGrain: kMillCostGrain, Oven: kOvenCostRep, OvenFlour: kOvenCostFlour},
		Instructions: helpText(),
		Yards:        []YardView{},
		Market:       MarketView{Listings: []ListingView{}, Recent: []RecentView{}},
		Coop: CoopView{
			Day:             dayISO(unix),
			BudgetTotal:     kCoopBudget,
			BudgetRemaining: kCoopBudget,
			GrainQty:        kCoopGrainQty,
			GrainPay:        kCoopGrainPay,
			GrainLeft:       kCoopGrainCapacity,
			BreadQty:        kCoopBreadQty,
			BreadPay:        kCoopBreadPay,
			BreadLeft:       kCoopBreadCapacity,
			MaxFills:        kCoopMaxFills,
			History:         []CoopFillView{},
		},
		Milestones: MilestonesView{BreadNeeded: kBakeryDeliveries},
	}
	if s == nil {
		return view, nil
	}

	type entry struct {
		id int64
		p  *player
	}
	var entries []entry
	today := s.today(unix)
	pending := map[int64]map[string]int{}
	expiredReturn := map[int64]map[string]int{}
	reserved := map[int64]map[string]int{}
	if err := s.eachPlayer(func(id int64, p *player) error {
		entries = append(entries, entry{id, p})
		for _, j := range p.PlotJobs {
			addPending(pending, j, unix)
		}
		for _, kind := range []string{"mill", "oven"} {
			slots := p.slots(kind)
			for i := range slots {
				addPending(pending, slots[i].Job, unix)
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	if err := s.eachListing(func(l *listing) error {
		if l.ExpiresAt <= unix {
			if expiredReturn[l.SellerID] == nil {
				expiredReturn[l.SellerID] = map[string]int{}
			}
			expiredReturn[l.SellerID][l.Good] += l.Qty
			return nil
		}
		if reserved[l.SellerID] == nil {
			reserved[l.SellerID] = map[string]int{}
		}
		reserved[l.SellerID][l.Good] += l.Qty
		return nil
	}); err != nil {
		return nil, err
	}
	offers := map[slotKey]*offer{}
	oc := s.offers.Cursor()
	for k, v := oc.First(); k != nil; k, v = oc.Next() {
		var o offer
		if err := decodeJSON(v, &o); err != nil {
			return nil, err
		}
		if o.ExpiresAt <= unix {
			continue
		}
		copied := o
		offers[slotKey{o.OwnerID, o.Kind, o.Slot}] = &copied
	}

	sort.Slice(entries, func(i, j int) bool {
		if entries[i].p.Yard != entries[j].p.Yard {
			return entries[i].p.Yard < entries[j].p.Yard
		}
		return entries[i].id < entries[j].id
	})
	var next *DeadlineView
	for _, e := range entries {
		yard := buildYard(e.id, e.p, userID, unix, today, pending[e.id], expiredReturn[e.id], reserved[e.id], offers, &next)
		view.Yards = append(view.Yards, yard)
		view.OpenCapacity += openCapacity(e.id, e.p, unix, offers)
		if e.id == userID {
			view.Joined = true
			me := yard
			view.Me = &me
			view.Coop.FillsToday = s.dailyFills(e.p, unix)
			view.Recovery = recoveryFor(e.p, account.Available)
		}
	}
	view.NextCompletion = next
	if view.Yards == nil {
		view.Yards = []YardView{}
	}

	view.Market = buildMarket(s, unix)
	view.Coop.BudgetRemaining = s.effectiveCoopInt("coop_budget", unix)
	view.Coop.GrainLeft = s.effectiveCoopInt("coop_grain_left", unix)
	view.Coop.BreadLeft = s.effectiveCoopInt("coop_bread_left", unix)
	view.Coop.BreadDelivered = s.metaInt("bread_total")
	view.Coop.BakeryUnlocked = s.bakeryUnlocked()
	if view.Coop.BakeryUnlocked {
		view.Coop.Bread2Left = s.effectiveCoopInt("coop_bread2_left", unix)
	}
	view.Coop.BakeryNeeded = kBakeryDeliveries
	if history, err := s.recentCoop(8); err == nil {
		for _, fill := range history {
			view.Coop.History = append(view.Coop.History, CoopFillView{At: fill.At, UserID: fill.UserID, Good: fill.Good, Qty: fill.Qty, Pay: fill.Pay})
		}
	}
	if view.Coop.History == nil {
		view.Coop.History = []CoopFillView{}
	}
	view.Milestones = MilestonesView{
		Bakery:         view.Coop.BakeryUnlocked,
		BreadDelivered: view.Coop.BreadDelivered,
		BreadNeeded:    kBakeryDeliveries,
	}
	return view, nil
}

func addPending(pending map[int64]map[string]int, j *job, now int64) {
	if j == nil || j.End > now {
		return
	}
	if pending[j.ActorID] == nil {
		pending[j.ActorID] = map[string]int{}
	}
	pending[j.ActorID][j.Good] += j.Qty
}

func buildYard(ownerID int64, p *player, viewerID, now, today int64, pending, expiredReturn, reserved map[string]int, offers map[slotKey]*offer, next **DeadlineView) YardView {
	dailyFills := p.DailyFills
	if p.DailyFillsDay != today {
		dailyFills = 0
	}
	yard := YardView{
		OwnerID:     ownerID,
		YardX:       p.Yard % 4,
		YardY:       p.Yard / 4,
		Width:       yardWidth,
		Height:      yardHeight,
		Plots:       p.Plots,
		StarterSeed: p.StarterSeed,
		DailyFills:  dailyFills,
		Inventory:   map[string]int{},
		Reserved:    map[string]int{},
		Tiles:       make([]TileView, 0, kMaxPlots+kMaxMills+kMaxOvens),
	}
	for _, good := range goodsOrder {
		yard.Inventory[good] = p.count(good) + pending[good] + expiredReturn[good]
	}
	for good, qty := range reserved {
		if qty > 0 {
			yard.Reserved[good] = qty
		}
	}
	for i := 0; i < kMaxPlots; i++ {
		tile := TileView{ID: "p" + itoa(i+1), Kind: "plot", OwnerID: ownerID, X: i, Y: 0, Built: true}
		if i >= p.Plots {
			tile.State = "locked"
			yard.Tiles = append(yard.Tiles, tile)
			continue
		}
		tile.State = "idle"
		if j := p.PlotJobs[i]; j != nil {
			tile.Job = jobView(j)
			tile.AvailableAt = j.End
			if j.End <= now {
				tile.State = "ready"
				if j.ActorID == viewerID {
					tile.Actions = append(tile.Actions, "collect")
				}
			} else {
				tile.State = "job"
				updateNext(next, j.End, "Сбор: "+goodLabel(j.Good))
			}
		} else if ownerID == viewerID {
			tile.Actions = append(tile.Actions, "plant p"+itoa(i+1))
		}
		yard.Tiles = append(yard.Tiles, tile)
	}
	for _, kind := range []string{"mill", "oven"} {
		slots := p.slots(kind)
		baseY := 1
		if kind == "oven" {
			baseY = 2
		}
		for i := range slots {
			slot := &slots[i]
			prefix := "m"
			if kind == "oven" {
				prefix = "o"
			}
			tile := TileView{
				ID:      prefix + itoa(i+1),
				Kind:    kind,
				OwnerID: ownerID,
				X:       i,
				Y:       baseY,
				Built:   slot.Built,
				ReadyAt: slot.ReadyAt,
			}
			o := offers[slotKey{ownerID, kind, i + 1}]
			switch {
			case !slot.Built:
				tile.State = "unbuilt"
			case slot.ReadyAt > now:
				tile.State = "building"
				tile.AvailableAt = slot.ReadyAt
				updateNext(next, slot.ReadyAt, "Стройка: "+assetLabel(kind)+" "+itoa(i+1))
			default:
				tile.State = "idle"
				if slot.Job != nil {
					tile.Job = jobView(slot.Job)
					tile.AvailableAt = slot.Job.End
					if slot.Job.End <= now {
						tile.State = "ready"
						if slot.Job.ActorID == viewerID {
							tile.Actions = append(tile.Actions, "collect")
						}
					} else {
						tile.State = "job"
						updateNext(next, slot.Job.End, "Сбор: "+goodLabel(slot.Job.Good))
					}
				}
				if slot.Lease != nil && now >= slot.Lease.End {
					slot.Lease = nil
				}
				if slot.Lease != nil {
					tile.Lease = &LeaseView{RenterID: slot.Lease.RenterID, Price: slot.Lease.Price, Start: slot.Lease.Start, End: slot.Lease.End}
					if tile.State == "idle" {
						tile.State = "leased"
						tile.AvailableAt = slot.Lease.End
					}
					if slot.Lease.RenterID == viewerID && slot.Job == nil {
						if tile.State != "ready" {
							tile.Actions = append(tile.Actions, kind+" "+"u"+i64(ownerID)+":"+tile.ID)
						}
					}
				}
				if o != nil {
					tile.Offer = offerView(o)
					if tile.State == "idle" {
						tile.State = "offered"
						tile.AvailableAt = o.ExpiresAt
					}
					if ownerID == viewerID {
						tile.Actions = append(tile.Actions, "unoffer "+o.ID)
					} else {
						tile.Actions = append(tile.Actions, fmt.Sprintf("accept %s:%d", o.ID, o.Version))
					}
				}
				if ownerID == viewerID && slot.Job == nil && slot.Lease == nil && o == nil {
					tile.Actions = append(tile.Actions, kind+" "+tile.ID)
				}
			}
			yard.Tiles = append(yard.Tiles, tile)
		}
	}
	return yard
}

func jobView(j *job) *JobView {
	return &JobView{Kind: j.Kind, ActorID: j.ActorID, Start: j.Start, End: j.End, Good: j.Good, Qty: j.Qty}
}

func offerView(o *offer) *OfferView {
	return &OfferView{ID: o.ID, OwnerID: o.OwnerID, Kind: o.Kind, Slot: o.Slot, Hours: o.Hours, Price: o.Price, ExpiresAt: o.ExpiresAt, Version: o.Version}
}

func updateNext(next **DeadlineView, at int64, label string) {
	if at <= 0 {
		return
	}
	if *next == nil || at < (*next).At {
		*next = &DeadlineView{At: at, Label: label}
	}
}

func openCapacity(ownerID int64, p *player, now int64, offers map[slotKey]*offer) int {
	count := 0
	for i := 0; i < p.Plots && i < kMaxPlots; i++ {
		if p.PlotJobs[i] == nil {
			count++
		}
	}
	for _, kind := range []string{"mill", "oven"} {
		slots := p.slots(kind)
		for i := range slots {
			slot := &slots[i]
			leaseFree := slot.Lease == nil || now >= slot.Lease.End
			if slot.Built && slot.ReadyAt <= now && slot.Job == nil && leaseFree && offers[slotKey{ownerID, kind, i + 1}] == nil {
				count++
			}
		}
	}
	return count
}

func assetLabel(kind string) string {
	if kind == "oven" {
		return "печь"
	}
	return "мельница"
}

func buildMarket(s *store, now int64) MarketView {
	market := MarketView{Listings: []ListingView{}, Recent: []RecentView{}}
	if listings, err := s.sortedListings(); err == nil {
		for _, l := range listings {
			if l.ExpiresAt <= now {
				continue
			}
			market.Listings = append(market.Listings, ListingView{
				ID: l.ID, SellerID: l.SellerID, Good: l.Good, Qty: l.Qty, UnitPrice: l.UnitPrice,
				CreatedAt: l.CreatedAt, ExpiresAt: l.ExpiresAt, Version: l.Version,
			})
		}
	}
	var recent []RecentView
	for _, good := range goodsOrder {
		if sales, err := s.recentSales(good); err == nil {
			for _, item := range sales {
				recent = append(recent, RecentView{Good: item.Good, Qty: item.Qty, UnitPrice: item.UnitPrice, At: item.At})
			}
		}
	}
	sort.Slice(recent, func(i, j int) bool { return recent[i].At > recent[j].At })
	if len(recent) > 12 {
		recent = recent[:12]
	}
	market.Recent = append(market.Recent, recent...)
	return market
}

func recoveryFor(p *player, available int) string {
	if available > 0 {
		return ""
	}
	if p.StarterSeed {
		return "Репа на нуле. Стартовое семя ещё не использовано: /farm plant."
	}
	for _, good := range []string{goodGrain, goodFlour, goodBread, goodSeed} {
		if p.count(good) > 0 {
			return "Репа на нуле. Товар в амбаре можно продать: /farm sell " + good + " 1 1."
		}
	}
	return "Репы и товара нет. Заработайте репу у других участников или возьмите заказ кооператива: /farm coop."
}

func itoa(n int) string  { return fmt.Sprintf("%d", n) }
func i64(n int64) string { return fmt.Sprintf("%d", n) }

func helpText() string {
	return strings.Join([]string{
		"Ферма. Грядка даёт 3 зерна за 4 часа. Мельница: 2 зерна -> 1 мука за 2 часа. Печь: 2 муки -> 2 хлеба за 2 часа.",
		fmt.Sprintf("Магазин: /farm shop. Семя стоит %d репы, грядка %d, мельница %d и %d ед. зерна, печь %d и %d ед. муки.", kSeedPrice, kPlotPrice, kMillCostRep, kMillCostGrain, kOvenCostRep, kOvenCostFlour),
		"Работа: /farm plant [1-3], /farm mill [1-2], /farm oven [1-2], /farm collect.",
		"Рынок: /farm sell grain 3 2, /farm buy <id> [количество], /farm cancel <id>. Цена и количество - целые положительные числа.",
		"Аренда: /farm offer mill 1 12 5 (нужен титул «Мастер»), /farm accept <id>, /farm unoffer <id>. Работа в аренде: /farm mill u<владелец>:m<слот>.",
		"Кооператив: /farm deliver grain (3 зерна -> 1 репа) или /farm deliver bread (2 хлеба -> 4 репы). Две сдачи в сутки.",
		"Дворы: /farm yard [id], рынок: /farm market, кооператив: /farm coop.",
	}, "\n")
}

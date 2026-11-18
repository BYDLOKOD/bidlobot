package farm

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/veschin/bidlobot/internal/gameengine"
)

// Module is the engine module name and the per-chat bucket key.
const Module = "farm"

const (
	goodSeed  = "seed"
	goodGrain = "grain"
	goodFlour = "flour"
	goodBread = "bread"

	kSeedPrice = 4
	kPlotPrice = 20
	kMaxPlots  = 3
	kMaxMills  = 2
	kMaxOvens  = 2

	kMillCostRep   = 30
	kMillCostGrain = 2
	kOvenCostRep   = 40
	kOvenCostFlour = 1

	kBuildTime = time.Hour
	kPlantTime = 4 * time.Hour
	kMillTime  = 2 * time.Hour
	kOvenTime  = 2 * time.Hour

	kPlantYield = 3
	kMillInput  = 2
	kMillOutput = 1
	kOvenInput  = 2
	kOvenOutput = 2

	kListingTTL     = 24 * time.Hour
	kMaxListings    = 10
	kOfferTTL       = time.Hour
	kOfferMinHours  = 6
	kOfferHours     = 12
	kOfferMaxHours  = 24
	kMaxOrderQty    = 1000
	kMaxUnitPrice   = 100000
	kMaxSeedBuy     = 50
	kMaxOfferPrice  = 100000
	kRecentSales    = 5
	kSalesStored    = 60
	kCoopHistoryMax = 50

	kCoopBudget          = 12
	kCoopGrainQty        = 3
	kCoopGrainPay        = 1
	kCoopBreadQty        = 2
	kCoopBreadPay        = 4
	kCoopMaxFills        = 2
	kCoopGrainCapacity   = 12
	kCoopBreadCapacity   = 3
	kBakeryDeliveries    = 10
	kBakeryBreadContract = 3
	secondsPerDay        = 86400
)

// Farm goods exposed by the market and inventory views.
var goodsOrder = []string{goodGrain, goodFlour, goodBread, goodSeed}

func goodKnown(good string) bool {
	switch good {
	case goodGrain, goodFlour, goodBread, goodSeed:
		return true
	}
	return false
}

func goodLabel(good string) string {
	switch good {
	case goodGrain:
		return "зерно"
	case goodFlour:
		return "мука"
	case goodBread:
		return "хлеб"
	case goodSeed:
		return "семена"
	}
	return good
}

type job struct {
	Kind    string `json:"kind"`
	ActorID int64  `json:"actor_id"`
	Start   int64  `json:"start"`
	End     int64  `json:"end"`
	Good    string `json:"good"`
	Qty     int    `json:"qty"`
}

type lease struct {
	RenterID int64 `json:"renter_id"`
	Price    int   `json:"price"`
	Start    int64 `json:"start"`
	End      int64 `json:"end"`
}

type slot struct {
	Built   bool   `json:"built"`
	ReadyAt int64  `json:"ready_at"`
	Job     *job   `json:"job,omitempty"`
	Lease   *lease `json:"lease,omitempty"`
}

type player struct {
	Yard          int            `json:"yard"`
	Plots         int            `json:"plots"`
	StarterSeed   bool           `json:"starter_seed"`
	Inventory     map[string]int `json:"inventory"`
	PlotJobs      []*job         `json:"plot_jobs"`
	Mills         []slot         `json:"mills"`
	Ovens         []slot         `json:"ovens"`
	DailyFillsDay int64          `json:"daily_fills_day"`
	DailyFills    int            `json:"daily_fills"`
}

func newPlayer(yard int) *player {
	return &player{
		Yard:        yard,
		Plots:       1,
		StarterSeed: true,
		Inventory:   map[string]int{},
		PlotJobs:    make([]*job, kMaxPlots),
		Mills:       make([]slot, kMaxMills),
		Ovens:       make([]slot, kMaxOvens),
	}
}

func (p *player) normalize() {
	if p.Plots < 1 {
		p.Plots = 1
	}
	if p.Plots > kMaxPlots {
		p.Plots = kMaxPlots
	}
	if p.Inventory == nil {
		p.Inventory = map[string]int{}
	}
	for len(p.PlotJobs) < kMaxPlots {
		p.PlotJobs = append(p.PlotJobs, nil)
	}
	if len(p.PlotJobs) > kMaxPlots {
		p.PlotJobs = p.PlotJobs[:kMaxPlots]
	}
	for len(p.Mills) < kMaxMills {
		p.Mills = append(p.Mills, slot{})
	}
	if len(p.Mills) > kMaxMills {
		p.Mills = p.Mills[:kMaxMills]
	}
	for len(p.Ovens) < kMaxOvens {
		p.Ovens = append(p.Ovens, slot{})
	}
	if len(p.Ovens) > kMaxOvens {
		p.Ovens = p.Ovens[:kMaxOvens]
	}
}

func (p *player) count(good string) int { return p.Inventory[good] }

func (p *player) add(good string, qty int) {
	if qty <= 0 {
		return
	}
	if p.Inventory == nil {
		p.Inventory = map[string]int{}
	}
	p.Inventory[good] += qty
}

func (p *player) take(good string, qty int) bool {
	if qty <= 0 || p.Inventory[good] < qty {
		return false
	}
	p.Inventory[good] -= qty
	if p.Inventory[good] == 0 {
		delete(p.Inventory, good)
	}
	return true
}

func (p *player) freePlot() int {
	for i := 0; i < p.Plots && i < kMaxPlots; i++ {
		if p.PlotJobs[i] == nil {
			return i + 1
		}
	}
	return 0
}

func (p *player) freeSlot(kind string) *slot {
	slots := p.slots(kind)
	for i := range slots {
		if !slots[i].Built {
			return &slots[i]
		}
	}
	return nil
}

func (p *player) slots(kind string) []slot {
	if kind == "oven" {
		return p.Ovens
	}
	return p.Mills
}

func (p *player) slotAt(kind string, index int) *slot {
	slots := p.slots(kind)
	if index < 1 || index > len(slots) {
		return nil
	}
	return &slots[index-1]
}

func (p *player) clearExpiredLeases(now int64) {
	for kind := range map[string]bool{"mill": true, "oven": true} {
		slots := p.slots(kind)
		for i := range slots {
			if slots[i].Lease != nil && now >= slots[i].Lease.End {
				slots[i].Lease = nil
			}
		}
	}
}

type listing struct {
	ID        string `json:"id"`
	SellerID  int64  `json:"seller_id"`
	Good      string `json:"good"`
	Qty       int    `json:"qty"`
	UnitPrice int    `json:"unit_price"`
	CreatedAt int64  `json:"created_at"`
	ExpiresAt int64  `json:"expires_at"`
	Version   int    `json:"version"`
}

type offer struct {
	ID        string `json:"id"`
	OwnerID   int64  `json:"owner_id"`
	Kind      string `json:"kind"`
	Slot      int    `json:"slot"`
	Hours     int    `json:"hours"`
	Price     int    `json:"price"`
	CreatedAt int64  `json:"created_at"`
	ExpiresAt int64  `json:"expires_at"`
	Version   int    `json:"version"`
}

type sale struct {
	Good      string `json:"good"`
	Qty       int    `json:"qty"`
	UnitPrice int    `json:"unit_price"`
	At        int64  `json:"at"`
}

type coopFill struct {
	At     int64  `json:"at"`
	UserID int64  `json:"user_id"`
	Good   string `json:"good"`
	Qty    int    `json:"qty"`
	Pay    int    `json:"pay"`
}

// store is one chat's farm root and its fixed child buckets. Every mutation
// in this package happens inside the engine's single write transaction.
type store struct {
	tx       *bolt.Tx
	chatID   int64
	root     *bolt.Bucket
	players  *bolt.Bucket
	listings *bolt.Bucket
	offers   *bolt.Bucket
	sales    *bolt.Bucket
	coop     *bolt.Bucket
	meta     *bolt.Bucket
}

var (
	bPlayers  = []byte("players")
	bListings = []byte("listings")
	bOffers   = []byte("offers")
	bSales    = []byte("sales")
	bCoop     = []byte("coop")
	bMeta     = []byte("meta")
)

func openStore(tx *bolt.Tx, chatID int64, write bool) (*store, error) {
	root, err := gameengine.State(tx, chatID, Module)
	if err != nil {
		return nil, err
	}
	if root == nil {
		return nil, nil
	}
	s := &store{tx: tx, chatID: chatID, root: root}
	names := []struct {
		name  []byte
		field **bolt.Bucket
	}{
		{bPlayers, &s.players},
		{bListings, &s.listings},
		{bOffers, &s.offers},
		{bSales, &s.sales},
		{bCoop, &s.coop},
		{bMeta, &s.meta},
	}
	for _, item := range names {
		var b *bolt.Bucket
		if write {
			b, err = root.CreateBucketIfNotExists(item.name)
		} else {
			b = root.Bucket(item.name)
		}
		if err != nil {
			return nil, err
		}
		if b == nil {
			return nil, nil
		}
		*item.field = b
	}
	return s, nil
}

func playerKey(userID int64) []byte {
	return binary.BigEndian.AppendUint64(nil, uint64(userID))
}

func decodePlayerID(key []byte) int64 {
	if len(key) != 8 {
		return 0
	}
	return int64(binary.BigEndian.Uint64(key))
}

func (s *store) loadPlayer(userID int64) (*player, bool, error) {
	raw := s.players.Get(playerKey(userID))
	if raw == nil {
		return nil, false, nil
	}
	var p player
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, false, fmt.Errorf("farm: decode player %d: %w", userID, err)
	}
	p.normalize()
	return &p, true, nil
}

func (s *store) savePlayer(userID int64, p *player) error {
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return s.players.Put(playerKey(userID), raw)
}

// ensurePlayer returns the stored player, creating the single starter grant
// exactly once per chat member.
func (s *store) ensurePlayer(userID int64) (*player, bool, error) {
	p, ok, err := s.loadPlayer(userID)
	if err != nil || ok {
		return p, false, err
	}
	yard := s.metaInt("next_yard")
	if err := s.setMetaInt("next_yard", yard+1); err != nil {
		return nil, false, err
	}
	p = newPlayer(yard)
	if err := s.savePlayer(userID, p); err != nil {
		return nil, false, err
	}
	return p, true, nil
}

func (s *store) eachPlayer(fn func(userID int64, p *player) error) error {
	c := s.players.Cursor()
	for k, v := c.First(); k != nil; k, v = c.Next() {
		var p player
		if err := json.Unmarshal(v, &p); err != nil {
			return fmt.Errorf("farm: decode player: %w", err)
		}
		p.normalize()
		if err := fn(decodePlayerID(k), &p); err != nil {
			return err
		}
	}
	return nil
}

func (s *store) metaInt(key string) int {
	raw := s.meta.Get([]byte(key))
	if raw == nil {
		return 0
	}
	n, err := strconv.Atoi(string(raw))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

func (s *store) metaInt64(key string) int64 {
	raw := s.meta.Get([]byte(key))
	if raw == nil {
		return 0
	}
	n, err := strconv.ParseInt(string(raw), 10, 64)
	if err != nil {
		return 0
	}
	return n
}

func (s *store) setMetaInt(key string, value int) error {
	return s.meta.Put([]byte(key), []byte(strconv.Itoa(value)))
}

func (s *store) setMetaInt64(key string, value int64) error {
	return s.meta.Put([]byte(key), []byte(strconv.FormatInt(value, 10)))
}

func (s *store) nextCounter(key string) (int, error) {
	next := s.metaInt(key) + 1
	if err := s.setMetaInt(key, next); err != nil {
		return 0, err
	}
	return next, nil
}

func (s *store) wallet(userID int64) (int, int, error) {
	account, err := gameengine.Wallet(s.tx, s.chatID, userID)
	if err != nil {
		return 0, 0, err
	}
	return account.Available, account.Score, nil
}

func (s *store) debit(userID int64, amount int) error {
	if amount <= 0 {
		return nil
	}
	available, _, err := s.wallet(userID)
	if err != nil {
		return err
	}
	if available < amount {
		return gameengine.Reject(fmt.Sprintf(
			"Не хватает репы: нужно %d, доступно %d. Посадите стартовое семя, продайте товар на рынке или сдайте заказ кооперативу.",
			amount, available))
	}
	return gameengine.Adjust(s.tx, s.chatID, userID, -amount, 0)
}

func (s *store) credit(userID int64, amount int) error {
	if amount <= 0 {
		return nil
	}
	return gameengine.Adjust(s.tx, s.chatID, userID, amount, 0)
}

func (s *store) listingByID(id string) (*listing, bool, error) {
	raw := s.listings.Get([]byte(id))
	if raw == nil {
		return nil, false, nil
	}
	var l listing
	if err := json.Unmarshal(raw, &l); err != nil {
		return nil, false, fmt.Errorf("farm: decode listing %s: %w", id, err)
	}
	return &l, true, nil
}

func (s *store) saveListing(l *listing) error {
	raw, err := json.Marshal(l)
	if err != nil {
		return err
	}
	return s.listings.Put([]byte(l.ID), raw)
}

func (s *store) offerByID(id string) (*offer, bool, error) {
	raw := s.offers.Get([]byte(id))
	if raw == nil {
		return nil, false, nil
	}
	var o offer
	if err := json.Unmarshal(raw, &o); err != nil {
		return nil, false, fmt.Errorf("farm: decode offer %s: %w", id, err)
	}
	return &o, true, nil
}

func (s *store) saveOffer(o *offer) error {
	raw, err := json.Marshal(o)
	if err != nil {
		return err
	}
	return s.offers.Put([]byte(o.ID), raw)
}

// expireListings returns remaining stock to the seller. Cancellation and
// expiry release only the units that were never bought.
func (s *store) expireListings(now int64) error {
	c := s.listings.Cursor()
	for k, v := c.First(); k != nil; k, v = c.Next() {
		var l listing
		if err := json.Unmarshal(v, &l); err != nil {
			return fmt.Errorf("farm: decode listing: %w", err)
		}
		if now < l.ExpiresAt {
			continue
		}
		p, ok, err := s.loadPlayer(l.SellerID)
		if err != nil {
			return err
		}
		if ok {
			p.add(l.Good, l.Qty)
			if err := s.savePlayer(l.SellerID, p); err != nil {
				return err
			}
		}
		if err := c.Delete(); err != nil {
			return err
		}
	}
	return nil
}

func (s *store) expireOffers(now int64) error {
	c := s.offers.Cursor()
	for k, v := c.First(); k != nil; k, v = c.Next() {
		var o offer
		if err := json.Unmarshal(v, &o); err != nil {
			return fmt.Errorf("farm: decode offer: %w", err)
		}
		if now < o.ExpiresAt {
			continue
		}
		if err := c.Delete(); err != nil {
			return err
		}
	}
	return nil
}

func (s *store) liveListingCount(sellerID int64) (int, error) {
	count := 0
	c := s.listings.Cursor()
	for k, v := c.First(); k != nil; k, v = c.Next() {
		var l listing
		if err := json.Unmarshal(v, &l); err != nil {
			return 0, err
		}
		if l.SellerID == sellerID {
			count++
		}
	}
	return count, nil
}

func (s *store) today(now int64) int64 { return now / secondsPerDay }

// rollDay funds the co-op for the new UTC day. Unused budget is discarded
// instead of carried forward, and finite contracts are re-opened.
func (s *store) rollDay(now int64) error {
	today := s.today(now)
	if s.metaInt64("coop_day") == today {
		return nil
	}
	if err := s.setMetaInt64("coop_day", today); err != nil {
		return err
	}
	if err := s.setMetaInt("coop_budget", kCoopBudget); err != nil {
		return err
	}
	if err := s.setMetaInt("coop_grain_left", kCoopGrainCapacity); err != nil {
		return err
	}
	if err := s.setMetaInt("coop_bread_left", kCoopBreadCapacity); err != nil {
		return err
	}
	return s.setMetaInt("coop_bread2_left", kBakeryBreadContract)
}

func safeMul(a, b int) (int, bool) {
	if a < 0 || b < 0 {
		return 0, false
	}
	if a != 0 && b > math.MaxInt/a {
		return 0, false
	}
	return a * b, true
}

// Parse turns a typed `/farm ...` tail into the verb and argument the engine
// forwards to Apply. Text and callback arguments stay in one grammar.
func Parse(input string) (verb, arg string, err error) {
	text := strings.TrimSpace(input)
	if len(text) >= len("/farm") && strings.EqualFold(text[:len("/farm")], "/farm") {
		text = strings.TrimSpace(text[len("/farm"):])
	}
	if text == "" {
		return "board", "", nil
	}
	fields := strings.Fields(text)
	verb = strings.ToLower(fields[0])
	args := fields[1:]
	bad := func(text string) (string, string, error) {
		return "", "", gameengine.Reject(text)
	}
	one := func() bool { return len(args) == 1 }
	none := func() bool { return len(args) == 0 }
	positive := func(raw string, max int) (int, bool) {
		n, conv := strconv.Atoi(raw)
		return n, conv == nil && n > 0 && n <= max
	}

	switch verb {
	case "board", "help", "shop", "market", "coop", "collect", "plot", "join":
		if !none() {
			return bad("Лишние аргументы. Откройте /farm help.")
		}
		return verb, "", nil
	case "seed":
		if none() {
			return verb, "", nil
		}
		if !one() {
			return bad("Количество семян: от 1 до 50.")
		}
		n, ok := positive(args[0], kMaxSeedBuy)
		if !ok {
			return bad("Количество семян: от 1 до 50.")
		}
		return verb, strconv.Itoa(n), nil
	case "build":
		if !one() {
			return bad("Что строить: mill или oven.")
		}
		kind := normalizeKind(args[0])
		if kind == "" {
			return bad("Что строить: mill или oven.")
		}
		return verb, kind, nil
	case "plant":
		if none() {
			return verb, "", nil
		}
		if !one() {
			return bad("Номер грядки: 1, 2 или 3.")
		}
		plotArg := strings.TrimPrefix(strings.ToLower(args[0]), "p")
		n, ok := positive(plotArg, kMaxPlots)
		if !ok {
			return bad("Номер грядки: 1, 2 или 3.")
		}
		return verb, strconv.Itoa(n), nil
	case "mill", "oven":
		if none() {
			return verb, "", nil
		}
		if one() {
			return verb, args[0], nil
		}
		if len(args) == 2 {
			return verb, args[0] + ":" + args[1], nil
		}
		return bad("Слот: 1 или 2; для аренды: u<владелец>:m<слот>.")
	case "sell":
		if len(args) != 3 {
			return bad("Формат: /farm sell grain 3 2 — товар, количество, цена.")
		}
		good := strings.ToLower(args[0])
		if !goodKnown(good) {
			return bad("Товар: grain, flour, bread или seed.")
		}
		qty, ok := positive(args[1], kMaxOrderQty)
		if !ok {
			return bad("Количество: от 1 до 1000.")
		}
		price, ok := positive(args[2], kMaxUnitPrice)
		if !ok {
			return bad("Цена за единицу: от 1 до 100000.")
		}
		return verb, good + " " + strconv.Itoa(qty) + " " + strconv.Itoa(price), nil
	case "buy":
		if len(args) < 1 || len(args) > 2 {
			return bad("Формат: /farm buy <id> [количество].")
		}
		if len(args) == 2 {
			if _, ok := positive(args[1], kMaxOrderQty); !ok {
				return bad("Количество: от 1 до 1000.")
			}
		}
		return verb, strings.Join(args, " "), nil
	case "cancel", "unoffer":
		if !one() {
			return bad("Укажите номер объявления.")
		}
		return verb, args[0], nil
	case "accept":
		if !one() {
			return bad("Укажите номер предложения аренды.")
		}
		return verb, args[0], nil
	case "offer":
		if len(args) != 4 {
			return bad("Формат: /farm offer mill 1 12 5 — слот, часы, цена.")
		}
		kind := normalizeKind(args[0])
		if kind == "" {
			return bad("Слот: mill или oven.")
		}
		slot, ok := positive(args[1], kMaxMills)
		if !ok {
			return bad("Слот: 1 или 2.")
		}
		hours, ok := positive(args[2], kOfferMaxHours)
		if !ok || (hours != kOfferMinHours && hours != kOfferHours && hours != kOfferMaxHours) {
			return bad("Срок аренды: 6, 12 или 24 часа.")
		}
		price, ok := positive(args[3], kMaxOfferPrice)
		if !ok {
			return bad("Цена аренды: от 1 до 100000.")
		}
		return verb, kind + " " + strconv.Itoa(slot) + " " + strconv.Itoa(hours) + " " + strconv.Itoa(price), nil
	case "deliver":
		if !one() {
			return bad("Заказ: grain или bread.")
		}
		good := strings.ToLower(args[0])
		if good != goodGrain && good != goodBread {
			return bad("Заказ: grain или bread.")
		}
		return verb, good, nil
	case "yard":
		if none() {
			return verb, "", nil
		}
		if !one() {
			return bad("Укажите один Telegram ID.")
		}
		return verb, args[0], nil
	case "start":
		if !one() {
			return bad("Укажите слот: p1, m1, o1 или u<владелец>:m<слот>.")
		}
		return verb, args[0], nil
	case "settle", "leave":
		return bad("Недоступное действие.")
	default:
		return bad("Неизвестная команда. /farm help — список действий.")
	}
}

func normalizeKind(raw string) string {
	switch strings.ToLower(raw) {
	case "mill", "m", "мельница":
		return "mill"
	case "oven", "o", "печь":
		return "oven"
	}
	return ""
}

func decodeJSON(raw []byte, value any) error { return json.Unmarshal(raw, value) }

func deadlineText(unix int64) string {
	return time.Unix(unix, 0).UTC().Format("02.01 15:04 UTC")
}

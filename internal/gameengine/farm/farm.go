package farm

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/veschin/bidlobot/internal/gameengine"
)

type harvest struct {
	Good string
	Qty  int
}

var _ gameengine.Module = Apply

// Apply is registered with the engine as the farm module. Client actions
// carry a positive actor. The scheduler calls only the internal "settle"
// verb with UserID 0 and the actor encoded in Arg.
func Apply(tx *bolt.Tx, a gameengine.Action) (gameengine.Result, error) {
	verb := strings.ToLower(strings.TrimSpace(a.Verb))
	if a.UserID == 0 {
		if verb != "settle" {
			return gameengine.Result{}, gameengine.Reject("Действие недоступно.")
		}
		return applyScheduled(tx, a)
	}
	if verb == "settle" || verb == "leave" {
		return gameengine.Result{}, gameengine.Reject("Действие недоступно.")
	}
	if a.Now.IsZero() {
		return gameengine.Result{}, gameengine.Reject("Действие недоступно. Откройте игру заново.")
	}
	s, err := openStore(tx, a.ChatID, true)
	if err != nil {
		return gameengine.Result{}, err
	}
	if s == nil {
		return gameengine.Result{}, fmt.Errorf("farm: write state unavailable")
	}
	now := a.Now.Unix()
	if err := s.rollDay(now); err != nil {
		return gameengine.Result{}, err
	}
	if err := s.expireListings(now); err != nil {
		return gameengine.Result{}, err
	}
	if err := s.expireOffers(now); err != nil {
		return gameengine.Result{}, err
	}
	settled, err := s.settleActor(a.UserID, now)
	if err != nil {
		return gameengine.Result{}, err
	}
	result, err := s.dispatch(a, verb, now, settled)
	if err != nil {
		return gameengine.Result{}, err
	}
	if len(settled) > 0 && verb != "collect" && verb != "help" {
		result.Text = harvestLine("Собрано", settled) + " " + result.Text
	}
	return result, nil
}

func (s *store) dispatch(a gameengine.Action, verb string, now int64, settled []harvest) (gameengine.Result, error) {
	switch verb {
	case "", "board", "join":
		return s.boardReceipt(a.UserID, now, verb == "join")
	case "help":
		return gameengine.Result{Text: helpText(), Buttons: []gameengine.Button{
			{Label: "Ферма", Verb: "board"},
		}}, nil
	case "shop":
		return shopReceipt(), nil
	case "seed":
		return s.buySeeds(a.UserID, a.Arg, now)
	case "plot":
		return s.buyPlot(a.UserID)
	case "build":
		return s.build(a.UserID, a.Arg, now)
	case "plant":
		return s.plant(a.UserID, a.Arg, now)
	case "mill", "oven":
		return s.startOwnJob(a.UserID, verb, a.Arg, now)
	case "start":
		return s.startByTile(a.UserID, a.Arg, now)
	case "collect":
		return s.collectReceipt(a.UserID, now, settled)
	case "market":
		return s.marketReceipt(a.UserID)
	case "sell":
		return s.sell(a.UserID, a.Arg, now)
	case "buy":
		return s.buy(a.UserID, a.Arg, now)
	case "cancel":
		return s.cancelListing(a.UserID, a.Arg)
	case "offer":
		return s.createOffer(a.UserID, a.Arg, now)
	case "unoffer":
		return s.cancelOffer(a.UserID, a.Arg)
	case "accept":
		return s.acceptOffer(a.UserID, a.Arg, now)
	case "coop":
		return s.coopReceipt(a.UserID, now)
	case "deliver":
		return s.deliver(a.UserID, a.Arg, now)
	case "yard":
		return s.yardReceipt(a.UserID, a.Arg, now)
	default:
		return gameengine.Result{}, gameengine.Reject("Неизвестное действие. /farm help - список команд.")
	}
}

func applyScheduled(tx *bolt.Tx, a gameengine.Action) (gameengine.Result, error) {
	actor, err := parseActorArg(a.Arg)
	if err != nil {
		return gameengine.Result{}, err
	}
	if a.Now.IsZero() {
		return gameengine.Result{}, fmt.Errorf("farm: settle without deadline")
	}
	s, err := openStore(tx, a.ChatID, true)
	if err != nil {
		return gameengine.Result{}, err
	}
	if s == nil {
		return gameengine.Result{}, nil
	}
	settled, err := s.settleActor(actor, a.Now.Unix())
	if err != nil {
		return gameengine.Result{}, err
	}
	if len(settled) == 0 {
		return gameengine.Result{}, nil
	}
	return gameengine.Result{Text: harvestLine("Готово", settled)}, nil
}

func parseActorArg(raw string) (int64, error) {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "u")
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("farm: invalid settle actor %q", raw)
	}
	return id, nil
}

// settleActor credits every finished job the actor started, including jobs
// running on another member's leased mill or oven. Cleared jobs are removed
// exactly once; repeated settlement is a no-op.
func (s *store) settleActor(actorID, now int64) ([]harvest, error) {
	actor, ok, err := s.loadPlayer(actorID)
	if err != nil || !ok {
		return nil, err
	}
	byGood := map[string]int{}
	changed := false
	updates := map[int64][]byte{}
	c := s.players.Cursor()
	for k, v := c.First(); k != nil; k, v = c.Next() {
		ownerID := decodePlayerID(k)
		var p *player
		if ownerID == actorID {
			p = actor
		} else {
			var other player
			if err := json.Unmarshal(v, &other); err != nil {
				return nil, fmt.Errorf("farm: decode player: %w", err)
			}
			other.normalize()
			p = &other
		}
		ownerChanged := false
		for i, j := range p.PlotJobs {
			if dueFor(j, actorID, now) {
				byGood[j.Good] += j.Qty
				actor.add(j.Good, j.Qty)
				p.PlotJobs[i] = nil
				ownerChanged, changed = true, true
			}
		}
		for _, kind := range []string{"mill", "oven"} {
			slots := p.slots(kind)
			for i := range slots {
				j := slots[i].Job
				if dueFor(j, actorID, now) {
					byGood[j.Good] += j.Qty
					actor.add(j.Good, j.Qty)
					slots[i].Job = nil
					ownerChanged, changed = true, true
				}
			}
		}
		if ownerChanged && ownerID != actorID {
			raw, err := json.Marshal(p)
			if err != nil {
				return nil, err
			}
			updates[ownerID] = raw
		}
	}
	for ownerID, raw := range updates {
		if err := s.players.Put(playerKey(ownerID), raw); err != nil {
			return nil, err
		}
	}
	if !changed {
		return nil, nil
	}
	if err := s.savePlayer(actorID, actor); err != nil {
		return nil, err
	}
	harvests := make([]harvest, 0, len(goodsOrder))
	for _, good := range goodsOrder {
		if qty := byGood[good]; qty > 0 {
			harvests = append(harvests, harvest{Good: good, Qty: qty})
		}
	}
	return harvests, nil
}

func dueFor(j *job, actorID, now int64) bool {
	return j != nil && j.ActorID == actorID && now >= j.End
}

func harvestLine(prefix string, harvests []harvest) string {
	parts := make([]string, 0, len(harvests))
	for _, h := range harvests {
		parts = append(parts, fmt.Sprintf("%s +%d", goodLabel(h.Good), h.Qty))
	}
	return prefix + ": " + strings.Join(parts, ", ") + "."
}

func (s *store) scheduleJob(actorID, ownerID int64, tile string, end int64) error {
	key := fmt.Sprintf("settle:%d:%d:%s:%d", actorID, ownerID, tile, end)
	return gameengine.Schedule(s.tx, Module, gameengine.Action{
		ChatID: s.chatID,
		Key:    key,
		Verb:   "settle",
		Arg:    "u" + strconv.FormatInt(actorID, 10),
		Now:    time.Unix(end, 0).UTC(),
	})
}

// Leave removes only unaccepted offers. Accepted leases and running jobs
// continue in the chat world after the member is gone.
func Leave(tx *bolt.Tx, chatID, userID int64) error {
	if chatID <= 0 || userID <= 0 {
		return nil
	}
	s, err := openStore(tx, chatID, true)
	if err != nil || s == nil {
		return err
	}
	c := s.offers.Cursor()
	for k, v := c.First(); k != nil; k, v = c.Next() {
		var o offer
		if err := json.Unmarshal(v, &o); err != nil {
			return fmt.Errorf("farm: decode offer: %w", err)
		}
		if o.OwnerID == userID {
			if err := c.Delete(); err != nil {
				return err
			}
		}
	}
	return nil
}

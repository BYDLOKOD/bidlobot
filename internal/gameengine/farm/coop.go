package farm

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/veschin/bidlobot/internal/gameengine"
)

func (s *store) effectiveCoopInt(key string, now int64) int {
	if s.metaInt64("coop_day") != s.today(now) {
		switch key {
		case "coop_budget":
			return kCoopBudget
		case "coop_grain_left":
			return kCoopGrainCapacity
		case "coop_bread_left":
			return kCoopBreadCapacity
		case "coop_bread2_left":
			return kBakeryBreadContract
		}
	}
	return s.metaInt(key)
}

func (s *store) bakeryUnlocked() bool {
	return s.metaInt("bread_total") >= kBakeryDeliveries
}

func (s *store) dailyFills(p *player, now int64) int {
	if p.DailyFillsDay != s.today(now) {
		return 0
	}
	return p.DailyFills
}

func (s *store) deliver(userID int64, arg string, now int64) (gameengine.Result, error) {
	good := strings.ToLower(strings.TrimSpace(arg))
	if good != goodGrain && good != goodBread {
		return gameengine.Result{}, gameengine.Reject("Заказ: /farm deliver grain или /farm deliver bread.")
	}
	p, _, err := s.ensurePlayer(userID)
	if err != nil {
		return gameengine.Result{}, err
	}
	today := s.today(now)
	if p.DailyFillsDay != today {
		p.DailyFillsDay = today
		p.DailyFills = 0
	}
	if p.DailyFills >= kCoopMaxFills {
		return gameengine.Result{}, gameengine.Reject("Лимит: две сдачи в сутки. Следующая - после 00:00 UTC; сейчас товар можно продать на рынке.")
	}
	budget := s.metaInt("coop_budget")
	var need int
	var pay int
	var leftKey string
	if good == goodGrain {
		need, pay, leftKey = kCoopGrainQty, kCoopGrainPay, "coop_grain_left"
	} else {
		need, pay = kCoopBreadQty, kCoopBreadPay
		leftKey = "coop_bread_left"
		if s.metaInt("coop_bread_left") <= 0 && s.bakeryUnlocked() && s.metaInt("coop_bread2_left") > 0 {
			leftKey = "coop_bread2_left"
		}
	}
	if s.metaInt(leftKey) <= 0 {
		if good == goodGrain {
			return gameengine.Result{}, gameengine.Reject("Партия зерна на сегодня закрыта. Сдайте хлеб: /farm deliver bread - или продайте зерно на рынке.")
		}
		return gameengine.Result{}, gameengine.Reject("Партии хлеба на сегодня закрыты. Приходите после 00:00 UTC или продайте хлеб на рынке.")
	}
	if budget < pay {
		return gameengine.Result{}, gameengine.Reject(fmt.Sprintf(
			"Бюджет кооператива на сегодня исчерпан (осталось %d). Приходите после 00:00 UTC или продайте товар на рынке.", budget))
	}
	if p.count(good) < need {
		return gameengine.Result{}, gameengine.Reject(fmt.Sprintf(
			"В амбаре только %s %d, для сдачи нужно %d.", goodLabel(good), p.count(good), need))
	}
	p.take(good, need)
	p.DailyFills++
	if err := s.setMetaInt(leftKey, s.metaInt(leftKey)-1); err != nil {
		return gameengine.Result{}, err
	}
	if err := s.setMetaInt("coop_budget", budget-pay); err != nil {
		return gameengine.Result{}, err
	}
	if err := s.credit(userID, pay); err != nil {
		return gameengine.Result{}, err
	}
	milestone := ""
	if good == goodBread {
		total := s.metaInt("bread_total") + 1
		if err := s.setMetaInt("bread_total", total); err != nil {
			return gameengine.Result{}, err
		}
		if total == kBakeryDeliveries {
			milestone = " Общая пекарня открылась: кооператив принимает вторую партию хлеба."
		}
	}
	if err := s.savePlayer(userID, p); err != nil {
		return gameengine.Result{}, err
	}
	if err := s.appendCoop(coopFill{At: now, UserID: userID, Good: good, Qty: need, Pay: pay}); err != nil {
		return gameengine.Result{}, err
	}
	available, _, err := s.wallet(userID)
	if err != nil {
		return gameengine.Result{}, err
	}
	return gameengine.Result{
		Text: fmt.Sprintf("Сдано кооперативу: %s ×%d. Получено репы: %d. Баланс: %d. Бюджет: %d. Ваши сдачи сегодня: %d/2.%s",
			goodLabel(good), need, pay, available, s.metaInt("coop_budget"), p.DailyFills, milestone),
		Buttons: []gameengine.Button{
			{Label: "Кооператив", Verb: "coop"},
			{Label: "Рынок", Verb: "market"},
		},
	}, nil
}

func (s *store) appendCoop(fill coopFill) error {
	seq, err := s.nextCounter("coop_seq")
	if err != nil {
		return err
	}
	raw, err := json.Marshal(fill)
	if err != nil {
		return err
	}
	if err := s.coop.Put([]byte(fmt.Sprintf("%016d", seq)), raw); err != nil {
		return err
	}
	c := s.coop.Cursor()
	count := 0
	var oldest [][]byte
	for k, _ := c.First(); k != nil; k, _ = c.Next() {
		count++
		if count > kCoopHistoryMax {
			oldest = append(oldest, append([]byte(nil), k...))
		}
	}
	for _, k := range oldest {
		if err := s.coop.Delete(k); err != nil {
			return err
		}
	}
	return nil
}

func (s *store) recentCoop(limit int) ([]coopFill, error) {
	var all []coopFill
	c := s.coop.Cursor()
	for k, v := c.First(); k != nil; k, v = c.Next() {
		var fill coopFill
		if err := json.Unmarshal(v, &fill); err != nil {
			return nil, err
		}
		all = append(all, fill)
	}
	if len(all) > limit {
		all = all[len(all)-limit:]
	}
	return all, nil
}

func (s *store) coopReceipt(userID int64, now int64) (gameengine.Result, error) {
	p, _, err := s.ensurePlayer(userID)
	if err != nil {
		return gameengine.Result{}, err
	}
	budget := s.metaInt("coop_budget")
	grainLeft := s.metaInt("coop_grain_left")
	breadLeft := s.metaInt("coop_bread_left")
	bakery := s.bakeryUnlocked()
	bread2 := 0
	if bakery {
		bread2 = s.metaInt("coop_bread2_left")
	}
	fills := s.dailyFills(p, now)
	var b strings.Builder
	fmt.Fprintf(&b, "Кооператив %s UTC. Бюджет: %d, осталось %d.\nЗерно: 3 → %d репы, партий %d.\nХлеб: 2 → %d репы, партий %d.",
		dayShort(now), kCoopBudget, budget, kCoopGrainPay, grainLeft, kCoopBreadPay, breadLeft)
	if bakery {
		fmt.Fprintf(&b, " Вторая партия хлеба: %d.", bread2)
	}
	fmt.Fprintf(&b, "\nВаши сдачи сегодня: %d/2.", fills)
	if !bakery {
		fmt.Fprintf(&b, "\nОбщая пекарня: %d/%d сдач хлеба.", s.metaInt("bread_total"), kBakeryDeliveries)
	}
	history, err := s.recentCoop(5)
	if err != nil {
		return gameengine.Result{}, err
	}
	if len(history) > 0 {
		b.WriteString("\nСдачи:")
		for _, fill := range history {
			fmt.Fprintf(&b, "\n%s · %d · %s ×%d · %d репы", deadlineText(fill.At), fill.UserID, goodLabel(fill.Good), fill.Qty, fill.Pay)
		}
	}
	b.WriteString("\nСдать: /farm deliver grain или /farm deliver bread.")
	buttons := []gameengine.Button{
		{Label: "Сдать зерно", Verb: "deliver", Arg: goodGrain},
		{Label: "Сдать хлеб", Verb: "deliver", Arg: goodBread},
		{Label: "Рынок", Verb: "market"},
		{Label: "Ферма", Verb: "board"},
	}
	return gameengine.Result{Text: b.String(), Buttons: buttons}, nil
}

func dayISO(unix int64) string {
	return time.Unix(unix, 0).UTC().Format("2006-01-02")
}

func dayShort(unix int64) string {
	return time.Unix(unix, 0).UTC().Format("02.01")
}

package farm

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/veschin/bidlobot/internal/gameengine"
)

func (s *store) sell(userID int64, arg string, now int64) (gameengine.Result, error) {
	parts := tokens(arg)
	if len(parts) != 3 {
		return gameengine.Result{}, gameengine.Reject("Формат: /farm sell grain 3 2 - товар, количество, цена.")
	}
	good := strings.ToLower(parts[0])
	if !goodKnown(good) {
		return gameengine.Result{}, gameengine.Reject("Товар: grain, flour, bread или seed.")
	}
	qty, ok := positiveToken(parts[1], kMaxOrderQty)
	if !ok {
		return gameengine.Result{}, gameengine.Reject("Количество: от 1 до 1000.")
	}
	price, ok := positiveToken(parts[2], kMaxUnitPrice)
	if !ok {
		return gameengine.Result{}, gameengine.Reject("Цена за единицу: от 1 до 100000.")
	}
	p, _, err := s.ensurePlayer(userID)
	if err != nil {
		return gameengine.Result{}, err
	}
	live, err := s.liveListingCount(userID)
	if err != nil {
		return gameengine.Result{}, err
	}
	if live >= kMaxListings {
		return gameengine.Result{}, gameengine.Reject("У вас 10 активных объявлений - предел. Снимите старое: /farm cancel <id>.")
	}
	if p.count(good) < qty {
		return gameengine.Result{}, gameengine.Reject(fmt.Sprintf(
			"В амбаре только %s %d, а выставляется %d. Уменьшите партию.", goodLabel(good), p.count(good), qty))
	}
	seq, err := s.nextCounter("next_listing")
	if err != nil {
		return gameengine.Result{}, err
	}
	p.take(good, qty)
	l := &listing{
		ID:        "l" + strconv.Itoa(seq),
		SellerID:  userID,
		Good:      good,
		Qty:       qty,
		UnitPrice: price,
		CreatedAt: now,
		ExpiresAt: now + int64(kListingTTL.Seconds()),
		Version:   1,
	}
	if err := s.saveListing(l); err != nil {
		return gameengine.Result{}, err
	}
	if err := s.savePlayer(userID, p); err != nil {
		return gameengine.Result{}, err
	}
	return gameengine.Result{
		Text: fmt.Sprintf("Выставлено: %s ×%d по %d. Номер: %s. Срок: %s. Снято со склада: %d.",
			goodLabel(good), qty, price, l.ID, deadlineText(l.ExpiresAt), qty),
		Buttons: []gameengine.Button{
			{Label: "Снять " + l.ID, Verb: "cancel", Arg: l.ID},
			{Label: "Рынок", Verb: "market"},
		},
	}, nil
}

func (s *store) buy(userID int64, arg string, now int64) (gameengine.Result, error) {
	parts := tokens(arg)
	if len(parts) < 1 || len(parts) > 3 {
		return gameengine.Result{}, gameengine.Reject("Формат: /farm buy <id> [количество].")
	}
	id := strings.ToLower(parts[0])
	qty := 0
	version := 0
	if len(parts) >= 2 {
		n, ok := positiveToken(parts[1], kMaxOrderQty)
		if !ok {
			return gameengine.Result{}, gameengine.Reject("Количество: от 1 до 1000.")
		}
		qty = n
	}
	if len(parts) == 3 {
		n, ok := positiveToken(parts[2], 1<<20)
		if !ok {
			return gameengine.Result{}, gameengine.Reject("Версия объявления указана неверно. Откройте рынок.")
		}
		version = n
	}
	l, ok, err := s.listingByID(id)
	if err != nil {
		return gameengine.Result{}, err
	}
	if !ok {
		return gameengine.Result{}, gameengine.Reject("Объявление не найдено или истекло. Откройте рынок: /farm market.")
	}
	if qty == 0 {
		qty = l.Qty
	}
	if version != 0 && version != l.Version {
		return s.staleListing(l, qty)
	}
	if l.SellerID == userID {
		return gameengine.Result{}, gameengine.Reject("Своё объявление купить нельзя. Снимите его: /farm cancel " + l.ID + ".")
	}
	if qty > l.Qty {
		return gameengine.Result{}, gameengine.Reject(fmt.Sprintf(
			"В объявлении осталось %s %d. Укажите не больше: /farm buy %s %d.",
			goodLabel(l.Good), l.Qty, l.ID, l.Qty))
	}
	total, ok := safeMul(qty, l.UnitPrice)
	if !ok {
		return gameengine.Result{}, gameengine.Reject("Сумма сделки слишком велика.")
	}
	available, _, err := s.wallet(userID)
	if err != nil {
		return gameengine.Result{}, err
	}
	if available < total {
		return gameengine.Result{}, gameengine.Reject(fmt.Sprintf(
			"Не хватает репы: нужно %d, доступно %d. Продайте товар на рынке или сдайте заказ кооперативу.",
			total, available))
	}
	buyer, _, err := s.ensurePlayer(userID)
	if err != nil {
		return gameengine.Result{}, err
	}
	if err := s.debit(userID, total); err != nil {
		return gameengine.Result{}, err
	}
	if err := s.credit(l.SellerID, total); err != nil {
		return gameengine.Result{}, err
	}
	buyer.add(l.Good, qty)
	l.Qty -= qty
	l.Version++
	if l.Qty == 0 {
		if err := s.listings.Delete([]byte(l.ID)); err != nil {
			return gameengine.Result{}, err
		}
	} else if err := s.saveListing(l); err != nil {
		return gameengine.Result{}, err
	}
	if err := s.savePlayer(userID, buyer); err != nil {
		return gameengine.Result{}, err
	}
	if err := s.appendSale(l.Good, qty, l.UnitPrice, now); err != nil {
		return gameengine.Result{}, err
	}
	available, _, err = s.wallet(userID)
	if err != nil {
		return gameengine.Result{}, err
	}
	remaining := ""
	if l.Qty > 0 {
		remaining = fmt.Sprintf(" Остаток объявления: %d.", l.Qty)
	}
	return gameengine.Result{
		Text: fmt.Sprintf("Куплено: %s ×%d по %d. Сумма: %d. Баланс: %d. Продавцу: +%d.%s",
			goodLabel(l.Good), qty, l.UnitPrice, total, available, total, remaining),
		Buttons: []gameengine.Button{
			{Label: "Рынок", Verb: "market"},
			{Label: "Ферма", Verb: "board"},
		},
	}, nil
}

// staleListing shows current terms and returns a fresh confirmation button
// without changing stock or wallets.
func (s *store) staleListing(l *listing, wanted int) (gameengine.Result, error) {
	if wanted > l.Qty {
		wanted = l.Qty
	}
	if wanted < 1 {
		wanted = 1
	}
	return gameengine.Result{
		Text: fmt.Sprintf("Условия изменились: осталось %s %d по %d, срок %s. Подтвердите покупку.",
			goodLabel(l.Good), l.Qty, l.UnitPrice, deadlineText(l.ExpiresAt)),
		Buttons: []gameengine.Button{
			{Label: fmt.Sprintf("Купить %d по %d", wanted, l.UnitPrice), Verb: "buy", Arg: fmt.Sprintf("%s:%d:%d", l.ID, wanted, l.Version)},
			{Label: "Рынок", Verb: "market"},
		},
	}, nil
}

func (s *store) cancelListing(userID int64, arg string) (gameengine.Result, error) {
	id := strings.ToLower(strings.TrimSpace(arg))
	l, ok, err := s.listingByID(id)
	if err != nil {
		return gameengine.Result{}, err
	}
	if !ok {
		return gameengine.Result{}, gameengine.Reject("Объявление не найдено или уже снято. Откройте рынок: /farm market.")
	}
	if l.SellerID != userID {
		return gameengine.Result{}, gameengine.Reject("Это не ваше объявление.")
	}
	p, ok, err := s.loadPlayer(userID)
	if err != nil {
		return gameengine.Result{}, err
	}
	if !ok {
		return gameengine.Result{}, gameengine.Reject("Двор не найден. Откройте ферму: /farm.")
	}
	p.add(l.Good, l.Qty)
	if err := s.savePlayer(userID, p); err != nil {
		return gameengine.Result{}, err
	}
	if err := s.listings.Delete([]byte(id)); err != nil {
		return gameengine.Result{}, err
	}
	return gameengine.Result{
		Text: fmt.Sprintf("Объявление снято. Возвращено: %s ×%d. Баланс не изменился.",
			goodLabel(l.Good), l.Qty),
		Buttons: []gameengine.Button{
			{Label: "Рынок", Verb: "market"},
			{Label: "Ферма", Verb: "board"},
		},
	}, nil
}

func (s *store) appendSale(good string, qty, price int, now int64) error {
	seq, err := s.nextCounter("sales_seq")
	if err != nil {
		return err
	}
	raw, err := json.Marshal(sale{Good: good, Qty: qty, UnitPrice: price, At: now})
	if err != nil {
		return err
	}
	key := fmt.Sprintf("%s:%016d", good, seq)
	if err := s.sales.Put([]byte(key), raw); err != nil {
		return err
	}
	prefix := []byte(good + ":")
	c := s.sales.Cursor()
	count := 0
	var oldest [][]byte
	for k, _ := c.Seek(prefix); k != nil && strings.HasPrefix(string(k), good+":"); k, _ = c.Next() {
		count++
		if count > kSalesStored {
			oldest = append(oldest, append([]byte(nil), k...))
		}
	}
	for _, k := range oldest {
		if err := s.sales.Delete(k); err != nil {
			return err
		}
	}
	return nil
}

func (s *store) recentSales(good string) ([]sale, error) {
	prefix := []byte(good + ":")
	var all []sale
	c := s.sales.Cursor()
	for k, v := c.Seek(prefix); k != nil && strings.HasPrefix(string(k), good+":"); k, v = c.Next() {
		var item sale
		if err := json.Unmarshal(v, &item); err != nil {
			return nil, err
		}
		all = append(all, item)
	}
	if len(all) > kRecentSales {
		all = all[len(all)-kRecentSales:]
	}
	return all, nil
}

func (s *store) marketReceipt(userID int64) (gameengine.Result, error) {
	listings, err := s.sortedListings()
	if err != nil {
		return gameengine.Result{}, err
	}
	var b strings.Builder
	b.WriteString("Рынок.")
	if len(listings) == 0 {
		b.WriteString(" Объявлений нет. Выставить товар: /farm sell grain 3 2.")
	} else {
		b.WriteString(" Объявления (цена, затем время):")
		for _, l := range listings {
			seller := ""
			if l.SellerID == userID {
				seller = " (ваше)"
			}
			fmt.Fprintf(&b, "\n%s · %s ×%d по %d · до %s%s",
				l.ID, goodLabel(l.Good), l.Qty, l.UnitPrice, deadlineText(l.ExpiresAt), seller)
		}
	}
	recent := make([]string, 0, len(goodsOrder))
	for _, good := range goodsOrder {
		sales, err := s.recentSales(good)
		if err != nil {
			return gameengine.Result{}, err
		}
		if len(sales) == 0 {
			continue
		}
		last := sales[len(sales)-1]
		recent = append(recent, fmt.Sprintf("%s: %d (последняя, %s)", goodLabel(good), last.UnitPrice, deadlineText(last.At)))
	}
	if len(recent) > 0 {
		b.WriteString("\nПоследние цены: " + strings.Join(recent, "; ") + ". Это не гарантия будущей цены.")
	}
	b.WriteString("\nКупить: /farm buy <id> [количество].")
	buttons := make([]gameengine.Button, 0, 8)
	for i, l := range listings {
		if i >= 6 {
			break
		}
		if l.SellerID == userID {
			buttons = append(buttons, gameengine.Button{Label: "Снять " + l.ID, Verb: "cancel", Arg: l.ID})
			continue
		}
		buttons = append(buttons, gameengine.Button{
			Label: fmt.Sprintf("%s: %s×%d по %d", l.ID, goodLabel(l.Good), l.Qty, l.UnitPrice),
			Verb:  "buy",
			Arg:   fmt.Sprintf("%s:%d:%d", l.ID, l.Qty, l.Version),
		})
	}
	buttons = append(buttons, gameengine.Button{Label: "Ферма", Verb: "board"})
	return gameengine.Result{Text: b.String(), Buttons: buttons}, nil
}

func (s *store) sortedListings() ([]*listing, error) {
	var listings []*listing
	if err := s.eachListing(func(l *listing) error {
		copied := *l
		listings = append(listings, &copied)
		return nil
	}); err != nil {
		return nil, err
	}
	sort.Slice(listings, func(i, j int) bool {
		if listings[i].UnitPrice != listings[j].UnitPrice {
			return listings[i].UnitPrice < listings[j].UnitPrice
		}
		if listings[i].CreatedAt != listings[j].CreatedAt {
			return listings[i].CreatedAt < listings[j].CreatedAt
		}
		return listings[i].ID < listings[j].ID
	})
	return listings, nil
}

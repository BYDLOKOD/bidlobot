package farm

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/veschin/bidlobot/internal/domain/reputation"
	"github.com/veschin/bidlobot/internal/gameengine"
)

func (s *store) createOffer(userID int64, arg string, now int64) (gameengine.Result, error) {
	parts := tokens(arg)
	if len(parts) != 4 {
		return gameengine.Result{}, gameengine.Reject("Формат: /farm offer mill 1 12 5 - слот, часы, цена.")
	}
	kind := normalizeKind(parts[0])
	if kind == "" {
		return gameengine.Result{}, gameengine.Reject("Слот: mill или oven.")
	}
	slotIndex, ok := positiveToken(parts[1], 2)
	if !ok {
		return gameengine.Result{}, gameengine.Reject("Слот: 1 или 2.")
	}
	hours, ok := positiveToken(parts[2], kOfferMaxHours)
	if !ok || (hours != kOfferMinHours && hours != kOfferHours && hours != kOfferMaxHours) {
		return gameengine.Result{}, gameengine.Reject("Срок аренды: 6, 12 или 24 часа.")
	}
	price, ok := positiveToken(parts[3], kMaxOfferPrice)
	if !ok {
		return gameengine.Result{}, gameengine.Reject("Цена аренды: от 1 до 100000.")
	}
	p, _, err := s.ensurePlayer(userID)
	if err != nil {
		return gameengine.Result{}, err
	}
	_, score, err := s.wallet(userID)
	if err != nil {
		return gameengine.Result{}, err
	}
	if score < reputation.MasterThreshold {
		return gameengine.Result{}, gameengine.Reject(fmt.Sprintf(
			"Сдавать оборудование в аренду может Мастер. Нужно %d очков звания, у вас %d. Очки дают за похвалу.", reputation.MasterThreshold, score))
	}
	p.clearExpiredLeases(now)
	if err := s.settleOwnerDue(p, userID, now); err != nil {
		return gameengine.Result{}, err
	}
	slot := p.slotAt(kind, slotIndex)
	if slot == nil {
		return gameengine.Result{}, gameengine.Reject("Слот: 1 или 2.")
	}
	label := "Мельница"
	if kind == "oven" {
		label = "Печь"
	}
	if !slot.Built {
		return gameengine.Result{}, gameengine.Reject(fmt.Sprintf("%s ещё не построена. /farm build %s.", label, kind))
	}
	if slot.ReadyAt > now {
		return gameengine.Result{}, gameengine.Reject(fmt.Sprintf("Стройка закончится %s. Предложить слот можно после этого.", deadlineText(slot.ReadyAt)))
	}
	if slot.Job != nil {
		return gameengine.Result{}, gameengine.Reject(fmt.Sprintf("%s занята до %s. Освободится в это время.", label, deadlineText(slot.Job.End)))
	}
	if slot.Lease != nil {
		return gameengine.Result{}, gameengine.Reject(fmt.Sprintf("%s сдана до %s. Следующее предложение - после аренды.", label, deadlineText(slot.Lease.End)))
	}
	if existing, ok, err := s.offerForSlot(userID, kind, slotIndex); err != nil {
		return gameengine.Result{}, err
	} else if ok {
		return gameengine.Result{}, gameengine.Reject(fmt.Sprintf("Слот уже предложен: %s до %s. Снять: /farm unoffer %s.",
			existing.ID, deadlineText(existing.ExpiresAt), existing.ID))
	}
	seq, err := s.nextCounter("next_offer")
	if err != nil {
		return gameengine.Result{}, err
	}
	o := &offer{
		ID:        "r" + strconv.Itoa(seq),
		OwnerID:   userID,
		Kind:      kind,
		Slot:      slotIndex,
		Hours:     hours,
		Price:     price,
		CreatedAt: now,
		ExpiresAt: now + int64(kOfferTTL.Seconds()),
		Version:   1,
	}
	if err := s.saveOffer(o); err != nil {
		return gameengine.Result{}, err
	}
	if err := s.savePlayer(userID, p); err != nil {
		return gameengine.Result{}, err
	}
	return gameengine.Result{
		Text: fmt.Sprintf("Предложение аренды %s %d: %d ч за %d. Действует до %s. Номер: %s.",
			strings.ToLower(label), slotIndex, hours, price, deadlineText(o.ExpiresAt), o.ID),
		Buttons: []gameengine.Button{
			{Label: "Снять " + o.ID, Verb: "unoffer", Arg: o.ID},
			{Label: "Двор", Verb: "yard"},
		},
	}, nil
}

func (s *store) cancelOffer(userID int64, arg string) (gameengine.Result, error) {
	id := strings.ToLower(strings.TrimSpace(arg))
	o, ok, err := s.offerByID(id)
	if err != nil {
		return gameengine.Result{}, err
	}
	if !ok {
		return gameengine.Result{}, gameengine.Reject("Предложение не найдено или истекло. Откройте двор: /farm yard.")
	}
	if o.OwnerID != userID {
		return gameengine.Result{}, gameengine.Reject("Это не ваше предложение.")
	}
	if err := s.offers.Delete([]byte(id)); err != nil {
		return gameengine.Result{}, err
	}
	return gameengine.Result{
		Text: fmt.Sprintf("Предложение %s снято. Слот свободен.", id),
		Buttons: []gameengine.Button{
			{Label: "Двор", Verb: "yard"},
			{Label: "Ферма", Verb: "board"},
		},
	}, nil
}

func (s *store) acceptOffer(userID int64, arg string, now int64) (gameengine.Result, error) {
	parts := tokens(arg)
	if len(parts) < 1 || len(parts) > 2 {
		return gameengine.Result{}, gameengine.Reject("Укажите номер предложения: /farm accept <id>.")
	}
	id := strings.ToLower(parts[0])
	version := 0
	if len(parts) == 2 {
		n, ok := positiveToken(parts[1], 1<<20)
		if !ok {
			return gameengine.Result{}, gameengine.Reject("Условия предложения устарели. Откройте двор владельца.")
		}
		version = n
	}
	o, ok, err := s.offerByID(id)
	if err != nil {
		return gameengine.Result{}, err
	}
	if !ok {
		return gameengine.Result{}, gameengine.Reject("Предложение больше не действует. Откройте двор владельца: /farm yard <id>.")
	}
	if o.OwnerID == userID {
		return gameengine.Result{}, gameengine.Reject("Своё предложение принять нельзя. Снимите его: /farm unoffer " + o.ID + ".")
	}
	if version != 0 && version != o.Version {
		return gameengine.Result{
			Text: fmt.Sprintf("Условия изменились: %s %d, %d ч, %d репы, действует до %s. Подтвердите аренду.",
				strings.ToLower(offerLabel(o.Kind)), o.Slot, o.Hours, o.Price, deadlineText(o.ExpiresAt)),
			Buttons: []gameengine.Button{
				{Label: fmt.Sprintf("Аренда %d ч за %d", o.Hours, o.Price), Verb: "accept", Arg: fmt.Sprintf("%s:%d", o.ID, o.Version)},
				{Label: "Двор", Verb: "yard", Arg: strconv.FormatInt(o.OwnerID, 10)},
			},
		}, nil
	}
	owner, ok, err := s.loadPlayer(o.OwnerID)
	if err != nil {
		return gameengine.Result{}, err
	}
	if !ok {
		return gameengine.Result{}, gameengine.Reject("Двор владельца не найден. Предложение отменено.")
	}
	owner.clearExpiredLeases(now)
	if err := s.settleOwnerDue(owner, o.OwnerID, now); err != nil {
		return gameengine.Result{}, err
	}
	slot := owner.slotAt(o.Kind, o.Slot)
	if slot == nil || !slot.Built || slot.ReadyAt > now || slot.Job != nil || slot.Lease != nil {
		return gameengine.Result{}, gameengine.Reject("Слот уже занят или недоступен. Предложение не действует - откройте двор владельца.")
	}
	if _, _, err := s.ensurePlayer(userID); err != nil {
		return gameengine.Result{}, err
	}
	if err := s.debit(userID, o.Price); err != nil {
		return gameengine.Result{}, err
	}
	if err := s.credit(o.OwnerID, o.Price); err != nil {
		return gameengine.Result{}, err
	}
	end := now + int64(o.Hours)*3600
	slot.Lease = &lease{RenterID: userID, Price: o.Price, Start: now, End: end}
	if err := s.savePlayer(o.OwnerID, owner); err != nil {
		return gameengine.Result{}, err
	}
	if err := s.offers.Delete([]byte(o.ID)); err != nil {
		return gameengine.Result{}, err
	}
	available, _, err := s.wallet(userID)
	if err != nil {
		return gameengine.Result{}, err
	}
	tile := "m" + strconv.Itoa(o.Slot)
	if o.Kind == "oven" {
		tile = "o" + strconv.Itoa(o.Slot)
	}
	return gameengine.Result{
		Text: fmt.Sprintf("Аренда началась. %s %d доступна до %s. Списано: %d. Доступно: %d.",
			offerLabel(o.Kind), o.Slot, deadlineText(end), o.Price, available),
		Buttons: []gameengine.Button{
			{Label: "Запустить", Verb: o.Kind, Arg: fmt.Sprintf("u%d:%s", o.OwnerID, tile)},
			{Label: "Двор", Verb: "yard", Arg: strconv.FormatInt(o.OwnerID, 10)},
		},
	}, nil
}

func offerLabel(kind string) string {
	if kind == "oven" {
		return "Печь"
	}
	return "Мельница"
}

func (s *store) offerForSlot(ownerID int64, kind string, slotIndex int) (*offer, bool, error) {
	c := s.offers.Cursor()
	for k, v := c.First(); k != nil; k, v = c.Next() {
		var o offer
		if err := decodeJSON(v, &o); err != nil {
			return nil, false, err
		}
		if o.OwnerID == ownerID && o.Kind == kind && o.Slot == slotIndex {
			return &o, true, nil
		}
	}
	return nil, false, nil
}

func (s *store) yardReceipt(viewerID int64, arg string, now int64) (gameengine.Result, error) {
	targetID := viewerID
	if strings.TrimSpace(arg) != "" {
		id, err := strconv.ParseInt(strings.TrimSpace(arg), 10, 64)
		if err != nil || id <= 0 {
			return gameengine.Result{}, gameengine.Reject("Укажите один Telegram ID.")
		}
		targetID = id
	}
	var p *player
	var ok bool
	var err error
	if targetID == viewerID {
		p, _, err = s.ensurePlayer(viewerID)
	} else {
		p, ok, err = s.loadPlayer(targetID)
		if err == nil && !ok {
			return gameengine.Result{}, gameengine.Reject("Двор не найден. Игрок ещё не открыл ферму.")
		}
	}
	if err != nil {
		return gameengine.Result{}, err
	}
	p.clearExpiredLeases(now)
	lines := []string{fmt.Sprintf("Двор %d. Грядок: %d. Амбар: зерно %d, мука %d, хлеб %d, семена %d.",
		targetID, p.Plots, p.count(goodGrain), p.count(goodFlour), p.count(goodBread), p.count(goodSeed))}
	buttons := make([]gameengine.Button, 0, 8)
	for i := 0; i < kMaxPlots; i++ {
		if i >= p.Plots {
			lines = append(lines, fmt.Sprintf("Грядка %d - не куплена.", i+1))
			continue
		}
		j := p.PlotJobs[i]
		if j == nil {
			lines = append(lines, fmt.Sprintf("Грядка %d - свободна.", i+1))
			if targetID == viewerID && len(buttons) < 8 {
				buttons = append(buttons, gameengine.Button{Label: fmt.Sprintf("Посадить %d", i+1), Verb: "plant", Arg: "p" + strconv.Itoa(i+1)})
			}
			continue
		}
		lines = append(lines, fmt.Sprintf("Грядка %d - зерно, до %s.", i+1, deadlineText(j.End)))
	}
	for _, kind := range []string{"mill", "oven"} {
		slots := p.slots(kind)
		for i := range slots {
			slot := &slots[i]
			label := "Мельница"
			if kind == "oven" {
				label = "Печь"
			}
			tile := "m" + strconv.Itoa(i+1)
			if kind == "oven" {
				tile = "o" + strconv.Itoa(i+1)
			}
			switch {
			case !slot.Built:
				lines = append(lines, fmt.Sprintf("%s %d - не построена.", label, i+1))
			case slot.ReadyAt > now:
				lines = append(lines, fmt.Sprintf("%s %d - стройка до %s.", label, i+1, deadlineText(slot.ReadyAt)))
			case slot.Job != nil:
				lines = append(lines, fmt.Sprintf("%s %d - занята до %s.", label, i+1, deadlineText(slot.Job.End)))
			case slot.Lease != nil:
				who := "другой участник"
				if slot.Lease.RenterID == viewerID {
					who = "вы"
				}
				lines = append(lines, fmt.Sprintf("%s %d - аренда (%s) до %s.", label, i+1, who, deadlineText(slot.Lease.End)))
				if slot.Lease.RenterID == viewerID && len(buttons) < 10 {
					buttons = append(buttons, gameengine.Button{Label: label + " " + strconv.Itoa(i+1), Verb: kind, Arg: fmt.Sprintf("u%d:%s", targetID, tile)})
				}
			default:
				if o, exists, err := s.offerForSlot(targetID, kind, i+1); err != nil {
					return gameengine.Result{}, err
				} else if exists {
					lines = append(lines, fmt.Sprintf("%s %d - предложена: %d ч за %d до %s.", label, i+1, o.Hours, o.Price, deadlineText(o.ExpiresAt)))
					if targetID != viewerID && len(buttons) < 10 {
						buttons = append(buttons, gameengine.Button{
							Label: fmt.Sprintf("Аренда %s %d: %d ч за %d", label, i+1, o.Hours, o.Price),
							Verb:  "accept",
							Arg:   fmt.Sprintf("%s:%d", o.ID, o.Version),
						})
					} else if targetID == viewerID {
						buttons = append(buttons, gameengine.Button{Label: "Снять " + o.ID, Verb: "unoffer", Arg: o.ID})
					}
				} else {
					lines = append(lines, fmt.Sprintf("%s %d - свободна.", label, i+1))
					if targetID == viewerID && len(buttons) < 10 {
						verb := kind
						buttons = append(buttons, gameengine.Button{Label: label + " " + strconv.Itoa(i+1), Verb: verb, Arg: tile})
					}
				}
			}
		}
	}
	buttons = append(buttons, gameengine.Button{Label: "Ферма", Verb: "board"})
	return gameengine.Result{Text: strings.Join(lines, "\n"), Buttons: buttons}, nil
}

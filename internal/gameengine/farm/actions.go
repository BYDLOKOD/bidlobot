package farm

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/veschin/bidlobot/internal/gameengine"
)

func tokens(raw string) []string {
	return strings.Fields(strings.ReplaceAll(strings.TrimSpace(raw), ":", " "))
}

func positiveToken(raw string, max int) (int, bool) {
	n, err := strconv.Atoi(raw)
	return n, err == nil && n > 0 && n <= max
}

func shopReceipt() gameengine.Result {
	return gameengine.Result{
		Text: fmt.Sprintf("Магазин фермы. Цены на кнопках указаны в очках репутации. Для мельницы нужно %d ед. зерна, для печи %d ед. муки. Строительство занимает 1 час.", kMillCostGrain, kOvenCostFlour),
		Buttons: []gameengine.Button{
			{Label: fmt.Sprintf("Купить семя за %d", kSeedPrice), Verb: "seed", Arg: "1"},
			{Label: fmt.Sprintf("Купить грядку за %d", kPlotPrice), Verb: "plot"},
			{Label: fmt.Sprintf("Построить мельницу за %d", kMillCostRep), Verb: "build", Arg: "mill"},
			{Label: fmt.Sprintf("Построить печь за %d", kOvenCostRep), Verb: "build", Arg: "oven"},
			{Label: "Ферма", Verb: "board"},
		},
	}
}

func (s *store) buySeeds(userID int64, arg string, now int64) (gameengine.Result, error) {
	count := 1
	if strings.TrimSpace(arg) != "" {
		n, ok := positiveToken(strings.TrimSpace(arg), kMaxSeedBuy)
		if !ok {
			return gameengine.Result{}, gameengine.Reject("Количество семян: от 1 до 50.")
		}
		count = n
	}
	p, _, err := s.ensurePlayer(userID)
	if err != nil {
		return gameengine.Result{}, err
	}
	cost := count * kSeedPrice
	if err := s.debit(userID, cost); err != nil {
		return gameengine.Result{}, err
	}
	p.add(goodSeed, count)
	if err := s.savePlayer(userID, p); err != nil {
		return gameengine.Result{}, err
	}
	available, _, err := s.wallet(userID)
	if err != nil {
		return gameengine.Result{}, err
	}
	return gameengine.Result{
		Text: fmt.Sprintf("Куплено семян: %d. Списано: %d. Доступно: %d.", count, cost, available),
		Buttons: []gameengine.Button{
			{Label: "Посадить", Verb: "plant", Arg: "p" + strconv.Itoa(freePlotOrFirst(p))},
			{Label: "Ферма", Verb: "board"},
		},
	}, nil
}

func (s *store) buyPlot(userID int64) (gameengine.Result, error) {
	p, _, err := s.ensurePlayer(userID)
	if err != nil {
		return gameengine.Result{}, err
	}
	if p.Plots >= kMaxPlots {
		return gameengine.Result{}, gameengine.Reject("Грядок уже 3 - это предел. Новые грядки недоступны.")
	}
	if err := s.debit(userID, kPlotPrice); err != nil {
		return gameengine.Result{}, err
	}
	p.Plots++
	if err := s.savePlayer(userID, p); err != nil {
		return gameengine.Result{}, err
	}
	available, _, err := s.wallet(userID)
	if err != nil {
		return gameengine.Result{}, err
	}
	return gameengine.Result{
		Text: fmt.Sprintf("Куплена грядка %d. Стоимость: %d. Баланс: %d.", p.Plots, kPlotPrice, available),
		Buttons: []gameengine.Button{
			{Label: "Посадить", Verb: "plant", Arg: "p" + strconv.Itoa(p.Plots)},
			{Label: "Ферма", Verb: "board"},
		},
	}, nil
}

func (s *store) build(userID int64, arg string, now int64) (gameengine.Result, error) {
	kind := normalizeKind(strings.TrimSpace(arg))
	if kind == "" {
		return gameengine.Result{}, gameengine.Reject("Что строить: /farm build mill или /farm build oven.")
	}
	p, _, err := s.ensurePlayer(userID)
	if err != nil {
		return gameengine.Result{}, err
	}
	slots := p.slots(kind)
	index := 0
	for i := range slots {
		if !slots[i].Built {
			index = i + 1
			break
		}
	}
	if index == 0 {
		label := "Мельниц"
		if kind == "oven" {
			label = "Печей"
		}
		return gameengine.Result{}, gameengine.Reject(fmt.Sprintf("%s уже %d - больше нельзя.", label, len(slots)))
	}
	var costRep int
	var inGood string
	var inQty int
	if kind == "mill" {
		costRep, inGood, inQty = kMillCostRep, goodGrain, kMillCostGrain
	} else {
		costRep, inGood, inQty = kOvenCostRep, goodFlour, kOvenCostFlour
	}
	if p.count(inGood) < inQty {
		return gameengine.Result{}, gameengine.Reject(fmt.Sprintf(
			"Не хватает %s: нужно %d, в амбаре %d. Посадите ещё или купите на рынке.",
			goodLabel(inGood), inQty, p.count(inGood)))
	}
	available, _, err := s.wallet(userID)
	if err != nil {
		return gameengine.Result{}, err
	}
	if available < costRep {
		return gameengine.Result{}, gameengine.Reject(fmt.Sprintf(
			"Не хватает репы: нужно %d, доступно %d. Посадите стартовое семя, продайте товар или сдайте заказ кооперативу.",
			costRep, available))
	}
	if err := s.debit(userID, costRep); err != nil {
		return gameengine.Result{}, err
	}
	p.take(inGood, inQty)
	ready := now + int64(kBuildTime.Seconds())
	slots[index-1].Built = true
	slots[index-1].ReadyAt = ready
	if err := s.savePlayer(userID, p); err != nil {
		return gameengine.Result{}, err
	}
	available, _, err = s.wallet(userID)
	if err != nil {
		return gameengine.Result{}, err
	}
	label := "Мельница"
	if kind == "oven" {
		label = "Печь"
	}
	return gameengine.Result{
		Text: fmt.Sprintf("%s строится. Готово к работе: %s. Списано: репа %d, %s %d. Баланс: %d.",
			label, deadlineText(ready), costRep, goodLabel(inGood), inQty, available),
		Buttons: []gameengine.Button{
			{Label: "Ферма", Verb: "board"},
		},
	}, nil
}

func freePlotOrFirst(p *player) int {
	if plot := p.freePlot(); plot > 0 {
		return plot
	}
	return 1
}

func (s *store) plant(userID int64, arg string, now int64) (gameengine.Result, error) {
	p, _, err := s.ensurePlayer(userID)
	if err != nil {
		return gameengine.Result{}, err
	}
	raw := strings.ToLower(strings.TrimSpace(arg))
	raw = strings.TrimPrefix(raw, "p")
	index := 0
	if raw != "" {
		n, ok := positiveToken(raw, kMaxPlots)
		if !ok {
			return gameengine.Result{}, gameengine.Reject("Номер грядки: 1, 2 или 3.")
		}
		index = n
	}
	if index > 0 {
		if index > p.Plots {
			return gameengine.Result{}, gameengine.Reject(fmt.Sprintf(
				"Грядка %d ещё не куплена. Новая грядка стоит %d репы: /farm plot.", index, kPlotPrice))
		}
		if p.PlotJobs[index-1] != nil {
			return gameengine.Result{}, gameengine.Reject(fmt.Sprintf(
				"Грядка %d занята до %s. Сбор: зерно %d.",
				index, deadlineText(p.PlotJobs[index-1].End), kPlantYield))
		}
	} else {
		index = p.freePlot()
		if index == 0 {
			return gameengine.Result{}, gameengine.Reject(fmt.Sprintf(
				"Все грядки заняты до %s. Новую грядку можно купить: /farm plot.",
				deadlineText(earliestPlotEnd(p))))
		}
	}
	seedSource := "семя"
	if p.StarterSeed {
		p.StarterSeed = false
		seedSource = "стартовое семя"
	} else if !p.take(goodSeed, 1) {
		return gameengine.Result{}, gameengine.Reject(fmt.Sprintf("Семена закончились. Одно семя стоит %d репы: /farm seed.", kSeedPrice))
	}
	end := now + int64(kPlantTime.Seconds())
	p.PlotJobs[index-1] = &job{
		Kind:    "plant",
		ActorID: userID,
		Start:   now,
		End:     end,
		Good:    goodGrain,
		Qty:     kPlantYield,
	}
	if err := s.savePlayer(userID, p); err != nil {
		return gameengine.Result{}, err
	}
	if err := s.scheduleJob(userID, userID, "p"+strconv.Itoa(index), end); err != nil {
		return gameengine.Result{}, err
	}
	return gameengine.Result{
		Text: fmt.Sprintf("Грядка %d засеяна. Использовано %s. Урожай созреет %s.", index, seedSource, deadlineText(end)),
		Buttons: []gameengine.Button{
			{Label: "Ферма", Verb: "board"},
			{Label: "Рынок", Verb: "market"},
		},
	}, nil
}

func earliestPlotEnd(p *player) int64 {
	var end int64
	for _, j := range p.PlotJobs {
		if j != nil && (end == 0 || j.End < end) {
			end = j.End
		}
	}
	return end
}

func (s *store) startOwnJob(userID int64, kind, arg string, now int64) (gameengine.Result, error) {
	ref, err := parseTileRef(kind, arg)
	if err != nil {
		return gameengine.Result{}, err
	}
	return s.startJob(userID, ref.OwnerID, ref.Kind, ref.Index, now)
}

func (s *store) startByTile(userID int64, arg string, now int64) (gameengine.Result, error) {
	ref, err := parseTileRef("", arg)
	if err != nil {
		return gameengine.Result{}, err
	}
	if ref.Kind == "plot" {
		return s.plant(userID, strconv.Itoa(ref.Index), now)
	}
	return s.startJob(userID, ref.OwnerID, ref.Kind, ref.Index, now)
}

type tileRef struct {
	Kind    string
	OwnerID int64
	Index   int
}

func parseTileRef(defaultKind, raw string) (tileRef, error) {
	parts := tokens(raw)
	ref := tileRef{Kind: defaultKind}
	if len(parts) == 0 {
		if defaultKind == "" {
			return tileRef{}, gameengine.Reject("Укажите слот: p1, m1, o1 или u<владелец>:m<слот>.")
		}
		ref.Index = 1
		return ref, nil
	}
	if len(parts) > 2 {
		return tileRef{}, gameengine.Reject("Укажите слот: p1, m1, o1 или u<владелец>:m<слот>.")
	}
	if strings.HasPrefix(strings.ToLower(parts[0]), "u") {
		ownerID, err := strconv.ParseInt(parts[0][1:], 10, 64)
		if err != nil || ownerID <= 0 {
			return tileRef{}, gameengine.Reject("Владелец указан неверно.")
		}
		ref.OwnerID = ownerID
		if len(parts) != 2 {
			return tileRef{}, gameengine.Reject("Укажите слот: u<владелец>:m<слот>.")
		}
		kind, index, ok := parseTileToken(parts[1])
		if !ok || kind == "plot" {
			return tileRef{}, gameengine.Reject("Слот аренды: m1, m2, o1 или o2.")
		}
		ref.Kind, ref.Index = kind, index
	} else {
		kind, index, ok := parseTileToken(parts[0])
		if !ok {
			return tileRef{}, gameengine.Reject("Слот: p1, m1, o1 или 1/2.")
		}
		if kind != "" {
			if ref.Kind != "" && kind != ref.Kind {
				return tileRef{}, gameengine.Reject("Этот слот не подходит для действия.")
			}
			ref.Kind = kind
		}
		ref.Index = index
	}
	if ref.Kind == "" {
		return tileRef{}, gameengine.Reject("Слот: m1, o1 или u<владелец>:m<слот>.")
	}
	switch ref.Kind {
	case "plot":
		if ref.Index < 1 || ref.Index > kMaxPlots {
			return tileRef{}, gameengine.Reject("Номер грядки: 1, 2 или 3.")
		}
	case "mill", "oven":
		if ref.Index < 1 || ref.Index > 2 {
			return tileRef{}, gameengine.Reject("Слот: 1 или 2.")
		}
	default:
		return tileRef{}, gameengine.Reject("Неизвестный слот.")
	}
	return ref, nil
}

func parseTileToken(raw string) (kind string, index int, ok bool) {
	token := strings.ToLower(strings.TrimSpace(raw))
	if token == "" {
		return "", 0, false
	}
	switch token[0] {
	case 'p':
		kind = "plot"
		token = token[1:]
	case 'm':
		kind = "mill"
		token = token[1:]
	case 'o':
		kind = "oven"
		token = token[1:]
	}
	n, err := strconv.Atoi(token)
	if err != nil || n <= 0 {
		return "", 0, false
	}
	return kind, n, true
}

func jobSpec(kind string) (inGood string, inQty int, outGood string, outQty int, seconds int64) {
	switch kind {
	case "mill":
		return goodGrain, kMillInput, goodFlour, kMillOutput, int64(kMillTime.Seconds())
	case "oven":
		return goodFlour, kOvenInput, goodBread, kOvenOutput, int64(kOvenTime.Seconds())
	}
	return "", 0, "", 0, 0
}

func (s *store) startJob(actorID, ownerID int64, kind string, index int, now int64) (gameengine.Result, error) {
	if kind != "mill" && kind != "oven" {
		return gameengine.Result{}, gameengine.Reject("Запустить можно мельницу или печь.")
	}
	if ownerID == 0 {
		ownerID = actorID
	}
	owner, ok, err := s.loadPlayer(ownerID)
	if err != nil {
		return gameengine.Result{}, err
	}
	if !ok {
		return gameengine.Result{}, gameengine.Reject("Двор не найден. Игрок ещё не открыл ферму.")
	}
	owner.clearExpiredLeases(now)
	if err := s.settleOwnerDue(owner, ownerID, now); err != nil {
		return gameengine.Result{}, err
	}
	slot := owner.slotAt(kind, index)
	if slot == nil {
		return gameengine.Result{}, gameengine.Reject("Слот: 1 или 2.")
	}
	if !slot.Built {
		label := "Мельница"
		if kind == "oven" {
			label = "Печь"
		}
		if ownerID == actorID {
			return gameengine.Result{}, gameengine.Reject(fmt.Sprintf("%s ещё не построена. /farm build %s.", label, kind))
		}
		return gameengine.Result{}, gameengine.Reject(fmt.Sprintf("%s ещё не построена у владельца.", label))
	}
	if slot.ReadyAt > now {
		return gameengine.Result{}, gameengine.Reject(fmt.Sprintf("Стройка закончится %s. Работа станет доступна после этого.", deadlineText(slot.ReadyAt)))
	}
	inGood, inQty, outGood, outQty, seconds := jobSpec(kind)
	end := now + seconds
	if slot.Lease != nil {
		if slot.Lease.RenterID != actorID {
			if ownerID == actorID {
				return gameengine.Result{}, gameengine.Reject(fmt.Sprintf("Слот сдан в аренду до %s. Работать может арендатор.", deadlineText(slot.Lease.End)))
			}
			return gameengine.Result{}, gameengine.Reject(fmt.Sprintf("Слот арендует другой участник до %s.", deadlineText(slot.Lease.End)))
		}
		if end > slot.Lease.End {
			return gameengine.Result{}, gameengine.Reject("До конца аренды осталось меньше двух часов. Выберите другую мельницу или печь.")
		}
	} else if ownerID != actorID {
		return gameengine.Result{}, gameengine.Reject("Слот не сдан в аренду. Возьмите предложение владельца на его дворе.")
	}
	if slot.Job != nil {
		label := "Мельница"
		if kind == "oven" {
			label = "Печь"
		}
		return gameengine.Result{}, gameengine.Reject(fmt.Sprintf("%s занята до %s. Освободится в это время.", label, deadlineText(slot.Job.End)))
	}
	if o, ok, err := s.offerForSlot(ownerID, kind, index); err != nil {
		return gameengine.Result{}, err
	} else if ok {
		return gameengine.Result{}, gameengine.Reject(fmt.Sprintf("Слот держится под предложением аренды %s до %s. Снять: /farm unoffer %s.",
			o.ID, deadlineText(o.ExpiresAt), o.ID))
	}
	actor := owner
	if ownerID != actorID {
		actor, _, err = s.ensurePlayer(actorID)
		if err != nil {
			return gameengine.Result{}, err
		}
	}
	if actor.count(inGood) < inQty {
		return gameengine.Result{}, gameengine.Reject(fmt.Sprintf(
			"Не хватает %s: нужно %d, в амбаре %d.",
			goodLabel(inGood), inQty, actor.count(inGood)))
	}
	actor.take(inGood, inQty)
	sz := slotAt(owner, kind, index)
	sz.Job = &job{Kind: kind, ActorID: actorID, Start: now, End: end, Good: outGood, Qty: outQty}
	if err := s.savePlayer(ownerID, owner); err != nil {
		return gameengine.Result{}, err
	}
	if ownerID != actorID {
		if err := s.savePlayer(actorID, actor); err != nil {
			return gameengine.Result{}, err
		}
	}
	tile := "m" + strconv.Itoa(index)
	if kind == "oven" {
		tile = "o" + strconv.Itoa(index)
	}
	if err := s.scheduleJob(actorID, ownerID, tile, end); err != nil {
		return gameengine.Result{}, err
	}
	label := "Помол"
	outLabel := "Мука"
	if kind == "oven" {
		label, outLabel = "Выпечка", "Хлеб"
	}
	leaseNote := ""
	if sz.Lease != nil {
		leaseNote = fmt.Sprintf(" Аренда до %s.", deadlineText(sz.Lease.End))
	}
	return gameengine.Result{
		Text: fmt.Sprintf("%s начат%s. %s: %d — к %s. Списано: %s %d.",
			label, leaseNote, outLabel, outQty, deadlineText(end), goodLabel(inGood), inQty),
		Buttons: []gameengine.Button{
			{Label: "Ферма", Verb: "board"},
		},
	}, nil
}

func slotAt(p *player, kind string, index int) *slot {
	return p.slotAt(kind, index)
}

// settleOwnerDue clears already-finished jobs on one owner's assets. A due
// leased job still belongs to its initiator, so the output follows ActorID.
func (s *store) settleOwnerDue(owner *player, ownerID, now int64) error {
	credit := func(j *job) error {
		if j.ActorID == ownerID {
			owner.add(j.Good, j.Qty)
			return nil
		}
		actor, ok, err := s.loadPlayer(j.ActorID)
		if err != nil || !ok {
			return err
		}
		actor.add(j.Good, j.Qty)
		return s.savePlayer(j.ActorID, actor)
	}
	for i, j := range owner.PlotJobs {
		if j != nil && now >= j.End {
			if err := credit(j); err != nil {
				return err
			}
			owner.PlotJobs[i] = nil
		}
	}
	for _, kind := range []string{"mill", "oven"} {
		slots := owner.slots(kind)
		for i := range slots {
			if slots[i].Job != nil && now >= slots[i].Job.End {
				if err := credit(slots[i].Job); err != nil {
					return err
				}
				slots[i].Job = nil
			}
		}
	}
	return nil
}

func (s *store) collectReceipt(userID int64, now int64, settled []harvest) (gameengine.Result, error) {
	buttons := []gameengine.Button{
		{Label: "Рынок", Verb: "market"},
		{Label: "Кооператив", Verb: "coop"},
		{Label: "Ферма", Verb: "board"},
	}
	if len(settled) > 0 {
		return gameengine.Result{Text: harvestLine("Сбор", settled), Buttons: buttons}, nil
	}
	next := s.nextActorCompletion(userID, now)
	if next == 0 {
		return gameengine.Result{
			Text:    "Готовых работ нет. Проверьте запасы в амбаре.",
			Buttons: buttons,
		}, nil
	}
	return gameengine.Result{
		Text:    fmt.Sprintf("Готовой продукции нет. Ближайшее завершение: %s.", deadlineText(next)),
		Buttons: buttons,
	}, nil
}

func (s *store) nextActorCompletion(actorID, now int64) int64 {
	var next int64
	consider := func(j *job) {
		if j != nil && j.ActorID == actorID && j.End > now && (next == 0 || j.End < next) {
			next = j.End
		}
	}
	_ = s.eachPlayer(func(_ int64, p *player) error {
		for _, j := range p.PlotJobs {
			consider(j)
		}
		for _, kind := range []string{"mill", "oven"} {
			slots := p.slots(kind)
			for i := range slots {
				consider(slots[i].Job)
			}
		}
		return nil
	})
	return next
}

func (s *store) boardReceipt(userID int64, now int64, join bool) (gameengine.Result, error) {
	p, created, err := s.ensurePlayer(userID)
	if err != nil {
		return gameengine.Result{}, err
	}
	if join && !created {
		return gameengine.Result{
			Text: "Ферма уже открыта. Стартовое семя выдаётся один раз - второго не будет.",
			Buttons: []gameengine.Button{
				{Label: "Ферма", Verb: "board"},
			},
		}, nil
	}
	available, score, err := s.wallet(userID)
	if err != nil {
		return gameengine.Result{}, err
	}
	listings := 0
	if err := s.eachListing(func(l *listing) error {
		if l.SellerID == userID {
			listings++
		}
		return nil
	}); err != nil {
		return gameengine.Result{}, err
	}
	budget := s.effectiveCoopInt("coop_budget", now)
	text := fmt.Sprintf(
		"Ферма. Репа: %d, очки: %d. Грядки: %d/%d. Амбар: зерно %d, мука %d, хлеб %d, семена %d. Стартовое семя: %s. Объявлений: %d/10. Бюджет кооператива: %d.",
		available, score, occupiedPlots(p), p.Plots,
		p.count(goodGrain), p.count(goodFlour), p.count(goodBread), p.count(goodSeed),
		yesNo(p.StarterSeed), listings, budget)
	if created {
		text = "Ферма открыта. У вас одна грядка и стартовое семя. " + text
	}
	buttons := []gameengine.Button{
		{Label: "Посадить", Verb: "plant", Arg: "p" + strconv.Itoa(freePlotOrFirst(p))},
		{Label: "Магазин", Verb: "shop"},
		{Label: "Рынок", Verb: "market"},
		{Label: "Кооператив", Verb: "coop"},
		{Label: "Помощь", Verb: "help"},
	}
	return gameengine.Result{Text: text, Buttons: buttons}, nil
}

func yesNo(value bool) string {
	if value {
		return "не использовано"
	}
	return "использовано"
}

func occupiedPlots(p *player) int {
	occupied := 0
	for i := 0; i < p.Plots && i < kMaxPlots; i++ {
		if p.PlotJobs[i] != nil {
			occupied++
		}
	}
	return occupied
}

func (s *store) eachListing(fn func(*listing) error) error {
	c := s.listings.Cursor()
	for k, v := c.First(); k != nil; k, v = c.Next() {
		var l listing
		if err := decodeJSON(v, &l); err != nil {
			return err
		}
		if err := fn(&l); err != nil {
			return err
		}
	}
	return nil
}

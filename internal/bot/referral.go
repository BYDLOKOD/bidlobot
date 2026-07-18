package bot

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"

	"github.com/veschin/bidlobot/internal/domain/referral"
	"github.com/veschin/bidlobot/internal/shared"
	"github.com/veschin/bidlobot/internal/storage"
)

// referral interaction lifetime. After this the prompt tokens stop
// accepting taps/replies and the user reruns the command.
const referralInteractionTTL = 10 * time.Minute

// referralSender is the narrow telego surface the referral UX needs.
// Production passes the rate-limited *tgclient.Client.
type referralSender interface {
	SendMessage(context.Context, *telego.SendMessageParams) (*telego.Message, error)
	EditMessageText(context.Context, *telego.EditMessageTextParams) (*telego.Message, error)
	AnswerCallbackQuery(context.Context, *telego.AnswerCallbackQueryParams) error
}

// Callback-data prefixes for the referral namespace. Each carries a
// random token that scopes the tap to one interaction's actor+chat.
const (
	rfCallbackPrefix = "rf:"
	rfPage           = "rf:p:" // rf:p:<page>:<token>
	rfSelect         = "rf:s:" // rf:s:<listIndex>:<token>
	rfNew            = "rf:n:" // rf:n:<token>
	rfMatch          = "rf:m:" // rf:m:<matchIndex>:<token>
	rfCreate         = "rf:c:" // rf:c:<token>
	rfDeleteBad      = "rf:d:bad:"
	rfDeleteScam     = "rf:d:scam:"
	rfCancel         = "rf:x:" // rf:x:<token>
)

// Services per picker page.
const referralPageSize = 8

// Label truncation rune cap.
const referralLabelMax = 48

type actorChatKey struct {
	userID int64
	chatID int64
}

type interactionKind int

const (
	kindRegistration interactionKind = iota
	kindModeration
)

type interactionState int

const (
	statePicker interactionState = iota
	stateAwaitURL
	stateAwaitNew
	stateAwaitChoice
	stateModeration
)

// referralInteraction is one live prompt: a picker page, an awaited
// reply, a fuzzy/exact-effect choice, or an admin moderation
// confirmation. Tokens are 16-hex, actor/chat/prompt-locked, expire
// after referralInteractionTTL, and prune lazily on access.
type referralInteraction struct {
	token       string
	kind        interactionKind
	state       interactionState
	actorUserID int64
	chatID      int64
	promptMsgID int
	createdAt   time.Time
	expiresAt   time.Time

	// picker
	page int

	// draft for the new-service flow and the fuzzy/exact confirmation
	draftSvc referral.Service
	draftURL string
	matches  []referral.Match // fuzzy/exact candidates for stateAwaitChoice

	// existing-service flow
	selectedServiceID uint64

	// moderation
	reportID uint64
}

// ReferralHandler implements the chat-scoped referral catalog UX:
// /refreg (register), /refs (list), /refreport (admin moderation).
type ReferralHandler struct {
	bot    referralSender
	store  referral.Store
	admins AdminChecker
	log    *slog.Logger

	mu          sync.Mutex
	byToken     map[string]*referralInteraction
	byActorChat map[actorChatKey]string
}

// NewReferralHandler constructs the referral UX handler. admins may be
// nil for tests that do not exercise /refreport.
func NewReferralHandler(bot referralSender, store referral.Store, admins AdminChecker, log *slog.Logger) *ReferralHandler {
	if log == nil {
		log = slog.Default()
	}
	return &ReferralHandler{
		bot:         bot,
		store:       store,
		admins:      admins,
		log:         log,
		byToken:     make(map[string]*referralInteraction),
		byActorChat: make(map[actorChatKey]string),
	}
}

// ---------------------------------------------------------------------------
// /refs - list the catalog
// ---------------------------------------------------------------------------

// HandleList renders the chat's referral catalog grouped by service.
func (h *ReferralHandler) HandleList(_ *th.Context, msg telego.Message) error {
	if msg.From == nil || msg.From.IsBot {
		return nil
	}
	absChat := storage.AbsChatID(msg.Chat.ID)
	groups, err := h.store.List(context.Background(), absChat)
	if err != nil {
		h.log.Warn("referral list failed", "error", err, "chat_id", msg.Chat.ID)
		return h.replyRef(msg, "что-то сломалось... рефок не вижу.")
	}
	chunks := renderReferralList(groups)
	for i, body := range chunks {
		params := &telego.SendMessageParams{
			ChatID:    telego.ChatID{ID: msg.Chat.ID},
			Text:      body,
			ParseMode: telego.ModeHTML,
		}
		if i == 0 {
			params.ReplyParameters = &telego.ReplyParameters{MessageID: msg.MessageID}
		}
		if _, err := h.bot.SendMessage(context.Background(), params); err != nil {
			h.log.Warn("referral list send failed", "error", err, "chat_id", msg.Chat.ID)
			return err
		}
	}
	return nil
}

// renderReferralList builds the HTML chunks for /refs. Each chunk stays
// below Telegram's 4096-character limit and only breaks between
// referral entries; a service that spans chunks repeats its heading
// and optional effect so no entry loses its category context.
func renderReferralList(groups []referral.Group) []string {
	if len(groups) == 0 {
		return []string{"рефок пока нет... даже тут пусто."}
	}
	const hardLimit = 4096
	const safetyMargin = 64

	var chunks []string
	var b strings.Builder
	flush := func() {
		if b.Len() > 0 {
			chunks = append(chunks, b.String())
			b.Reset()
		}
	}
	for _, g := range groups {
		heading := "<b>" + html.EscapeString(g.Service.Name) + "</b>\n"
		effect := ""
		if strings.TrimSpace(g.Service.Effect) != "" {
			effect = "<i>" + html.EscapeString(strings.TrimSpace(g.Service.Effect)) + "</i>\n"
		}
		headingWritten := false
		for _, ref := range g.Referrals {
			entry := fmt.Sprintf("<code>#%d</code> · <a href=\"%s\">%s</a>\n",
				ref.ID, html.EscapeString(ref.URL), html.EscapeString(ref.OwnerDisplay))
			// Force-flush if appending entry (with heading repeat)
			// would overflow.
			piece := entry
			if !headingWritten {
				piece = heading + effect + entry
			}
			if b.Len()+len(piece) > hardLimit-safetyMargin && b.Len() > 0 {
				flush()
				headingWritten = false
				piece = heading + effect + entry
			}
			if !headingWritten {
				b.WriteString(heading)
				b.WriteString(effect)
				headingWritten = true
			}
			b.WriteString(entry)
		}
	}
	flush()
	if len(chunks) == 0 {
		return []string{"рефок пока нет... даже тут пусто."}
	}
	return chunks
}

// ---------------------------------------------------------------------------
// /refreg - open the picker
// ---------------------------------------------------------------------------

// HandleRegister opens the service picker. A new registration replaces
// that actor/chat's prior registration; moderation confirmations
// coexist by token.
func (h *ReferralHandler) HandleRegister(_ *th.Context, msg telego.Message) error {
	if msg.From == nil || msg.From.IsBot {
		return nil
	}
	absChat := storage.AbsChatID(msg.Chat.ID)
	groups, err := h.store.List(context.Background(), absChat)
	if err != nil {
		h.log.Warn("referral list failed", "error", err, "chat_id", msg.Chat.ID)
		return h.replyRef(msg, "что-то сломалось... сервисы не вижу.")
	}
	services := groupServices(groups)

	token := newToken()
	it := &referralInteraction{
		token:       token,
		kind:        kindRegistration,
		state:       statePicker,
		actorUserID: msg.From.ID,
		chatID:      msg.Chat.ID,
		createdAt:   time.Now(),
		expiresAt:   time.Now().Add(referralInteractionTTL),
		page:        0,
	}
	text, keyboard := renderPicker(services, it)
	sent, err := h.bot.SendMessage(context.Background(), &telego.SendMessageParams{
		ChatID:    telego.ChatID{ID: msg.Chat.ID},
		Text:      text,
		ParseMode: telego.ModeHTML,
		ReplyParameters: &telego.ReplyParameters{
			MessageID: msg.MessageID,
		},
		ReplyMarkup: keyboard,
	})
	if err != nil {
		h.log.Warn("referral picker send failed", "error", err, "chat_id", msg.Chat.ID)
		return err
	}
	it.promptMsgID = sent.MessageID
	h.putInteraction(it)
	return nil
}

func groupServices(groups []referral.Group) []referral.Service {
	out := make([]referral.Service, 0, len(groups))
	for _, g := range groups {
		out = append(out, g.Service)
	}
	return out
}

// renderPicker builds the picker text and inline keyboard for one page.
// Services are listed in the catalog's already-sorted order (case-
// insensitive name) so the index in rf:s:<idx> is stable across page
// navigation.
func renderPicker(services []referral.Service, it *referralInteraction) (string, *telego.InlineKeyboardMarkup) {
	text := "выбери сервис... или добавь новый."
	rows := [][]telego.InlineKeyboardButton{}

	total := len(services)
	pages := (total + referralPageSize - 1) / referralPageSize
	if pages == 0 {
		pages = 1
	}
	if it.page < 0 {
		it.page = 0
	}
	if it.page >= pages {
		it.page = pages - 1
	}
	start := it.page * referralPageSize
	end := start + referralPageSize
	if end > total {
		end = total
	}
	for i := start; i < end; i++ {
		svc := services[i]
		rows = append(rows, []telego.InlineKeyboardButton{
			{
				Text:         pickerLabel(svc),
				CallbackData: fmt.Sprintf("%s%d:%s", rfSelect, i, it.token),
			},
		})
	}

	// Nav row: ← / → on one row when pagination applies.
	if total > referralPageSize {
		var nav []telego.InlineKeyboardButton
		if it.page > 0 {
			nav = append(nav, telego.InlineKeyboardButton{
				Text:         "←",
				CallbackData: fmt.Sprintf("%s%d:%s", rfPage, it.page-1, it.token),
			})
		}
		if it.page+1 < pages {
			nav = append(nav, telego.InlineKeyboardButton{
				Text:         "→",
				CallbackData: fmt.Sprintf("%s%d:%s", rfPage, it.page+1, it.token),
			})
		}
		if len(nav) > 0 {
			rows = append(rows, nav)
		}
	}

	rows = append(rows, []telego.InlineKeyboardButton{
		{Text: "Новый сервис", CallbackData: rfNew + it.token},
	})
	return text, &telego.InlineKeyboardMarkup{InlineKeyboard: rows}
}

func pickerLabel(svc referral.Service) string {
	name := strings.TrimSpace(svc.Name)
	effect := strings.TrimSpace(svc.Effect)
	label := name
	if effect != "" {
		label = name + " — " + effect
	}
	if r := []rune(label); len(r) > referralLabelMax {
		label = string(r[:referralLabelMax-1]) + "…"
	}
	return label
}

// ---------------------------------------------------------------------------
// /refreport - admin moderation entry
// ---------------------------------------------------------------------------

// HandleReport opens an admin moderation confirmation for one referral
// by ID.
func (h *ReferralHandler) HandleReport(_ *th.Context, msg telego.Message) error {
	if msg.From == nil || msg.From.IsBot {
		return nil
	}
	if h.admins == nil {
		return h.replyRef(msg, "только админы могут это удалить...")
	}
	absChat := storage.AbsChatID(msg.Chat.ID)
	isAdmin, err := h.admins.IsAdmin(absChat, msg.From.ID)
	if err != nil || !isAdmin {
		return h.replyRef(msg, "только админы могут это удалить...")
	}
	arg := strings.TrimSpace(commandArgs(msg.Text))
	arg = strings.TrimPrefix(arg, "#")
	id, err := strconv.ParseUint(arg, 10, 64)
	if err != nil {
		return h.replyRef(msg, "такой рефки уже нет...")
	}
	ref, err := h.store.GetReferral(context.Background(), absChat, id)
	if err != nil {
		return h.replyRef(msg, "такой рефки уже нет...")
	}
	serviceName := ""
	groups, _ := h.store.List(context.Background(), absChat)
	for _, g := range groups {
		if g.Service.ID == ref.ServiceID {
			serviceName = g.Service.Name
			break
		}
	}
	text := fmt.Sprintf("удалить рефку #%d?\n%s\n%s — %s",
		ref.ID, html.EscapeString(serviceName), html.EscapeString(ref.OwnerDisplay), html.EscapeString(ref.URL))
	token := newToken()
	it := &referralInteraction{
		token:       token,
		kind:        kindModeration,
		state:       stateModeration,
		actorUserID: msg.From.ID,
		chatID:      msg.Chat.ID,
		createdAt:   time.Now(),
		expiresAt:   time.Now().Add(referralInteractionTTL),
		reportID:    ref.ID,
	}
	sent, err := h.bot.SendMessage(context.Background(), &telego.SendMessageParams{
		ChatID:    telego.ChatID{ID: msg.Chat.ID},
		Text:      text,
		ParseMode: telego.ModeHTML,
		ReplyParameters: &telego.ReplyParameters{
			MessageID: msg.MessageID,
		},
		ReplyMarkup: moderationKeyboard(token),
	})
	if err != nil {
		h.log.Warn("referral report send failed", "error", err, "chat_id", msg.Chat.ID)
		return err
	}
	it.promptMsgID = sent.MessageID
	h.putInteraction(it)
	return nil
}

func moderationKeyboard(token string) *telego.InlineKeyboardMarkup {
	return &telego.InlineKeyboardMarkup{
		InlineKeyboard: [][]telego.InlineKeyboardButton{
			{
				{Text: "Неверное оформление", CallbackData: rfDeleteBad + token},
				{Text: "Скам", CallbackData: rfDeleteScam + token},
			},
			{
				{Text: "Отмена", CallbackData: rfCancel + token},
			},
		},
	}
}

// ---------------------------------------------------------------------------
// Callback dispatch
// ---------------------------------------------------------------------------

// ReferralCallbackPredicate matches only supergroup callbacks whose
// data is in the rf: namespace and whose message is present (we need
// the chat id and prompt message id).
func ReferralCallbackPredicate() th.Predicate {
	return func(_ context.Context, update telego.Update) bool {
		cb := update.CallbackQuery
		if cb == nil || cb.Message == nil {
			return false
		}
		if cb.Message.GetChat().Type != telego.ChatTypeSupergroup {
			return false
		}
		return strings.HasPrefix(cb.Data, rfCallbackPrefix)
	}
}

// HandleCallback answers every referral-namespace callback. Wrong
// actor/chat gets a transient alert; expired/missing interactions get
// a re-run prompt; completed/cancelled interactions clear the
// keyboard.
func (h *ReferralHandler) HandleCallback(_ *th.Context, query telego.CallbackQuery) error {
	answer := h.dispatchCallback(query)
	if answer == "" {
		answer = "ок"
	}
	_ = h.bot.AnswerCallbackQuery(context.Background(), &telego.AnswerCallbackQueryParams{
		CallbackQueryID: query.ID,
		Text:            answer,
		ShowAlert:       len(answer) > 100,
	})
	return nil
}

// dispatchCallback routes one rf: tap to its handler. Returns the
// toast text to surface via AnswerCallbackQuery (empty for silent).
func (h *ReferralHandler) dispatchCallback(query telego.CallbackQuery) string {
	data := query.Data
	chatID := query.Message.GetChat().ID

	token, ok := rfToken(data)
	if !ok {
		return ""
	}
	it := h.getInteraction(token)
	if it == nil || time.Now().After(it.expiresAt) {
		h.dropInteraction(token)
		return "эта кнопка уже устала... запусти команду заново."
	}
	if it.chatID != chatID || it.actorUserID != query.From.ID {
		return "это не твоя кнопка..."
	}

	switch {
	case strings.HasPrefix(data, rfPage):
		return h.cbPage(query, it, data)
	case strings.HasPrefix(data, rfSelect):
		return h.cbSelect(query, it, data)
	case strings.HasPrefix(data, rfNew):
		return h.cbNew(query, it)
	case strings.HasPrefix(data, rfMatch):
		return h.cbMatch(query, it, data)
	case strings.HasPrefix(data, rfCreate):
		return h.cbCreate(query, it)
	case strings.HasPrefix(data, rfDeleteBad), strings.HasPrefix(data, rfDeleteScam):
		return h.cbDelete(query, it, data)
	case strings.HasPrefix(data, rfCancel):
		return h.cbCancel(query, it)
	}
	return ""
}

func (h *ReferralHandler) cbPage(query telego.CallbackQuery, it *referralInteraction, data string) string {
	rest := strings.TrimPrefix(data, rfPage)
	parts := strings.SplitN(rest, ":", 2)
	if len(parts) != 2 || parts[1] != it.token {
		return ""
	}
	page, err := strconv.Atoi(parts[0])
	if err != nil || page < 0 {
		return ""
	}
	it.page = page
	groups, err := h.store.List(context.Background(), storage.AbsChatID(it.chatID))
	if err != nil {
		return "что-то сломалось..."
	}
	text, kb := renderPicker(groupServices(groups), it)
	h.editPrompt(it, text, kb)
	return ""
}

func (h *ReferralHandler) cbSelect(query telego.CallbackQuery, it *referralInteraction, data string) string {
	rest := strings.TrimPrefix(data, rfSelect)
	parts := strings.SplitN(rest, ":", 2)
	if len(parts) != 2 || parts[1] != it.token {
		return ""
	}
	idx, err := strconv.Atoi(parts[0])
	if err != nil || idx < 0 {
		return ""
	}
	groups, err := h.store.List(context.Background(), storage.AbsChatID(it.chatID))
	if err != nil {
		return "что-то сломалось..."
	}
	services := groupServices(groups)
	if idx >= len(services) {
		return "этого сервиса уже нет... запусти /refreg ещё раз."
	}
	svc := services[idx]
	it.selectedServiceID = svc.ID
	it.state = stateAwaitURL
	h.editPrompt(it, existingServicePrompt(svc), cancelKeyboard(it.token))
	return ""
}

func existingServicePrompt(svc referral.Service) string {
	var b strings.Builder
	b.WriteString("<b>" + html.EscapeString(strings.TrimSpace(svc.Name)) + "</b>\n")
	if strings.TrimSpace(svc.Effect) != "" {
		b.WriteString("<i>" + html.EscapeString(strings.TrimSpace(svc.Effect)) + "</i>\n")
	}
	b.WriteString("ответь на это сообщение одной https-ссылкой.")
	return b.String()
}

func (h *ReferralHandler) cbNew(query telego.CallbackQuery, it *referralInteraction) string {
	it.state = stateAwaitNew
	it.draftSvc = referral.Service{}
	it.draftURL = ""
	h.editPrompt(it,
		"ответь на это сообщение:\nназвание сервиса\nэффект (строку можно пропустить)\nhttps-ссылка",
		cancelKeyboard(it.token))
	return ""
}

func (h *ReferralHandler) cbMatch(query telego.CallbackQuery, it *referralInteraction, data string) string {
	rest := strings.TrimPrefix(data, rfMatch)
	parts := strings.SplitN(rest, ":", 2)
	if len(parts) != 2 || parts[1] != it.token {
		return ""
	}
	idx, err := strconv.Atoi(parts[0])
	if err != nil || idx < 0 || idx >= len(it.matches) {
		return ""
	}
	chosen := it.matches[idx].Service
	return h.commitWithService(it, chosen.ID, it.draftURL, &query.From)
}

func (h *ReferralHandler) cbCreate(query telego.CallbackQuery, it *referralInteraction) string {
	// User insisted on creating a NEW service despite fuzzy/exact matches.
	return h.commitWithService(it, 0, it.draftURL, &query.From)
}

// commitWithService stores the draft referral. serviceID==0 means
// "create a new service from draftSvc"; otherwise reuse the existing
// service. url must be non-empty.
func (h *ReferralHandler) commitWithService(it *referralInteraction, serviceID uint64, rawURL string, from *telego.User) string {
	if strings.TrimSpace(rawURL) == "" {
		return ""
	}
	absChat := storage.AbsChatID(it.chatID)
	svc := it.draftSvc
	svc.ID = serviceID
	ref := referral.Referral{
		OwnerUserID:  it.actorUserID,
		OwnerDisplay: ownerDisplay(from),
		URL:          rawURL,
	}
	storedSvc, storedRef, err := h.store.Create(context.Background(), absChat, svc, ref)
	if err != nil {
		return h.handleCreateError(it, err, from)
	}
	h.dropInteraction(it.token)
	h.editPrompt(it,
		fmt.Sprintf("Рефка #%d сохранена в %s.", storedRef.ID, html.EscapeString(storedSvc.Name)),
		emptyKeyboard())
	return ""
}

func (h *ReferralHandler) handleCreateError(it *referralInteraction, err error, from *telego.User) string {
	switch {
	case errors.Is(err, referral.ErrServiceExists):
		h.rerunMatchForDraft(it, from)
		return ""
	case errors.Is(err, referral.ErrOwnerServiceExists):
		return "у тебя уже есть рефка для этого сервиса... сначала попроси админа удалить её."
	case errors.Is(err, referral.ErrURLExists):
		return "эта ссылка уже есть в списке..."
	case errors.Is(err, referral.ErrNotFound):
		return "этого сервиса уже нет... запусти /refreg ещё раз."
	default:
		h.log.Warn("referral create failed", "error", err, "chat_id", it.chatID)
		return "что-то сломалось... рефка не сохранена."
	}
}

// rerunMatchForDraft re-runs MatchServices against the live catalog
// and re-renders the fuzzy/exact prompt. Used when ErrServiceExists
// surfaces during a race.
func (h *ReferralHandler) rerunMatchForDraft(it *referralInteraction, from *telego.User) {
	absChat := storage.AbsChatID(it.chatID)
	groups, err := h.store.List(context.Background(), absChat)
	if err != nil {
		h.log.Warn("referral rerun list failed", "error", err, "chat_id", it.chatID)
		return
	}
	services := groupServices(groups)
	matches := referral.MatchServices(it.draftSvc.Name, services)
	if len(matches) == 0 {
		return
	}
	it.matches = matches
	it.state = stateAwaitChoice
	text, kb := choicePrompt(matches, it.token)
	h.editPrompt(it, text, kb)
}

func (h *ReferralHandler) cbDelete(query telego.CallbackQuery, it *referralInteraction, data string) string {
	if h.admins == nil {
		return "только админы могут это удалить..."
	}
	absChat := storage.AbsChatID(it.chatID)
	ok, err := h.admins.IsAdmin(absChat, query.From.ID)
	if err != nil || !ok {
		return "только админы могут это удалить..."
	}
	reason := "оформление"
	if strings.HasPrefix(data, rfDeleteScam) {
		reason = "скам"
	}
	if err := h.store.DeleteReferral(context.Background(), absChat, it.reportID); err != nil {
		if errors.Is(err, referral.ErrNotFound) {
			return "такой рефки уже нет..."
		}
		h.log.Warn("referral delete failed", "error", err, "chat_id", it.chatID)
		return "что-то сломалось... рефка не удалена."
	}
	h.dropInteraction(it.token)
	h.editPrompt(it,
		fmt.Sprintf("Рефка #%d удалена: %s.", it.reportID, reason),
		emptyKeyboard())
	return ""
}

func (h *ReferralHandler) cbCancel(query telego.CallbackQuery, it *referralInteraction) string {
	h.dropInteraction(it.token)
	h.editPrompt(it, "отменено... как обычно.", emptyKeyboard())
	return ""
}

// editPrompt replaces the prompt message text/keyboard, logging errors.
func (h *ReferralHandler) editPrompt(it *referralInteraction, text string, kb *telego.InlineKeyboardMarkup) {
	if _, err := h.bot.EditMessageText(context.Background(), &telego.EditMessageTextParams{
		ChatID:      telego.ChatID{ID: it.chatID},
		MessageID:   it.promptMsgID,
		Text:        text,
		ParseMode:   telego.ModeHTML,
		ReplyMarkup: kb,
	}); err != nil {
		h.log.Warn("referral prompt edit failed", "error", err, "chat_id", it.chatID)
	}
}

// choicePrompt renders the exact-effect conflict prompt (one match) or
// the fuzzy "is it one of these?" prompt (multiple matches). Up to
// three Name — Effect buttons, then "Нет, это новый сервис" for the
// fuzzy path or "Отмена" for the exact-effect path.
func choicePrompt(matches []referral.Match, token string) (string, *telego.InlineKeyboardMarkup) {
	rows := [][]telego.InlineKeyboardButton{}
	limit := len(matches)
	if limit > 3 {
		limit = 3
	}
	for i := 0; i < limit; i++ {
		rows = append(rows, []telego.InlineKeyboardButton{
			{
				Text:         pickerLabel(matches[i].Service),
				CallbackData: fmt.Sprintf("%s%d:%s", rfMatch, i, token),
			},
		})
	}
	if matches[0].Exact {
		// Exact-name conflict: no "create new" escape; the user must
		// pick the existing service or cancel.
		rows = append(rows, []telego.InlineKeyboardButton{
			{Text: "Отмена", CallbackData: rfCancel + token},
		})
		var b strings.Builder
		existing := matches[0].Service
		b.WriteString(" такой сервис уже есть:\n")
		b.WriteString("<b>" + html.EscapeString(strings.TrimSpace(existing.Name)) + "</b>\n")
		if strings.TrimSpace(existing.Effect) != "" {
			b.WriteString("<i>" + html.EscapeString(strings.TrimSpace(existing.Effect)) + "</i>\n")
		}
		b.WriteString("выбери его, чтобы добавить свою рефку, или отмени.")
		return b.String(), &telego.InlineKeyboardMarkup{InlineKeyboard: rows}
	}
	rows = append(rows, []telego.InlineKeyboardButton{
		{Text: "Нет, это новый сервис", CallbackData: rfCreate + token},
	})
	return "Это уже один из этих сервисов?", &telego.InlineKeyboardMarkup{InlineKeyboard: rows}
}

// ---------------------------------------------------------------------------
// Registration input (text reply to a prompt)
// ---------------------------------------------------------------------------

// RegistrationInputPredicate matches only text messages that reply to
// an active registration prompt for the same actor/chat.
func (h *ReferralHandler) RegistrationInputPredicate() th.Predicate {
	return func(_ context.Context, update telego.Update) bool {
		m := update.Message
		if m == nil || m.Text == "" || m.From == nil {
			return false
		}
		if m.Chat.Type != telego.ChatTypeSupergroup {
			return false
		}
		if m.ReplyToMessage == nil {
			return false
		}
		return h.findAwaiting(m.From.ID, m.Chat.ID, m.ReplyToMessage.MessageID) != nil
	}
}

// HandleRegistrationInput consumes one reply to an active prompt.
func (h *ReferralHandler) HandleRegistrationInput(_ *th.Context, msg telego.Message) error {
	if msg.From == nil {
		return nil
	}
	it := h.findAwaiting(msg.From.ID, msg.Chat.ID, msg.ReplyToMessage.MessageID)
	if it == nil {
		return nil
	}
	switch it.state {
	case stateAwaitURL:
		return h.handleURLReply(it, msg)
	case stateAwaitNew:
		return h.handleNewReply(it, msg)
	}
	return nil
}

func (h *ReferralHandler) handleURLReply(it *referralInteraction, msg telego.Message) error {
	rawURL, ok := validateURL(msg.Text)
	if !ok {
		return h.replyRef(msg, "нужна полная https-ссылка...")
	}
	absChat := storage.AbsChatID(it.chatID)
	svc := referral.Service{ID: it.selectedServiceID}
	ref := referral.Referral{
		OwnerUserID:  it.actorUserID,
		OwnerDisplay: ownerDisplay(msg.From),
		URL:          rawURL,
	}
	storedSvc, storedRef, err := h.store.Create(context.Background(), absChat, svc, ref)
	if err != nil {
		toast := h.handleCreateError(it, err, msg.From)
		if toast != "" {
			_ = h.replyRef(msg, toast)
		}
		return nil
	}
	h.dropInteraction(it.token)
	h.editPrompt(it,
		fmt.Sprintf("Рефка #%d сохранена в %s.", storedRef.ID, html.EscapeString(storedSvc.Name)),
		emptyKeyboard())
	return nil
}

func (h *ReferralHandler) handleNewReply(it *referralInteraction, msg telego.Message) error {
	lines := strings.Split(strings.TrimRight(msg.Text, "\n"), "\n")
	var name, effect, rawURL string
	switch len(lines) {
	case 2:
		name, rawURL = lines[0], lines[1]
	case 3:
		name, effect, rawURL = lines[0], lines[1], lines[2]
	default:
		return h.replyRef(msg, "ответь двумя строками: сервис, ссылка; или тремя: сервис, эффект, ссылка.")
	}

	cleanName, nameErr := validateServiceName(name)
	if nameErr != nil {
		return h.replyRef(msg, "название сервиса не похоже на название...")
	}
	cleanEffect, effectErr := validateEffect(effect)
	if effectErr != nil {
		return h.replyRef(msg, "слишком длинно...")
	}
	url, urlOK := validateURL(rawURL)
	if !urlOK {
		return h.replyRef(msg, "нужна полная https-ссылка...")
	}

	it.draftSvc = referral.Service{Name: cleanName, Effect: cleanEffect, NameKey: referral.NormalizeName(cleanName)}
	it.draftURL = url

	absChat := storage.AbsChatID(it.chatID)
	groups, err := h.store.List(context.Background(), absChat)
	if err != nil {
		h.log.Warn("referral list failed", "error", err, "chat_id", it.chatID)
		return h.replyRef(msg, "что-то сломалось... рефка не сохранена.")
	}
	services := groupServices(groups)
	matches := referral.MatchServices(cleanName, services)
	if len(matches) == 0 {
		return h.commitNewDraftWithService(it, msg, 0)
	}
	if matches[0].Exact {
		existing := matches[0].Service
		if effectCompatible(it.draftSvc.Effect, existing.Effect) {
			return h.commitNewDraftWithService(it, msg, existing.ID)
		}
		it.matches = matches
		it.state = stateAwaitChoice
		text, kb := choicePrompt(matches, it.token)
		h.editPrompt(it, text, kb)
		return nil
	}
	it.matches = matches
	it.state = stateAwaitChoice
	text, kb := choicePrompt(matches, it.token)
	h.editPrompt(it, text, kb)
	return nil
}

func (h *ReferralHandler) commitNewDraftWithService(it *referralInteraction, msg telego.Message, serviceID uint64) error {
	absChat := storage.AbsChatID(it.chatID)
	svc := it.draftSvc
	svc.ID = serviceID
	ref := referral.Referral{
		OwnerUserID:  it.actorUserID,
		OwnerDisplay: ownerDisplay(msg.From),
		URL:          it.draftURL,
	}
	storedSvc, storedRef, err := h.store.Create(context.Background(), absChat, svc, ref)
	if err != nil {
		toast := h.handleCreateError(it, err, msg.From)
		if toast != "" {
			_ = h.replyRef(msg, toast)
		}
		return nil
	}
	h.dropInteraction(it.token)
	h.editPrompt(it,
		fmt.Sprintf("Рефка #%d сохранена в %s.", storedRef.ID, html.EscapeString(storedSvc.Name)),
		emptyKeyboard())
	return nil
}

// ---------------------------------------------------------------------------
// Interaction state management
// ---------------------------------------------------------------------------

func (h *ReferralHandler) putInteraction(it *referralInteraction) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pruneLocked()
	if it.kind == kindRegistration {
		key := actorChatKey{userID: it.actorUserID, chatID: it.chatID}
		if old, ok := h.byActorChat[key]; ok {
			delete(h.byToken, old)
		}
		h.byActorChat[key] = it.token
	}
	h.byToken[it.token] = it
}

func (h *ReferralHandler) getInteraction(token string) *referralInteraction {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.byToken[token]
}

func (h *ReferralHandler) dropInteraction(token string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if it, ok := h.byToken[token]; ok {
		if it.kind == kindRegistration {
			key := actorChatKey{userID: it.actorUserID, chatID: it.chatID}
			if cur, ok := h.byActorChat[key]; ok && cur == token {
				delete(h.byActorChat, key)
			}
		}
		delete(h.byToken, token)
	}
}

// findAwaiting returns the registration interaction waiting on a text
// reply from userID in chatID to promptMsgID, or nil.
func (h *ReferralHandler) findAwaiting(userID, chatID int64, promptMsgID int) *referralInteraction {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pruneLocked()
	key := actorChatKey{userID: userID, chatID: chatID}
	token, ok := h.byActorChat[key]
	if !ok {
		return nil
	}
	it := h.byToken[token]
	if it == nil || it.promptMsgID != promptMsgID {
		return nil
	}
	if it.state != stateAwaitURL && it.state != stateAwaitNew && it.state != stateAwaitChoice {
		return nil
	}
	return it
}

func (h *ReferralHandler) pruneLocked() {
	now := time.Now()
	for tok, it := range h.byToken {
		if now.After(it.expiresAt) {
			if it.kind == kindRegistration {
				key := actorChatKey{userID: it.actorUserID, chatID: it.chatID}
				if cur, ok := h.byActorChat[key]; ok && cur == tok {
					delete(h.byActorChat, key)
				}
			}
			delete(h.byToken, tok)
		}
	}
}

// ---------------------------------------------------------------------------
// Validation
// ---------------------------------------------------------------------------

func validateServiceName(raw string) (string, error) {
	clean := collapseWhitespace(raw)
	if utf8.RuneCountInString(clean) > 80 {
		return "", errTooLong
	}
	if !hasLetterOrDigit(clean) {
		return "", errInvalidName
	}
	return clean, nil
}

func validateEffect(raw string) (string, error) {
	clean := collapseWhitespace(raw)
	if utf8.RuneCountInString(clean) > 160 {
		return "", errTooLong
	}
	return clean, nil
}

// validateURL trims, caps at 2048 bytes, requires https + non-empty
// host. No canonicalization beyond trim: duplicate comparison uses the
// raw trimmed string.
func validateURL(raw string) (string, bool) {
	u := strings.TrimSpace(raw)
	if u == "" || len(u) > 2048 {
		return "", false
	}
	parsed, err := url.ParseRequestURI(u)
	if err != nil {
		return "", false
	}
	if parsed.Scheme != "https" || parsed.Host == "" {
		return "", false
	}
	return u, true
}

func hasLetterOrDigit(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return true
		}
	}
	return false
}

func collapseWhitespace(s string) string {
	var b strings.Builder
	prevSpace := false
	for _, r := range s {
		if unicode.IsSpace(r) {
			if !prevSpace {
				b.WriteRune(' ')
				prevSpace = true
			}
			continue
		}
		prevSpace = false
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}

func effectCompatible(submitted, persisted string) bool {
	submitted = collapseWhitespace(submitted)
	persisted = collapseWhitespace(persisted)
	if submitted == "" {
		return true
	}
	return strings.EqualFold(submitted, persisted)
}

func ownerDisplay(from *telego.User) string {
	if from == nil {
		return ""
	}
	display := shared.UserDisplay(from.Username, from.FirstName)
	if display == "" {
		return strconv.FormatInt(from.ID, 10)
	}
	return display
}

func newToken() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// rfToken extracts the trailing token from any rf: callback. Tokens
// are the last ":"-separated segment, always 16 hex chars.
func rfToken(data string) (string, bool) {
	idx := strings.LastIndexByte(data, ':')
	if idx < 0 || idx+1 >= len(data) {
		return "", false
	}
	token := data[idx+1:]
	if len(token) != 16 {
		return "", false
	}
	return token, true
}

// cancelKeyboard is a single-row Cancel button used while a prompt
// awaits a reply.
func cancelKeyboard(token string) *telego.InlineKeyboardMarkup {
	return &telego.InlineKeyboardMarkup{
		InlineKeyboard: [][]telego.InlineKeyboardButton{
			{{Text: "Отмена", CallbackData: rfCancel + token}},
		},
	}
}

func (h *ReferralHandler) replyRef(msg telego.Message, body string) error {
	_, err := h.bot.SendMessage(context.Background(), &telego.SendMessageParams{
		ChatID:    telego.ChatID{ID: msg.Chat.ID},
		Text:      body,
		ParseMode: telego.ModeHTML,
		ReplyParameters: &telego.ReplyParameters{
			MessageID: msg.MessageID,
		},
	})
	if err != nil {
		h.log.Warn("referral reply failed", "error", err, "chat_id", msg.Chat.ID)
	}
	return err
}

var (
	errTooLong     = errors.New("referral: too long")
	errInvalidName = errors.New("referral: invalid service name")
)

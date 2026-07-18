package bot

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mymmrac/telego"

	"github.com/veschin/bidlobot/internal/domain/referral"
	"github.com/veschin/bidlobot/internal/storage"
)

// stubRefSender captures every SendMessage, EditMessageText, and
// AnswerCallbackQuery for assertion. The message ID it returns on send
// increments so a test can build reply chains.
type stubRefSender struct {
	mu sync.Mutex

	nextID   int
	Sent     []*telego.SendMessageParams
	Edited   []*telego.EditMessageTextParams
	Answered []*telego.AnswerCallbackQueryParams

	SendErr error
}

func newStubRefSender() *stubRefSender { return &stubRefSender{} }

func (s *stubRefSender) SendMessage(_ context.Context, p *telego.SendMessageParams) (*telego.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.SendErr != nil {
		return nil, s.SendErr
	}
	s.Sent = append(s.Sent, p)
	s.nextID++
	return &telego.Message{MessageID: 5000 + s.nextID, Chat: telego.Chat{ID: chatIDFromParams(p.ChatID)}}, nil
}

func (s *stubRefSender) EditMessageText(_ context.Context, p *telego.EditMessageTextParams) (*telego.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Edited = append(s.Edited, p)
	return &telego.Message{MessageID: p.MessageID, Chat: telego.Chat{ID: chatIDFromParams(p.ChatID)}}, nil
}

func (s *stubRefSender) AnswerCallbackQuery(_ context.Context, p *telego.AnswerCallbackQueryParams) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Answered = append(s.Answered, p)
	return nil
}

func (s *stubRefSender) lastSentText() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.Sent) == 0 {
		return ""
	}
	return s.Sent[len(s.Sent)-1].Text
}

func (s *stubRefSender) lastEditText() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.Edited) == 0 {
		return ""
	}
	return s.Edited[len(s.Edited)-1].Text
}

// lastSentKeyboard returns the inline keyboard of the last sent
// message, or nil if it had none.
func (s *stubRefSender) lastSentKeyboard() *telego.InlineKeyboardMarkup {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.Sent) == 0 {
		return nil
	}
	markup, ok := s.Sent[len(s.Sent)-1].ReplyMarkup.(*telego.InlineKeyboardMarkup)
	if !ok {
		return nil
	}
	return markup
}

func chatIDFromParams(id telego.ChatID) int64 { return id.ID }

// findButton searches the keyboard for a button whose text matches
// `label` and returns its callback data.
func findButton(kb *telego.InlineKeyboardMarkup, label string) (string, bool) {
	if kb == nil {
		return "", false
	}
	for _, row := range kb.InlineKeyboard {
		for _, btn := range row {
			if btn.Text == label {
				return btn.CallbackData, true
			}
		}
	}
	return "", false
}

// newRefTestHandler builds a ReferralHandler against a fresh bbolt DB.
func newRefTestHandler(t *testing.T, admins AdminChecker) (*ReferralHandler, *stubRefSender, *storage.ReferralRepo, func()) {
	t.Helper()
	dir := t.TempDir()
	bs, err := storage.NewBoltStore(filepath.Join(dir, "refs.db"))
	if err != nil {
		t.Fatalf("NewBoltStore: %v", err)
	}
	cleanup := func() { bs.Close() }
	repo := storage.NewReferralRepo(bs.DB())
	sender := newStubRefSender()
	h := NewReferralHandler(sender, repo, admins, testLogger())
	return h, sender, repo, cleanup
}

func refTestChat() telego.Chat {
	return telego.Chat{ID: -100777, Type: telego.ChatTypeSupergroup}
}

func refMsg(text string, from *telego.User) telego.Message {
	return telego.Message{MessageID: 1, Chat: refTestChat(), From: from, Text: text, Date: 1}
}

func refReply(text string, from *telego.User, replyToMsgID int) telego.Message {
	m := refMsg(text, from)
	m.MessageID = 2
	m.ReplyToMessage = &telego.Message{MessageID: replyToMsgID, Chat: refTestChat()}
	return m
}

func refCallback(data string, from *telego.User, chatID int64, msgID int) telego.CallbackQuery {
	return telego.CallbackQuery{
		ID:   "1",
		From: *from,
		Data: data,
		Message: &telego.Message{
			MessageID: msgID,
			Chat:      telego.Chat{ID: chatID, Type: telego.ChatTypeSupergroup},
		},
	}
}

// ---------------------------------------------------------------------------
// /refs empty
// ---------------------------------------------------------------------------

func TestReferralListEmpty(t *testing.T) {
	h, sender, _, cleanup := newRefTestHandler(t, nil)
	defer cleanup()

	if err := h.HandleList(nil, refMsg("/refs", aliceUser)); err != nil {
		t.Fatal(err)
	}
	if got := sender.lastSentText(); !strings.Contains(got, "рефок пока нет") {
		t.Fatalf("empty /refs copy, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// /refreg picker is sent
// ---------------------------------------------------------------------------

func TestReferralRegisterSendsPicker(t *testing.T) {
	h, sender, _, cleanup := newRefTestHandler(t, nil)
	defer cleanup()

	if err := h.HandleRegister(nil, refMsg("/refreg", aliceUser)); err != nil {
		t.Fatal(err)
	}
	if got := sender.lastSentText(); !strings.Contains(got, "выбери сервис") {
		t.Fatalf("picker copy, got %q", got)
	}
	kb := sender.lastSentKeyboard()
	if _, ok := findButton(kb, "Новый сервис"); !ok {
		t.Fatalf("picker missing 'Новый сервис' button: %+v", kb)
	}
}

// ---------------------------------------------------------------------------
// End-to-end: Alice new service → Bob reuse → non-admin → admin remove
// ---------------------------------------------------------------------------

func TestReferralFlowEndToEnd(t *testing.T) {
	admins := &fixedAdminChecker{allow: false}
	h, sender, repo, cleanup := newRefTestHandler(t, admins)
	defer cleanup()

	// --- Alice: /refreg → "Новый сервис" → reply with 3 lines.
	if err := h.HandleRegister(nil, refMsg("/refreg", aliceUser)); err != nil {
		t.Fatal(err)
	}
	pickerMsgID := sender.Sent[len(sender.Sent)-1].ReplyMarkup
	_ = pickerMsgID
	pickerMessageID := 5001 // first SendMessage returned 5000+1
	cbData, ok := findButton(sender.lastSentKeyboard(), "Новый сервис")
	if !ok {
		t.Fatal("missing 'Новый сервис' button")
	}
	if err := h.HandleCallback(nil, refCallback(cbData, aliceUser, -100777, pickerMessageID)); err != nil {
		t.Fatal(err)
	}
	// The prompt for the new-service reply was edited into the same
	// picker message (ID 5001). Reply to it with the 3-line draft.
	draft := "ZAI Coding Plan\n+5 баксов на подписку\nhttps://z.ai/ref/alice"
	if err := h.HandleRegistrationInput(nil, refReply(draft, aliceUser, pickerMessageID)); err != nil {
		t.Fatal(err)
	}
	editText := sender.lastEditText()
	if !strings.Contains(editText, "Рефка #1 сохранена в ZAI Coding Plan.") {
		t.Fatalf("alice success copy, got %q", editText)
	}

	// --- Bob: /refreg → tap ZAI service → reply with URL only.
	if err := h.HandleRegister(nil, refMsg("/refreg", bobUser)); err != nil {
		t.Fatal(err)
	}
	bobPickerID := 5002
	cbData, ok = findButton(sender.lastSentKeyboard(), "ZAI Coding Plan — +5 баксов на подписку")
	if !ok {
		t.Fatalf("missing existing-service button: %+v", sender.lastSentKeyboard())
	}
	if err := h.HandleCallback(nil, refCallback(cbData, bobUser, -100777, bobPickerID)); err != nil {
		t.Fatal(err)
	}
	if err := h.HandleRegistrationInput(nil, refReply("https://z.ai/ref/bob", bobUser, bobPickerID)); err != nil {
		t.Fatal(err)
	}
	if got := sender.lastEditText(); !strings.Contains(got, "Рефка #2 сохранена в ZAI Coding Plan.") {
		t.Fatalf("bob success copy, got %q", got)
	}

	// --- /refs: one ZAI group, effect line, #1/alice and #2/bob.
	sender.Sent = nil
	if err := h.HandleList(nil, refMsg("/refs", aliceUser)); err != nil {
		t.Fatal(err)
	}
	body := sender.lastSentText()
	if !strings.Contains(body, "<b>ZAI Coding Plan</b>") {
		t.Fatalf("/refs missing service heading, got %q", body)
	}
	if !strings.Contains(body, "<i>+5 баксов на подписку</i>") {
		t.Fatalf("/refs missing effect line, got %q", body)
	}
	if !strings.Contains(body, "#1") || !strings.Contains(body, "alice") {
		t.Fatalf("/refs missing #1/alice, got %q", body)
	}
	if !strings.Contains(body, "#2") || !strings.Contains(body, "bob") {
		t.Fatalf("/refs missing #2/bob, got %q", body)
	}
	if !strings.Contains(body, `href="https://z.ai/ref/alice"`) {
		t.Fatalf("/refs missing clickable alice link, got %q", body)
	}

	// --- Non-admin /refreport: rejected, catalog unchanged.
	_, err := repo.GetReferral(context.Background(), 100777, 1)
	if err != nil {
		t.Fatalf("precond: referral 1 missing: %v", err)
	}
	if err := h.HandleReport(nil, refMsg("/refreport 1", carolUser)); err != nil {
		t.Fatal(err)
	}
	if got := sender.lastSentText(); !strings.Contains(got, "только админы могут это удалить") {
		t.Fatalf("non-admin report copy, got %q", got)
	}
	if _, err := repo.GetReferral(context.Background(), 100777, 1); err != nil {
		t.Fatalf("non-admin report must not delete: %v", err)
	}

	// --- Admin /refreport 1 → Скам → only #1 removed.
	admins.allow = true // promote the actor (carol) for the next call
	sender.Sent = nil
	if err := h.HandleReport(nil, refMsg("/refreport 1", carolUser)); err != nil {
		t.Fatal(err)
	}
	if got := sender.lastSentText(); !strings.Contains(got, "удалить рефку #1") {
		t.Fatalf("admin report copy, got %q", got)
	}
	kb := sender.lastSentKeyboard()
	scamCB, ok := findButton(kb, "Скам")
	if !ok {
		t.Fatalf("missing 'Скам' button: %+v", kb)
	}
	reportMsgID := 5003
	if err := h.HandleCallback(nil, refCallback(scamCB, carolUser, -100777, reportMsgID)); err != nil {
		t.Fatal(err)
	}
	if got := sender.lastEditText(); !strings.Contains(got, "Рефка #1 удалена: скам.") {
		t.Fatalf("removal copy, got %q", got)
	}

	// --- Final /refs: Bob preserved, Alice gone.
	sender.Sent = nil
	if err := h.HandleList(nil, refMsg("/refs", aliceUser)); err != nil {
		t.Fatal(err)
	}
	final := sender.lastSentText()
	if strings.Contains(final, "#1") {
		t.Fatalf("/refs still contains #1 after removal: %q", final)
	}
	if !strings.Contains(final, "#2") || !strings.Contains(final, "bob") {
		t.Fatalf("/refs must retain bob after alice removed: %q", final)
	}
}

// ---------------------------------------------------------------------------
// Actor lock: a callback from a different user is rejected
// ---------------------------------------------------------------------------

func TestReferralCallbackWrongActorRejected(t *testing.T) {
	h, sender, _, cleanup := newRefTestHandler(t, nil)
	defer cleanup()

	if err := h.HandleRegister(nil, refMsg("/refreg", aliceUser)); err != nil {
		t.Fatal(err)
	}
	pickerMsgID := 5001
	cbData, ok := findButton(sender.lastSentKeyboard(), "Новый сервис")
	if !ok {
		t.Fatal("missing 'Новый сервис' button")
	}
	// Bob taps Alice's button.
	if err := h.HandleCallback(nil, refCallback(cbData, bobUser, -100777, pickerMsgID)); err != nil {
		t.Fatal(err)
	}
	if len(sender.Answered) != 1 {
		t.Fatalf("expected 1 callback answer, got %d", len(sender.Answered))
	}
	if !strings.Contains(sender.Answered[0].Text, "не твоя кнопка") {
		t.Fatalf("wrong-actor alert, got %q", sender.Answered[0].Text)
	}
}

// ---------------------------------------------------------------------------
// Validation: bad URL reply is rejected, prompt stays active
// ---------------------------------------------------------------------------

func TestReferralNewServiceBadURL(t *testing.T) {
	h, sender, _, cleanup := newRefTestHandler(t, nil)
	defer cleanup()

	if err := h.HandleRegister(nil, refMsg("/refreg", aliceUser)); err != nil {
		t.Fatal(err)
	}
	pickerMsgID := 5001
	cbData, _ := findButton(sender.lastSentKeyboard(), "Новый сервис")
	if err := h.HandleCallback(nil, refCallback(cbData, aliceUser, -100777, pickerMsgID)); err != nil {
		t.Fatal(err)
	}
	if err := h.HandleRegistrationInput(nil, refReply("MyService\nnot-a-url", aliceUser, pickerMsgID)); err != nil {
		t.Fatal(err)
	}
	if got := sender.lastSentText(); !strings.Contains(got, "нужна полная https-ссылка") {
		t.Fatalf("bad URL reply copy, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// HTML escaping: a crafted service name and URL cannot inject markup
// ---------------------------------------------------------------------------

func TestReferralHTMLEscaping(t *testing.T) {
	h, sender, _, cleanup := newRefTestHandler(t, nil)
	defer cleanup()

	if err := h.HandleRegister(nil, refMsg("/refreg", aliceUser)); err != nil {
		t.Fatal(err)
	}
	pickerMsgID := 5001
	cbData, _ := findButton(sender.lastSentKeyboard(), "Новый сервис")
	if err := h.HandleCallback(nil, refCallback(cbData, aliceUser, -100777, pickerMsgID)); err != nil {
		t.Fatal(err)
	}
	draft := "<b>XSS</b>\nhttps://z.ai/<script>"
	if err := h.HandleRegistrationInput(nil, refReply(draft, aliceUser, pickerMsgID)); err != nil {
		t.Fatal(err)
	}
	if got := sender.lastEditText(); !strings.Contains(got, "Рефка #1") {
		t.Fatalf("expected success, got %q", got)
	}
	sender.Sent = nil
	if err := h.HandleList(nil, refMsg("/refs", aliceUser)); err != nil {
		t.Fatal(err)
	}
	body := sender.lastSentText()
	if strings.Contains(body, "<script>") || strings.Contains(body, "<b>XSS</b>") {
		t.Fatalf("HTML injection leaked into /refs: %q", body)
	}
	if !strings.Contains(body, "&lt;b&gt;XSS&lt;/b&gt;") {
		t.Fatalf("service name not escaped: %q", body)
	}
}

// ---------------------------------------------------------------------------
// Listing chunking: split below 4096 chars between entries
// ---------------------------------------------------------------------------

func TestReferralListChunking(t *testing.T) {
	_, _, repo, cleanup := newRefTestHandler(t, nil)
	defer cleanup()

	absChat := int64(100777)
	// Seed many referrals under one service to force overflow.
	svc, _, err := repo.Create(context.Background(), absChat,
		referral.Service{Name: "Svc"},
		referral.Referral{OwnerUserID: 1, OwnerDisplay: "owner1", URL: "https://z.ai/owner1"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 2; i <= 120; i++ {
		owner := "owner" + strconv.Itoa(i)
		_, _, err := repo.Create(context.Background(), absChat,
			referral.Service{ID: svc.ID, Name: "Svc"},
			referral.Referral{OwnerUserID: int64(i), OwnerDisplay: owner, URL: "https://z.ai/" + owner})
		if err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}
	groups, err := repo.List(context.Background(), absChat)
	if err != nil {
		t.Fatal(err)
	}
	chunks := renderReferralList(groups)
	if len(chunks) < 2 {
		t.Fatalf("expected chunking for large catalog, got %d chunk(s)", len(chunks))
	}
	for i, c := range chunks {
		if len(c) > 4096 {
			t.Errorf("chunk %d exceeds 4096 chars: %d", i, len(c))
		}
		// Every chunk repeats the service heading so no entry loses
		// category context.
		if !strings.Contains(c, "<b>Svc</b>") {
			t.Errorf("chunk %d missing service heading: %q", i, c)
		}
	}
}

// ---------------------------------------------------------------------------
// Picker pagination: ←/→ buttons appear when services overflow one page
// ---------------------------------------------------------------------------

func TestReferralPickerPagination(t *testing.T) {
	h, sender, repo, cleanup := newRefTestHandler(t, nil)
	defer cleanup()

	// Seed referralPageSize+1 services to force two pages.
	absChat := int64(100777)
	svc, _, err := repo.Create(context.Background(), absChat,
		referral.Service{Name: "Svc0"},
		referral.Referral{OwnerUserID: 1, OwnerDisplay: "o", URL: "https://z.ai/0"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < referralPageSize+1; i++ {
		_, _, err := repo.Create(context.Background(), absChat,
			referral.Service{Name: "Svc" + strconv.Itoa(i)},
			referral.Referral{OwnerUserID: int64(i + 1), OwnerDisplay: "o", URL: "https://z.ai/" + strconv.Itoa(i)})
		if err != nil {
			t.Fatal(err)
		}
	}
	_ = svc

	if err := h.HandleRegister(nil, refMsg("/refreg", aliceUser)); err != nil {
		t.Fatal(err)
	}
	kb := sender.lastSentKeyboard()
	if _, ok := findButton(kb, "→"); !ok {
		t.Fatalf("missing forward nav button on page 0: %+v", kb)
	}
	if _, ok := findButton(kb, "←"); ok {
		t.Fatalf("unexpected back nav on page 0: %+v", kb)
	}
	nextCB, _ := findButton(kb, "→")
	pickerMsgID := 5001
	if err := h.HandleCallback(nil, refCallback(nextCB, aliceUser, -100777, pickerMsgID)); err != nil {
		t.Fatal(err)
	}
	// After paging forward, the last edit's keyboard should now show ←
	// (and possibly → if more than two pages).
	lastEdit := sender.Edited[len(sender.Edited)-1]
	if _, ok := findButton(lastEdit.ReplyMarkup, "←"); !ok {
		t.Fatalf("missing back nav on page 1: %+v", lastEdit.ReplyMarkup)
	}
}

// ---------------------------------------------------------------------------
// Fuzzy match prompt: typo-y name offers candidates and "new service"
// ---------------------------------------------------------------------------

func TestReferralFuzzyMatchPromptsChoice(t *testing.T) {
	h, sender, repo, cleanup := newRefTestHandler(t, nil)
	defer cleanup()

	absChat := int64(100777)
	existing, _, err := repo.Create(context.Background(), absChat,
		referral.Service{Name: "ZAI Coding Plan", Effect: "+5"},
		referral.Referral{OwnerUserID: 9, OwnerDisplay: "zoe", URL: "https://z.ai/zoe"})
	if err != nil {
		t.Fatal(err)
	}
	_ = existing

	if err := h.HandleRegister(nil, refMsg("/refreg", aliceUser)); err != nil {
		t.Fatal(err)
	}
	pickerMsgID := 5001
	cbData, _ := findButton(sender.lastSentKeyboard(), "Новый сервис")
	if err := h.HandleCallback(nil, refCallback(cbData, aliceUser, -100777, pickerMsgID)); err != nil {
		t.Fatal(err)
	}
	// Typo: "ZAI Codng Plan" should fuzzy-match but not exact-match.
	draft := "ZAI Codng Plan\nhttps://z.ai/alice"
	if err := h.HandleRegistrationInput(nil, refReply(draft, aliceUser, pickerMsgID)); err != nil {
		t.Fatal(err)
	}
	lastEdit := sender.Edited[len(sender.Edited)-1]
	if !strings.Contains(lastEdit.Text, "Это уже один из этих сервисов?") {
		t.Fatalf("expected fuzzy choice prompt, got %q", lastEdit.Text)
	}
	markup := lastEdit.ReplyMarkup
	if _, ok := findButton(markup, "Нет, это новый сервис"); !ok {
		t.Fatalf("fuzzy prompt missing 'new service' escape: %+v", markup)
	}
}

// ---------------------------------------------------------------------------
// Exact-effect conflict prompt: same name, different effect → no escape
// ---------------------------------------------------------------------------

func TestReferralExactEffectConflictPromptsExistingOrCancel(t *testing.T) {
	h, sender, repo, cleanup := newRefTestHandler(t, nil)
	defer cleanup()

	absChat := int64(100777)
	_, _, err := repo.Create(context.Background(), absChat,
		referral.Service{Name: "ZAI Coding Plan", Effect: "+5"},
		referral.Referral{OwnerUserID: 9, OwnerDisplay: "zoe", URL: "https://z.ai/zoe"})
	if err != nil {
		t.Fatal(err)
	}

	if err := h.HandleRegister(nil, refMsg("/refreg", aliceUser)); err != nil {
		t.Fatal(err)
	}
	pickerMsgID := 5001
	cbData, _ := findButton(sender.lastSentKeyboard(), "Новый сервис")
	if err := h.HandleCallback(nil, refCallback(cbData, aliceUser, -100777, pickerMsgID)); err != nil {
		t.Fatal(err)
	}
	// Same name, DIFFERENT non-empty effect → exact-effect conflict.
	draft := "ZAI Coding Plan\n+10 баксов\nhttps://z.ai/alice"
	if err := h.HandleRegistrationInput(nil, refReply(draft, aliceUser, pickerMsgID)); err != nil {
		t.Fatal(err)
	}
	lastEdit := sender.Edited[len(sender.Edited)-1]
	if !strings.Contains(lastEdit.Text, "такой сервис уже есть") {
		t.Fatalf("expected exact-effect conflict prompt, got %q", lastEdit.Text)
	}
	markup := lastEdit.ReplyMarkup
	if _, ok := findButton(markup, "Нет, это новый сервис"); ok {
		t.Fatalf("exact-effect conflict must NOT offer 'new service' escape: %+v", markup)
	}
	if _, ok := findButton(markup, "Отмена"); !ok {
		t.Fatalf("exact-effect conflict must offer cancel: %+v", markup)
	}
}

// ---------------------------------------------------------------------------
// Expiry: an expired interaction answers as stale
// ---------------------------------------------------------------------------

func TestReferralCallbackExpiredStale(t *testing.T) {
	h, sender, _, cleanup := newRefTestHandler(t, nil)
	defer cleanup()

	if err := h.HandleRegister(nil, refMsg("/refreg", aliceUser)); err != nil {
		t.Fatal(err)
	}
	// Force-expire every live interaction.
	h.mu.Lock()
	for _, it := range h.byToken {
		it.expiresAt = time.Now().Add(-time.Minute)
	}
	h.mu.Unlock()

	pickerMsgID := 5001
	cbData, _ := findButton(sender.lastSentKeyboard(), "Новый сервис")
	if err := h.HandleCallback(nil, refCallback(cbData, aliceUser, -100777, pickerMsgID)); err != nil {
		t.Fatal(err)
	}
	if len(sender.Answered) != 1 {
		t.Fatalf("expected 1 callback answer, got %d", len(sender.Answered))
	}
	if !strings.Contains(sender.Answered[0].Text, "устала") {
		t.Fatalf("expired alert copy, got %q", sender.Answered[0].Text)
	}
}

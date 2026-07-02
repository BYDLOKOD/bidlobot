package bot

import (
	"context"
	"fmt"
	"html"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"

	"github.com/veschin/bidlobot/internal/domain/cleanup"
	"github.com/veschin/bidlobot/internal/domain/dmsession"
	"github.com/veschin/bidlobot/internal/domain/gracekick"
	"github.com/veschin/bidlobot/internal/domain/membership"
	"github.com/veschin/bidlobot/internal/domain/moderation"
	"github.com/veschin/bidlobot/internal/domain/monthstats"
	"github.com/veschin/bidlobot/internal/domain/pending"
	"github.com/veschin/bidlobot/internal/domain/stats"
	"github.com/veschin/bidlobot/internal/histimport"
	"github.com/veschin/bidlobot/internal/storage"
)

// DM console: the only private control surface for the bot. An admin
// opens a private chat with the bot, picks a managed chat once, then
// issues moderation against it. Nothing the admin types and nothing the
// bot replies is ever visible to the 200 members of the target chat.
//
// Inline mode cannot do this: a chosen inline result is posted as a
// public message into the originating chat (see memory:
// telegram-api-constraints). DM is the only surface where the control
// exchange stays off the public timeline.

const dmCBNamespace = "dm:" // callback_data namespace, distinct from public "v1:"

// dmSender is the narrow slice of telego.Bot the console needs. An
// interface so tests can record sends/edits without a live bot;
// *telego.Bot satisfies it directly in production.
type dmSender interface {
	SendMessage(ctx context.Context, p *telego.SendMessageParams) (*telego.Message, error)
	EditMessageText(ctx context.Context, p *telego.EditMessageTextParams) (*telego.Message, error)
	AnswerCallbackQuery(ctx context.Context, p *telego.AnswerCallbackQueryParams) error
}

// dmConsoleDeps is everything the console needs. The concrete services
// are the same ones the public handlers used; only the surface changes.
type DMConsole struct {
	bot      dmSender
	sessions dmsession.Store
	members  membership.Store
	admin    AdminChecker
	mod      *moderation.Service
	cleanup  *cleanup.Service
	gracekik *gracekick.Service // nil-tolerant: nil -> /cleanup says unavailable
	stats    *stats.Service
	month    *monthstats.Service
	pending  pending.Store
	log      *slog.Logger

	appCtxV context.Context
	wgV     *sync.WaitGroup

	// History-import wiring. All nil-tolerant: a minimal/test app
	// without them serves /import with "недоступно" and ignores a stray
	// uploaded document with the no-context hint. memberRepo/monthRepo
	// are the CONCRETE repos histimport.Ingest needs (UpsertChat +
	// GetState/ApplyImport); the existing `members` field is unchanged.
	importState    dmsession.ImportStateStore
	imports        *importRuns
	files          fileFetcher
	memberRepo     histimport.MembershipStore
	monthRepo      histimport.MonthlyStore
	importStageDir string
}

// SetAppContext binds running cleanup workers to the app lifecycle so a
// SIGTERM aborts them instead of orphaning Telegram API calls.
func (d *DMConsole) SetAppContext(ctx context.Context) { d.appCtxV = ctx }

// AttachWaitGroup lets App.Stop() wait for an in-flight DM cleanup.
func (d *DMConsole) AttachWaitGroup(wg *sync.WaitGroup) { d.wgV = wg }

// SetGraceKick wires the inactive-cleanup campaign engine. Nil-tolerant:
// without it `/cleanup` reports the feature unavailable.
func (d *DMConsole) SetGraceKick(g *gracekick.Service) { d.gracekik = g }

func (d *DMConsole) appCtx() context.Context {
	if d.appCtxV != nil {
		return d.appCtxV
	}
	return context.Background()
}

func (d *DMConsole) wg() *sync.WaitGroup {
	if d.wgV != nil {
		return d.wgV
	}
	return &sync.WaitGroup{}
}

func NewDMConsole(
	bot dmSender,
	sessions dmsession.Store,
	members membership.Store,
	admin AdminChecker,
	mod *moderation.Service,
	clean *cleanup.Service,
	st *stats.Service,
	month *monthstats.Service,
	pendingStore pending.Store,
	importState dmsession.ImportStateStore,
	imports *importRuns,
	files fileFetcher,
	memberRepo histimport.MembershipStore,
	monthRepo histimport.MonthlyStore,
	log *slog.Logger,
) *DMConsole {
	return &DMConsole{
		bot:         bot,
		sessions:    sessions,
		members:     members,
		admin:       admin,
		mod:         mod,
		cleanup:     clean,
		stats:       st,
		month:       month,
		pending:     pendingStore,
		importState: importState,
		imports:     imports,
		files:       files,
		memberRepo:  memberRepo,
		monthRepo:   monthRepo,
		log:         log,
	}
}

type managedChat struct {
	AbsChatID int64
	Title     string
}

// dmSignedChat converts the stored absolute id back to the signed form
// telego's API calls expect. Supergroups are -absID. (The public
// callback path has its own signedChatID(query) for a different input.)
func dmSignedChat(absChatID int64) int64 { return -absChatID }

func (d *DMConsole) send(ctx context.Context, userID int64, htmlBody string, kb *telego.InlineKeyboardMarkup) {
	p := &telego.SendMessageParams{
		ChatID:    telego.ChatID{ID: userID},
		Text:      htmlBody,
		ParseMode: telego.ModeHTML,
	}
	if kb != nil {
		p.ReplyMarkup = kb
	}
	if _, err := d.bot.SendMessage(ctx, p); err != nil {
		d.log.Warn("dm send failed", "error", err, "user_id", userID)
	}
}

// HandleMessage is the DM message entry point. It is wired only under
// the private-chat predicate, so msg.Chat.ID == msg.From.ID always.
func (d *DMConsole) HandleMessage(thctx *th.Context, msg telego.Message) error {
	if msg.From == nil || msg.From.IsBot {
		return nil
	}
	ctx := thctx.Context()
	caller := msg.From.ID
	// A document upload is the history-export delivery for a prior
	// /import. Handle it before the text-command parse: an export file
	// carries no text, so the fields==0 early-return would otherwise
	// swallow it silently.
	if msg.Document != nil {
		return d.handleImportDocument(thctx, msg)
	}
	fields := strings.Fields(msg.Text)
	if len(fields) == 0 {
		return nil
	}
	cmd := strings.ToLower(strings.TrimPrefix(strings.SplitN(fields[0], "@", 2)[0], "/"))

	switch cmd {
	case "start", "help":
		return d.handleStart(ctx, caller)
	case "chat":
		return d.showChatPicker(ctx, caller, true)
	case "stats":
		return d.handleStats(ctx, caller, fields[1:])
	case "warns":
		return d.handleWarns(ctx, caller, fields[1:])
	case "warn", "mute", "unmute", "ban", "unban", "cleanup":
		return d.handleModeration(ctx, caller, cmd, fields[1:])
	case "import":
		return d.handleImportStart(ctx, caller)
	default:
		d.send(ctx, caller, msgDMUnknown, nil)
		return nil
	}
}

// resolveManagedChats returns the chats where the bot can moderate AND
// the caller is an admin. This is re-evaluated on every /start and
// every command-time session check, so a demoted admin loses access
// immediately.
func (d *DMConsole) resolveManagedChats(ctx context.Context, caller int64) ([]managedChat, error) {
	chats, err := d.members.ListChats(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]managedChat, 0, len(chats))
	for _, c := range chats {
		if c.BotStatus != membership.StatusAdministrator && c.BotStatus != membership.StatusCreator {
			continue
		}
		if !c.CanRestrict {
			continue
		}
		ok, aerr := d.admin.IsAdmin(c.AbsChatID, caller)
		if aerr != nil || !ok {
			continue
		}
		title := c.Title
		if title == "" {
			title = fmt.Sprintf("chat %d", c.AbsChatID)
		}
		out = append(out, managedChat{AbsChatID: c.AbsChatID, Title: title})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Title < out[j].Title })
	return out, nil
}

func (d *DMConsole) handleStart(ctx context.Context, caller int64) error {
	managed, err := d.resolveManagedChats(ctx, caller)
	if err != nil {
		d.send(ctx, caller, msgDMError, nil)
		return nil
	}
	switch len(managed) {
	case 0:
		d.send(ctx, caller, msgDMNoChats, nil)
		return nil
	case 1:
		if err := d.sessions.Set(ctx, caller, managed[0].AbsChatID, time.Now().UTC()); err != nil {
			d.send(ctx, caller, msgDMError, nil)
			return nil
		}
		d.send(ctx, caller, fmt.Sprintf(msgDMReady, html.EscapeString(managed[0].Title))+dmHelpBody, nil)
		return nil
	default:
		return d.showChatPickerList(ctx, caller, managed)
	}
}

func (d *DMConsole) showChatPicker(ctx context.Context, caller int64, _ bool) error {
	managed, err := d.resolveManagedChats(ctx, caller)
	if err != nil {
		d.send(ctx, caller, msgDMError, nil)
		return nil
	}
	if len(managed) == 0 {
		d.send(ctx, caller, msgDMNoChats, nil)
		return nil
	}
	return d.showChatPickerList(ctx, caller, managed)
}

func (d *DMConsole) showChatPickerList(ctx context.Context, caller int64, managed []managedChat) error {
	rows := make([][]telego.InlineKeyboardButton, 0, len(managed))
	for _, m := range managed {
		rows = append(rows, []telego.InlineKeyboardButton{{
			Text:         m.Title,
			CallbackData: dmCBNamespace + "pick:" + strconv.FormatInt(m.AbsChatID, 10),
		}})
	}
	d.send(ctx, caller, msgDMPick, &telego.InlineKeyboardMarkup{InlineKeyboard: rows})
	return nil
}

// requireSession loads the session and re-verifies the caller is still
// an admin of the selected chat. Returns absChatID + signed id, or
// nudges the caller and returns ok=false.
func (d *DMConsole) requireSession(ctx context.Context, caller int64) (abs int64, signed int64, ok bool) {
	s, err := d.sessions.Get(ctx, caller)
	if err != nil {
		d.send(ctx, caller, msgDMNoSession, nil)
		return 0, 0, false
	}
	isAdmin, aerr := d.admin.IsAdmin(s.AbsChatID, caller)
	if aerr != nil || !isAdmin {
		_ = d.sessions.Clear(ctx, caller)
		d.send(ctx, caller, msgDMLostAdmin, nil)
		return 0, 0, false
	}
	return s.AbsChatID, dmSignedChat(s.AbsChatID), true
}

// resolveTarget parses "@username" or a numeric id from DM args (there
// is no reply-to in a DM). Returns the user id and isBot flag.
func (d *DMConsole) resolveTarget(ctx context.Context, absChatID int64, arg string) (userID int64, display string, isBot bool, ok bool) {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return 0, "", false, false
	}
	if name, isAt := strings.CutPrefix(arg, "@"); isAt {
		m, err := d.members.GetMemberByUsername(ctx, absChatID, name)
		if err != nil || m == nil || m.UserID == 0 {
			return 0, "", false, false
		}
		return m.UserID, memberDisplay(m.Username, m.FirstName), m.IsBot, true
	}
	if uid, err := strconv.ParseInt(arg, 10, 64); err == nil && uid > 0 {
		display = arg
		if m, merr := d.members.GetMember(ctx, uid, absChatID); merr == nil && m != nil {
			display = memberDisplay(m.Username, m.FirstName)
			isBot = m.IsBot
		}
		return uid, display, isBot, true
	}
	return 0, "", false, false
}

// memberDisplay returns a raw (non-HTML-escaped) display name: first name
// if non-empty, otherwise username. Callers must escape before embedding
// in HTML-parsed Telegram messages.
func memberDisplay(username, firstName string) string {
	if firstName != "" {
		return firstName
	}
	return username
}

func (d *DMConsole) handleStats(ctx context.Context, caller int64, args []string) error {
	abs, _, ok := d.requireSession(ctx, caller)
	if !ok {
		return nil
	}
	var body string
	var err error
	switch {
	case len(args) == 0:
		body, err = d.stats.ChatOverview(ctx, abs)
	case args[0] == "top":
		body, err = d.stats.Top(ctx, abs)
	case args[0] == "today":
		body, err = d.stats.Today(ctx, abs)
	case args[0] == "months":
		if d.month == nil {
			d.send(ctx, caller, msgDMError, nil)
			return nil
		}
		body, err = d.month.Months(ctx, abs)
	case args[0] == "month":
		if d.month == nil {
			d.send(ctx, caller, msgDMError, nil)
			return nil
		}
		monthArg := ""
		if len(args) >= 2 {
			monthArg = args[1]
		}
		body, err = d.month.MonthReport(ctx, abs, monthArg)
	default:
		uid, _, _, found := d.resolveTarget(ctx, abs, args[0])
		if !found {
			d.send(ctx, caller, msgDMTargetNotFound, nil)
			return nil
		}
		body, err = d.stats.UserStats(ctx, abs, uid, "")
	}
	if err != nil {
		d.send(ctx, caller, msgDMError, nil)
		return nil
	}
	d.send(ctx, caller, body, nil)
	return nil
}

func (d *DMConsole) handleWarns(ctx context.Context, caller int64, args []string) error {
	abs, _, ok := d.requireSession(ctx, caller)
	if !ok {
		return nil
	}
	if len(args) >= 2 && args[0] == "clear" {
		uid, disp, _, found := d.resolveTarget(ctx, abs, args[1])
		if !found {
			d.send(ctx, caller, msgDMTargetNotFound, nil)
			return nil
		}
		if err := d.mod.ClearWarnings(ctx, uid, abs); err != nil {
			d.send(ctx, caller, msgDMError, nil)
			return nil
		}
		d.send(ctx, caller, fmt.Sprintf(msgDMWarnsCleared, html.EscapeString(disp)), nil)
		return nil
	}
	if len(args) == 0 {
		d.send(ctx, caller, msgDMNeedTarget, nil)
		return nil
	}
	uid, _, _, found := d.resolveTarget(ctx, abs, args[0])
	if !found {
		d.send(ctx, caller, msgDMTargetNotFound, nil)
		return nil
	}
	list, err := d.mod.ListWarnings(ctx, uid, abs)
	if err != nil {
		d.send(ctx, caller, msgDMError, nil)
		return nil
	}
	d.send(ctx, caller, list, nil)
	return nil
}

func (d *DMConsole) handleModeration(ctx context.Context, caller int64, cmd string, args []string) error {
	abs, signed, ok := d.requireSession(ctx, caller)
	if !ok {
		return nil
	}

	if cmd == "cleanup" {
		return d.handleCleanup(ctx, caller, abs, args)
	}

	if len(args) == 0 {
		d.send(ctx, caller, msgDMNeedTarget, nil)
		return nil
	}
	uid, disp, isBot, found := d.resolveTarget(ctx, abs, args[0])
	if !found {
		d.send(ctx, caller, msgDMTargetNotFound, nil)
		return nil
	}
	rest := strings.TrimSpace(strings.Join(args[1:], " "))

	switch cmd {
	case "warn":
		if err := d.mod.ValidateTarget(ctx, abs, caller, uid, isBot, "warn"); err != nil {
			d.send(ctx, caller, escErr(err), nil)
			return nil
		}
		count, err := d.mod.Warn(ctx, abs, uid, caller, rest)
		if err != nil {
			d.send(ctx, caller, msgDMError, nil)
			return nil
		}
		out := fmt.Sprintf(msgDMWarned, html.EscapeString(disp), count)
		if rest != "" {
			out += "\n" + fmt.Sprintf(msgDMReasonLine, html.EscapeString(rest))
		}
		if count >= 3 {
			if err := d.mod.AutoMute(ctx, signed, uid); err != nil {
				out += "\n" + msgDMAutomuteFailed
			} else {
				out += "\n" + msgDMAutomuteOn
			}
		}
		d.send(ctx, caller, out, nil)
		return nil
	case "mute":
		dur := time.Hour
		if rest != "" {
			parsed, perr := parseModDuration(strings.Fields(rest)[0])
			if perr != nil {
				d.send(ctx, caller, msgDMBadDuration, nil)
				return nil
			}
			dur = parsed
		}
		if err := d.mod.ValidateTarget(ctx, abs, caller, uid, isBot, "mute"); err != nil {
			d.send(ctx, caller, escErr(err), nil)
			return nil
		}
		if err := d.mod.Mute(ctx, signed, uid, dur); err != nil {
			d.send(ctx, caller, msgDMPermOrTransient, nil)
			return nil
		}
		d.send(ctx, caller, fmt.Sprintf(msgDMMuted, html.EscapeString(disp), dur.String()), nil)
		return nil
	case "unmute":
		if err := d.mod.Unmute(ctx, signed, uid); err != nil {
			d.send(ctx, caller, msgDMError, nil)
			return nil
		}
		d.send(ctx, caller, fmt.Sprintf(msgDMUnmuted, html.EscapeString(disp)), nil)
		return nil
	case "unban":
		if err := d.mod.Unban(ctx, signed, uid); err != nil {
			d.send(ctx, caller, escErr(err), nil)
			return nil
		}
		d.send(ctx, caller, fmt.Sprintf(msgDMUnbanned, html.EscapeString(disp)), nil)
		return nil
	case "ban":
		if err := d.mod.ValidateTarget(ctx, abs, caller, uid, isBot, "ban"); err != nil {
			d.send(ctx, caller, escErr(err), nil)
			return nil
		}
		return d.confirmBan(ctx, caller, abs, uid, disp, rest)
	}
	return nil
}

// confirmBan stages a ban as a pending action and asks for an explicit
// in-DM confirmation. Ban is irreversible from the member's side, so it
// gets a confirm step; warn/mute/unmute/unban are reversible and run
// straight through.
func (d *DMConsole) confirmBan(ctx context.Context, caller, abs, target int64, disp, reason string) error {
	id, err := storage.NewID()
	if err != nil {
		d.send(ctx, caller, msgDMError, nil)
		return nil
	}
	now := time.Now().UTC()
	act := pending.Action{
		ID:            id,
		Kind:          pending.KindBan,
		AbsChatID:     abs,
		ActorUserID:   caller,
		TargetUserID:  target,
		TargetDisplay: disp,
		Reason:        reason,
		CreatedAt:     now,
		ExpiresAt:     now.Add(5 * time.Minute),
	}
	if err := d.pending.Create(ctx, act); err != nil {
		d.send(ctx, caller, msgDMError, nil)
		return nil
	}
	body := fmt.Sprintf(msgDMConfirmBan, html.EscapeString(disp))
	if reason != "" {
		body += "\n" + fmt.Sprintf(msgDMReasonLine, html.EscapeString(reason))
	}
	d.send(ctx, caller, body, dmConfirmKeyboard(id))
	return nil
}

func dmConfirmKeyboard(id string) *telego.InlineKeyboardMarkup {
	return &telego.InlineKeyboardMarkup{InlineKeyboard: [][]telego.InlineKeyboardButton{{
		{Text: "✅ Подтвердить", CallbackData: dmCBNamespace + "apply:" + id},
		{Text: "✕ Отмена", CallbackData: dmCBNamespace + "cancel:" + id},
	}}}
}

func escErr(err error) string { return html.EscapeString(err.Error()) }

// parseModDuration accepts 30m,1h,12h and Nd (days). Mirrors the public
// handler's accepted range so behavior is identical across surfaces.
func parseModDuration(s string) (time.Duration, error) {
	if d, err := time.ParseDuration(s); err == nil {
		if d < time.Minute || d > 366*24*time.Hour {
			return 0, fmt.Errorf("out of range")
		}
		return d, nil
	}
	if num, ok := strings.CutSuffix(s, "d"); ok {
		n, err := strconv.Atoi(num)
		if err != nil {
			return 0, fmt.Errorf("bad days")
		}
		dd := time.Duration(n) * 24 * time.Hour
		if dd < time.Minute || dd > 366*24*time.Hour {
			return 0, fmt.Errorf("out of range")
		}
		return dd, nil
	}
	return 0, fmt.Errorf("unparseable")
}

package bot

// Reaction-based reputation.
//
// A 👍 (like) reaction on a supergroup message gives +rep to the message
// author; 👎 (dislike) or 🤡 (clown_face) gives -rep. Several reactions
// landing within repBatchWindow are flushed as ONE combined chat message
// in the classic /praise /roast style, each entry carrying the updated
// balances (no per-reaction spam, but the balance change is always
// visible). Removing a reaction does NOT undo the rep (no reversal).
//
// Each entry also says WHAT was liked and leads to it: a short quote of the
// reacted message, and that quote is the t.me permalink of the message (tap
// it -> jump to the message). The quote comes from the same bounded
// in-memory index as the author (the Bot API cannot read a message back by
// id), so a restart loses the quotes of older messages exactly like it
// loses their attribution. When no quote is known the target's own name
// carries the link instead, so the entry still leads somewhere.
//
// Author attribution problem: MessageReactionUpdated carries only
// chat_id + message_id + who reacted - never the message author. The bot
// therefore keeps a bounded in-memory index of (chat, messageID) ->
// author, fed by the message stream, so a reaction can be attributed.
// Only messages the bot saw while running are indexed; reactions on
// older (pre-restart) messages are skipped.

import (
	"context"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"

	"github.com/veschin/bidlobot/internal/domain/reputation"
	"github.com/veschin/bidlobot/internal/shared"
)

// repBatchWindow is how long rep events accumulate before one combined
// chat message is sent.
const repBatchWindow = 10 * time.Second

// maxIndexedMessages bounds the in-memory author index: the bot keeps
// only the most recent messages it has seen, so reactions on very old
// messages cannot be attributed.
const maxIndexedMessages = 5000

// repQuoteMaxRunes bounds the quoted text of one reacted message. Long
// enough to recognise a message, short enough that a batch of a dozen
// reactions still fits in one chat message.
const repQuoteMaxRunes = 120

// repMessageMaxChars is the compose budget for one combined rep message.
// Telegram rejects anything past 4096 chars, and one batch can hold a
// whole burst of reactions, so the batcher splits instead of truncating.
// The headroom covers the markup and the ellipsis of the last entry.
const repMessageMaxChars = 3800

// Tracked reaction emojis. :like: = 👍, :dislike: = 👎, :clown_face: = 🤡.
const (
	emojiLike    = "👍"
	emojiDislike = "👎"
	emojiClown   = "🤡"
)

// msgKey addresses one message inside a chat.
type msgKey struct {
	chatID int64
	msgID  int
}

// msgAuthor is what the index remembers about a message's author.
type msgAuthor struct {
	userID    int64
	username  string
	firstName string
	isBot     bool
	// quote is the cleaned, render-ready quote of the message text (see
	// repQuoteText): already whitespace-collapsed and length-bounded,
	// still unescaped - the composer escapes it.
	quote string
	// threadID is the message's forum topic, for the permalink.
	threadID int
}

// msgAuthorIndex is a bounded in-memory (chat, messageID) -> author map
// with FIFO eviction. Safe for concurrent use.
type msgAuthorIndex struct {
	mu   sync.RWMutex
	byID map[msgKey]msgAuthor
	// fifo is the insertion order for eviction (append, pop front).
	fifo []msgKey
	max  int
}

func newMsgAuthorIndex(max int) *msgAuthorIndex {
	return &msgAuthorIndex{
		byID: make(map[msgKey]msgAuthor),
		max:  max,
	}
}

// Record stores the author of a message. Re-recording an existing key
// refreshes it without growing the FIFO.
func (x *msgAuthorIndex) Record(key msgKey, author msgAuthor) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if _, ok := x.byID[key]; !ok {
		x.fifo = append(x.fifo, key)
	}
	x.byID[key] = author
	for len(x.fifo) > x.max {
		old := x.fifo[0]
		x.fifo = x.fifo[1:]
		delete(x.byID, old)
	}
}

// Lookup returns the recorded author. ok=false when the message was not
// seen (pre-restart or evicted).
func (x *msgAuthorIndex) Lookup(key msgKey) (msgAuthor, bool) {
	x.mu.RLock()
	defer x.mu.RUnlock()
	a, ok := x.byID[key]
	return a, ok
}

// ownerRecorder indexes a BOT-sent message under the human whose content
// it reposts, so reactions on the bot's message credit that human.
// *repReactor implements it; a nil *repReactor is a valid no-op value.
type ownerRecorder interface {
	RecordOwner(rec ownerRecord)
}

// ownerRecord is one BOT-sent message that must be credited to a human.
// The media reposters, comment quotes and X renders send messages the bot
// never receives back as updates, so the stream-fed index cannot learn
// them; the caller describes the message instead.
//
// ThreadID must be the topic the message was actually sent into, which is
// what the send call returned - not the topic of the message that
// triggered the repost. Several senders pass no message_thread_id, so
// their messages land in the General topic whatever the source topic was,
// and a link built from the source topic would not resolve.
//
// Text is the plain text the bot posted for it: the Bot API returns
// parsed text plus entities, so an HTML caption arrives here already
// stripped of its markup. It becomes the rep quote. Empty Text is
// allowed: the rep entry then links to the message without quoting it
// (a bare reposted video has no caption at all).
type ownerRecord struct {
	ChatID   int64
	MsgID    int
	ThreadID int
	Owner    *telego.User
	Text     string
}

// sentMessage is what owner attribution needs from a message the bot just
// sent: the id the chat sees and the forum topic Telegram actually put it
// in (0 = no topic). Read from the send result on purpose - see
// ownerRecord.ThreadID.
type sentMessage struct {
	ID       int
	ThreadID int
}

// sentMessageOf describes one sent message. A nil message (a caller that
// only knows the id) yields the zero value and records no topic.
func sentMessageOf(m *telego.Message) sentMessage {
	if m == nil {
		return sentMessage{}
	}
	return sentMessage{ID: m.GetMessageID(), ThreadID: m.MessageThreadID}
}

// sentMessagesOf describes an album send result.
func sentMessagesOf(ms []telego.Message) []sentMessage {
	out := make([]sentMessage, 0, len(ms))
	for i := range ms {
		out = append(out, sentMessageOf(&ms[i]))
	}
	return out
}

// RecordOwner feeds the author index for a message the bot itself sent
// (video reposts, comment quotes, X renders). The bot never receives its
// own outgoing messages as updates, so the stream-fed index cannot learn
// them; reactions on such messages would otherwise die on the lookup
// miss. isBot stays false on purpose: the recorded author is the human
// owner, and the existing isBot guard then protects nothing here.
// nil-receiver safe: feature off -> no-op.
func (r *repReactor) RecordOwner(rec ownerRecord) {
	if r == nil || rec.Owner == nil || rec.Owner.IsBot || rec.MsgID <= 0 {
		return
	}
	r.index.Record(msgKey{chatID: rec.ChatID, msgID: rec.MsgID}, msgAuthor{
		userID:    rec.Owner.ID,
		username:  rec.Owner.Username,
		firstName: rec.Owner.FirstName,
		isBot:     false,
		quote:     repQuoteText(rec.Text),
		threadID:  rec.ThreadID,
	})
}

// repReactor is the reaction->rep pipeline: author lookup, emoji mapping,
// one atomic settlement, and the 10-second batcher that emits one
// combined message per chat.
type repReactor struct {
	store reputation.Store
	index *msgAuthorIndex
	batch *repBatcher
	log   *slog.Logger
}

// NewRepReactor wires the reaction-rep pipeline. sender is the
// rate-limited public-surface sender used for the combined rep messages.
// The AdminChecker argument is kept for wiring stability; administrators
// receive the same deltas as everyone else.
func NewRepReactor(store reputation.Store, _ AdminChecker, sender repSender, log *slog.Logger) *repReactor {
	if log == nil {
		log = slog.Default()
	}
	return &repReactor{
		store: store,
		index: newMsgAuthorIndex(maxIndexedMessages),
		batch: newRepBatcher(sender, log),
		log:   log,
	}
}

// repSender is the narrow Telegram surface the batcher needs.
type repSender interface {
	SendMessage(context.Context, *telego.SendMessageParams) (*telego.Message, error)
}

// RecordMessage feeds the author index from the supergroup message
// stream. Runs among the passive observers, before the reposters, so the
// original human message is always indexed.
func (r *repReactor) RecordMessage(msg *telego.Message) {
	if msg == nil || msg.From == nil {
		return
	}
	r.index.Record(msgKey{chatID: msg.Chat.ID, msgID: msg.GetMessageID()}, msgAuthor{
		userID:    msg.From.ID,
		username:  msg.From.Username,
		firstName: msg.From.FirstName,
		isBot:     msg.From.IsBot,
		quote:     repQuoteFor(msg),
		threadID:  msg.MessageThreadID,
	})
}

// Handle processes one message_reaction update. It is wired into
// reactionFanout, after the battle observer and before membership
// tracking. Never fails: rep is best-effort and a reaction must never
// error the sequential update loop.
func (r *repReactor) Handle(_ *th.Context, reaction telego.MessageReactionUpdated) error {
	// Anonymous (User == nil) and bot reactions carry no rep actor.
	if reaction.User == nil || reaction.User.IsBot {
		return nil
	}
	// NewReaction empty = the user removed their reaction. No reversal.
	if len(reaction.NewReaction) == 0 {
		r.log.Info("rep reaction: removal ignored",
			"chat_id", reaction.Chat.ID, "message_id", reaction.MessageID, "user_id", reaction.User.ID)
		return nil
	}

	kind, tracked := repEmojiToKind(reaction.NewReaction)
	r.log.Info("rep reaction update",
		"chat_id", reaction.Chat.ID,
		"message_id", reaction.MessageID,
		"user_id", reaction.User.ID,
		"new", reactionEmojis(reaction.NewReaction),
		"old", reactionEmojis(reaction.OldReaction),
		"kind", kindName(kind),
		"tracked", tracked,
	)
	if !tracked {
		return nil // custom (premium) emoji or untracked reaction
	}

	author, ok := r.index.Lookup(msgKey{chatID: reaction.Chat.ID, msgID: reaction.MessageID})
	if !ok {
		r.log.Info("rep reaction: author not indexed",
			"chat_id", reaction.Chat.ID, "message_id", reaction.MessageID, "user_id", reaction.User.ID)
		return nil // message not seen while running
	}
	if author.isBot {
		return nil // never give rep for a bot's message
	}
	if author.userID == reaction.User.ID {
		return nil // self-reaction
	}

	if !r.isStillMember(reaction.Chat.ID, reaction.User.ID) {
		return nil
	}
	if !r.isStillMember(reaction.Chat.ID, author.userID) {
		r.log.Info("rep reaction: author is not a current member",
			"chat_id", reaction.Chat.ID, "message_id", reaction.MessageID, "user_id", author.userID)
		return nil
	}

	res, err := r.store.Settle(context.Background(), reputation.SocialRequest{
		ChatID:          reaction.Chat.ID,
		ActorID:         reaction.User.ID,
		TargetID:        author.userID,
		Kind:            kind,
		Source:          reputation.SourceReaction,
		SourceMessageID: reaction.MessageID,
	})
	if err != nil {
		switch {
		case errors.Is(err, reputation.ErrSelfTarget),
			errors.Is(err, reputation.ErrInsufficientBalance),
			errors.Is(err, reputation.ErrPairLimit),
			errors.Is(err, reputation.ErrSenderLimit),
			errors.Is(err, reputation.ErrDuplicateSource):
			// Socially expected: a repeated reaction, a daily limit or
			// an empty wallet. Silent - no chat spam for a reaction.
			return nil
		default:
			r.log.Warn("rep reaction settle failed", "error", err, "chat_id", reaction.Chat.ID)
			return nil
		}
	}

	line := ""
	if kind == reputation.KindPraise {
		line = praiseLine(reaction.Chat.ID, repReasonNone)
	} else {
		line = "Минус по адресу."
	}
	r.batch.Add(reaction.Chat.ID, repEvent{
		kind:              kind,
		actor:             repDisplay(reaction.User.Username, reaction.User.FirstName, reaction.User.ID),
		target:            repDisplay(author.username, author.firstName, author.userID),
		line:              line,
		actorWalletDelta:  res.ActorWalletDelta,
		targetWalletDelta: res.TargetWalletDelta,
		targetScoreDelta:  res.TargetScoreDelta,
		actorAvailable:    res.Actor.Available,
		targetAvailable:   res.Target.Available,
		targetScore:       res.Target.Score,
		quote:             author.quote,
		link:              shared.MessageLink(reaction.Chat.ID, author.threadID, reaction.MessageID),
	})
	return nil
}

// isStillMember verifies current membership before a reaction write.
// The sender is the production Telegram client, which exposes
// GetChatMember; a sender without that method fails closed.
func (r *repReactor) isStillMember(chatID, userID int64) bool {
	verifier, ok := r.batch.sender.(repMemberVerifier)
	if !ok {
		r.log.Warn("rep reaction membership verifier unavailable", "chat_id", chatID, "user_id", userID)
		return false
	}
	cm, err := verifier.GetChatMember(context.Background(), &telego.GetChatMemberParams{
		ChatID: telego.ChatID{ID: chatID},
		UserID: userID,
	})
	if err != nil || cm == nil {
		r.log.Warn("rep reaction membership check failed", "error", err, "chat_id", chatID, "user_id", userID)
		return false
	}
	return cm.MemberIsMember()
}

type repMemberVerifier interface {
	GetChatMember(context.Context, *telego.GetChatMemberParams) (telego.ChatMember, error)
}

// repEmojiToKind maps the first tracked emoji in NewReaction to a
// reputation kind. Returns tracked=false for custom (premium) emoji and
// untracked reactions.
func repEmojiToKind(rs []telego.ReactionType) (kind reputation.Kind, tracked bool) {
	for _, rt := range rs {
		e, ok := rt.(*telego.ReactionTypeEmoji)
		if !ok {
			continue
		}
		switch e.Emoji {
		case emojiLike:
			return reputation.KindPraise, true
		case emojiDislike, emojiClown:
			return reputation.KindRoast, true
		}
	}
	return 0, false
}

// reactionEmojis extracts the plain emoji strings from a reaction list
// (custom/premium reactions are skipped) for diagnostic logging.
func reactionEmojis(rs []telego.ReactionType) []string {
	var out []string
	for _, rt := range rs {
		if e, ok := rt.(*telego.ReactionTypeEmoji); ok {
			out = append(out, e.Emoji)
		}
	}
	return out
}

// kindName renders a reputation kind for logs.
func kindName(k reputation.Kind) string {
	if k == reputation.KindRoast {
		return "roast"
	}
	return "praise"
}

// repDisplay renders an inert, HTML-escaped display name for the rep
// report, falling back to the numeric ID when neither handle nor name
// is known (mirrors the /praise command rendering).
func repDisplay(username, firstName string, userID int64) string {
	d := shared.UserDisplay(username, firstName)
	if d == "" {
		return strconv.FormatInt(userID, 10)
	}
	return html.EscapeString(d)
}

// repEvent is one credited rep change, accumulated for the batcher.
type repEvent struct {
	kind              reputation.Kind
	actor             string // HTML-escaped inert display
	target            string // HTML-escaped inert display
	line              string // curated public line, already final
	actorWalletDelta  int
	targetWalletDelta int
	targetScoreDelta  int
	actorAvailable    int
	targetAvailable   int
	targetScore       int
	// quote is the raw (unescaped) text of the reacted message; the
	// composer escapes and, when link is set, makes it the link text.
	quote string
	// link is the t.me permalink of the reacted message ("" when the
	// chat is no supergroup or the message cannot be addressed).
	link string
}

// repQuoteText cleans one message text into a rep quote: collapses every
// whitespace run (newlines, tabs, the repost caption layout) into single
// spaces, trims, and cuts the result at repQuoteMaxRunes runes with an
// ellipsis. Rune-based on purpose - cutting bytes would split a Cyrillic
// or emoji sequence and emit invalid UTF-8.
// Returns "" for an empty text.
func repQuoteText(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if s == "" {
		return ""
	}
	r := []rune(s)
	if len(r) > repQuoteMaxRunes {
		s = strings.TrimRight(string(r[:repQuoteMaxRunes]), " ") + "…"
	}
	return s
}

// repQuoteFor builds the quote of an incoming message: its text or
// caption, or a short media label when it carries no text at all (a bare
// photo or sticker still gets a rep entry that says what it was).
func repQuoteFor(msg *telego.Message) string {
	if q := repQuoteText(repMessageText(msg)); q != "" {
		return q
	}
	return repMediaLabel(msg)
}

// repMessageText returns whichever of text/caption the message carries.
func repMessageText(msg *telego.Message) string {
	if msg == nil {
		return ""
	}
	if msg.Text != "" {
		return msg.Text
	}
	return msg.Caption
}

// repMediaLabel names a text-less message so its rep entry is not
// anonymous. Deliberately short: it is a caption substitute, not a
// description.
func repMediaLabel(msg *telego.Message) string {
	switch {
	case msg == nil:
		return ""
	case msg.Photo != nil:
		return "фото"
	case msg.Video != nil:
		return "видео"
	case msg.Animation != nil:
		return "гифка"
	case msg.VideoNote != nil:
		return "кружок"
	case msg.Voice != nil:
		return "голосовое"
	case msg.Audio != nil:
		return "аудио"
	case msg.Sticker != nil:
		return "стикер"
	case msg.Dice != nil:
		return "кубик"
	case msg.Poll != nil:
		return "опрос"
	case msg.Location != nil, msg.Venue != nil:
		return "геолокация"
	case msg.Contact != nil:
		return "контакт"
	case msg.Game != nil:
		return "игра"
	case msg.Document != nil:
		return "файл"
	default:
		return ""
	}
}

// escQuote escapes a quote for Telegram's HTML parse mode. Only the three
// characters that can open a tag or an entity are replaced, which is what
// the Bot API requires; "'" and '"' are ordinary text characters in a
// text node, so they stay as they are instead of turning into entities
// the chat would have to render back.
func escQuote(s string) string {
	return quoteEscaper.Replace(s)
}

var quoteEscaper = strings.NewReplacer(
	"&", "&amp;",
	"<", "&lt;",
	">", "&gt;",
)

// repBatcher accumulates rep events per chat and flushes them as one
// combined message (or a few, when the batch outgrows the length budget)
// repBatchWindow after the first event of a batch. The flush runs on a
// timer goroutine so it never blocks the sequential update loop. Errors
// are logged, never surfaced.
type repBatcher struct {
	mu     sync.Mutex
	sender repSender
	log    *slog.Logger
	window time.Duration
	// pending maps chatID -> open batch. nil entry semantics handled by
	// the map presence check.
	pending map[int64]*repBatch
}

type repBatch struct {
	events []repEvent
	timer  *time.Timer
}

func newRepBatcher(sender repSender, log *slog.Logger) *repBatcher {
	return &repBatcher{
		sender:  sender,
		log:     log,
		window:  repBatchWindow,
		pending: make(map[int64]*repBatch),
	}
}

// Add records one rep event and starts the flush window if none is open
// for the chat.
func (b *repBatcher) Add(chatID int64, ev repEvent) {
	b.mu.Lock()
	batch, ok := b.pending[chatID]
	if !ok {
		batch = &repBatch{}
		b.pending[chatID] = batch
		batch.timer = time.AfterFunc(b.window, func() { b.flush(chatID) })
	}
	batch.events = append(batch.events, ev)
	b.mu.Unlock()
}

// flush sends the combined message(s) for a chat and clears the batch.
// Runs on the timer goroutine. A batch longer than one message is split
// on event boundaries; the first send error ends the batch (a failing
// chat will fail the rest too).
func (b *repBatcher) flush(chatID int64) {
	b.mu.Lock()
	batch, ok := b.pending[chatID]
	if !ok {
		b.mu.Unlock()
		return
	}
	delete(b.pending, chatID)
	events := batch.events
	b.mu.Unlock()

	texts := composeRepMessages(events)
	for _, text := range texts {
		if _, err := b.sender.SendMessage(context.Background(), &telego.SendMessageParams{
			ChatID:    telego.ChatID{ID: chatID},
			Text:      text,
			ParseMode: telego.ModeHTML,
			// The quote is a link to the reacted message; a web page
			// preview over it would bury the joke and inflate the
			// message.
			LinkPreviewOptions: &telego.LinkPreviewOptions{IsDisabled: true},
		}); err != nil {
			b.log.Warn("rep batcher send failed", "chat_id", chatID, "error", err)
			return
		}
	}
	b.log.Info("rep batcher flushed", "chat_id", chatID, "events", len(events), "messages", len(texts))
}

// composeRepMessage renders the combined rep report in the classic
// /praise /roast style, one block per credited reaction. actor/target
// must already be HTML-escaped; send the result with ParseMode HTML.
func composeRepMessage(events []repEvent) string {
	var b strings.Builder
	for i, e := range events {
		if i > 0 {
			b.WriteString("\n\n")
		}
		writeRepEntry(&b, e)
	}
	return b.String()
}

// composeRepMessages splits the events of one batch into as many chat
// messages as the length budget needs, on event boundaries: truncating
// would silently drop reactions that already cost the actor balance.
func composeRepMessages(events []repEvent) []string {
	if len(events) == 0 {
		return nil
	}
	var (
		out []string
		cur []repEvent
	)
	flush := func() {
		if len(cur) == 0 {
			return
		}
		out = append(out, composeRepMessage(cur))
		cur = nil
	}
	for _, e := range events {
		// Trial on a copy: compose decides the split, and the real
		// chunk must not be overwritten by a rejected trial.
		trial := make([]repEvent, len(cur)+1)
		copy(trial, cur)
		trial[len(cur)] = e
		if len(cur) > 0 && len(composeRepMessage(trial)) > repMessageMaxChars {
			flush()
			cur = []repEvent{e}
			continue
		}
		cur = trial
	}
	flush()
	return out
}

// writeRepEntry appends one credited reaction: the curated public line,
// the deterministic signed receipt, and the quote of the reacted message
// made the permalink's link text. actor/target must already be
// HTML-escaped; send the result with ParseMode HTML.
func writeRepEntry(b *strings.Builder, e repEvent) {
	// The reacted message is reachable through exactly one link: its
	// quote when there is one, else the target's own name.
	head := e.target
	if e.quote == "" && e.link != "" {
		head = fmt.Sprintf(`<a href="%s">%s</a>`, e.link, e.target)
	}
	line := e.line
	if line == "" {
		if e.kind == reputation.KindRoast {
			line = "Минус по адресу."
		} else {
			line = "Плюс по адресу."
		}
	}

	fmt.Fprintf(b, "%s\n\n", line)
	fmt.Fprintf(b, "<b>%+d</b> к репе %s, <b>%+d</b> к счёту; <b>%+d</b> у %s.\n",
		e.targetWalletDelta, head,
		e.targetScoreDelta,
		e.actorWalletDelta, e.actor,
	)

	if e.quote != "" {
		q := defuseMentions(escQuote(e.quote))
		if e.link != "" {
			b.WriteString(fmt.Sprintf("<a href=\"%s\">\u00ab%s\u00bb</a>\n", e.link, q))
		} else {
			b.WriteString("\u00ab" + q + "\u00bb\n")
		}
	}
	fmt.Fprintf(b, "<i>Доступно: %s \u2014 %d, %s \u2014 %d. Счёт: %s \u2014 %d.</i>",
		e.actor, e.actorAvailable,
		e.target, e.targetAvailable,
		e.target, e.targetScore,
	)
}

// repAuthorIndexMiddleware feeds the rep reactor's author index from the
// supergroup message stream. Runs in the observer chain (membership,
// stats, then this, then the summarize recorder) - before the content
// middlewares, so the original human message is always indexed even when
// a reposter deletes it.
func repAuthorIndexMiddleware(r *repReactor) th.Handler {
	return func(ctx *th.Context, update telego.Update) error {
		if msg := update.Message; msg != nil && msg.From != nil {
			r.RecordMessage(msg)
		}
		return ctx.Next(update)
	}
}

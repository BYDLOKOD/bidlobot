package bot

// Reaction-based reputation.
//
// A 👍 (like) reaction on a supergroup message gives +rep to the message
// author; 👎 (dislike) or 🤡 (clown_face) gives -rep. Several reactions
// landing within repBatchWindow are flushed as ONE combined chat message
// listing all of them (no per-reaction spam). Removing a reaction does
// NOT undo the rep (no reversal).
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
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"

	"github.com/veschin/bidlobot/internal/domain/reputation"
	"github.com/veschin/bidlobot/internal/shared"
	"github.com/veschin/bidlobot/internal/storage"
)

// repBatchWindow is how long rep events accumulate before one combined
// chat message is sent.
const repBatchWindow = 10 * time.Second

// maxIndexedMessages bounds the in-memory author index: the bot keeps
// only the most recent messages it has seen, so reactions on very old
// messages cannot be attributed.
const maxIndexedMessages = 5000

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

// repReactor is the reaction->rep pipeline: author lookup, emoji mapping,
// reputation Apply, and the 10-second batcher that emits one combined
// message per chat.
type repReactor struct {
	store  reputation.Store
	admins AdminChecker
	index  *msgAuthorIndex
	batch  *repBatcher
	log    *slog.Logger
}

// NewRepReactor wires the reaction-rep pipeline. sender is the
// rate-limited public-surface sender used for the combined rep messages.
func NewRepReactor(store reputation.Store, admins AdminChecker, sender repSender, log *slog.Logger) *repReactor {
	if log == nil {
		log = slog.Default()
	}
	return &repReactor{
		store:  store,
		admins: admins,
		index:  newMsgAuthorIndex(maxIndexedMessages),
		batch:  newRepBatcher(sender, log),
		log:    log,
	}
}

// repSender is the narrow Telegram surface the batcher needs.
type repSender interface {
	SendMessage(context.Context, *telego.SendMessageParams) (*telego.Message, error)
}

// RecordMessage feeds the author index from the supergroup message
// stream. Runs as a middleware before the passive observers so the
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
		return nil
	}

	kind, emoji, tracked := repEmojiToKind(reaction.NewReaction)
	if !tracked {
		return nil // custom (premium) emoji or untracked reaction
	}

	author, ok := r.index.Lookup(msgKey{chatID: reaction.Chat.ID, msgID: reaction.MessageID})
	if !ok {
		return nil // message not seen while running
	}
	if author.isBot {
		return nil // never give rep for a bot's message
	}
	if author.userID == reaction.User.ID {
		return nil // self-reaction
	}

	absChat := storage.AbsChatID(reaction.Chat.ID)
	actorAdmin, ok := r.isAdmin(absChat, reaction.User.ID)
	if !ok {
		return nil
	}
	targetAdmin, ok := r.isAdmin(absChat, author.userID)
	if !ok {
		return nil
	}

	if _, err := r.store.Apply(context.Background(), absChat, reaction.User.ID, author.userID, kind, actorAdmin, targetAdmin); err != nil {
		switch {
		case errors.Is(err, reputation.ErrSelfTarget),
			errors.Is(err, reputation.ErrInsufficientBalance),
			errors.Is(err, reputation.ErrTargetInsufficientBalance):
			// Socially expected: self-target, broke actor, broke target.
			// Silent - no chat spam for a reaction.
			return nil
		default:
			r.log.Warn("rep reaction apply failed", "error", err, "chat_id", reaction.Chat.ID)
			return nil
		}
	}

	r.batch.Add(reaction.Chat.ID, repEvent{
		emoji:  emoji,
		delta:  repDelta(kind, targetAdmin),
		target: shared.UserDisplay(author.username, author.firstName),
	})
	return nil
}

// isAdmin resolves admin status for the reputation Apply. ok=false on a
// lookup failure - the caller skips the reaction (better to drop one rep
// than to apply it with a wrong admin flag).
func (r *repReactor) isAdmin(absChatID, userID int64) (bool, bool) {
	if r.admins == nil {
		return false, true
	}
	isAdmin, err := r.admins.IsAdmin(absChatID, userID)
	if err != nil {
		r.log.Warn("rep admin check failed", "error", err, "abs_chat_id", absChatID, "user_id", userID)
		return false, false
	}
	return isAdmin, true
}

// repEmojiToKind maps the first tracked emoji in NewReaction to a
// reputation kind. Returns tracked=false for custom (premium) emoji and
// untracked reactions.
func repEmojiToKind(rs []telego.ReactionType) (kind reputation.Kind, emoji string, tracked bool) {
	for _, rt := range rs {
		e, ok := rt.(*telego.ReactionTypeEmoji)
		if !ok {
			continue
		}
		switch e.Emoji {
		case emojiLike:
			return reputation.KindPraise, e.Emoji, true
		case emojiDislike, emojiClown:
			return reputation.KindRoast, e.Emoji, true
		}
	}
	return 0, "", false
}

// repDelta mirrors the balance math in storage.ReputationRepo.Apply so
// the batcher can report the actual change.
func repDelta(kind reputation.Kind, targetIsAdmin bool) int {
	switch kind {
	case reputation.KindPraise:
		if targetIsAdmin {
			return 6
		}
		return 3
	case reputation.KindRoast:
		return -1
	default:
		return 0
	}
}

// repEvent is one credited rep change, accumulated for the batcher.
type repEvent struct {
	emoji  string
	delta  int
	target string
}

// repBatcher accumulates rep events per chat and flushes ONE combined
// message repBatchWindow after the first event of a batch. The flush
// runs on a timer goroutine so it never blocks the sequential update
// loop. Errors are logged, never surfaced.
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

// flush sends the combined message for a chat and clears the batch. Runs
// on the timer goroutine.
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

	text := composeRepMessage(events)
	if _, err := b.sender.SendMessage(context.Background(), &telego.SendMessageParams{
		ChatID: telego.ChatID{ID: chatID},
		Text:   text,
	}); err != nil {
		b.log.Warn("rep batcher send failed", "chat_id", chatID, "error", err)
	}
}

// composeRepMessage renders the combined rep report. The actor is not
// listed - the emoji plus delta is enough.
func composeRepMessage(events []repEvent) string {
	var b strings.Builder
	b.WriteString("Репутация:\n")
	for _, e := range events {
		fmt.Fprintf(&b, "%s %s %+d\n", e.emoji, e.target, e.delta)
	}
	return strings.TrimRight(b.String(), "\n")
}

// repAuthorIndexMiddleware feeds the rep reactor's author index from the
// supergroup message stream. Runs early, before the passive observers,
// so the original human message is always indexed even when a later
// middleware reposts/deletes it.
func repAuthorIndexMiddleware(r *repReactor) th.Handler {
	return func(ctx *th.Context, update telego.Update) error {
		if msg := update.Message; msg != nil && msg.From != nil {
			r.RecordMessage(msg)
		}
		return ctx.Next(update)
	}
}

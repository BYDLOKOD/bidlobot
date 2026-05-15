package summarize

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/veschin/bidlobot/internal/shared/glm"
)

// ErrNoMessages means the live window is empty (fresh process / nothing
// said yet). The bot turns this into a non-error informational reply.
var ErrNoMessages = errors.New("summarize: no messages in window")

// ErrBusy means a summarization is already running for this chat. The
// LLM call is expensive; one in-flight per chat is enough.
var ErrBusy = errors.New("summarize: already running for this chat")

// Completer is the GLM surface the service needs; satisfied by
// *glm.Client and trivially fakeable in tests.
type Completer interface {
	Complete(ctx context.Context, messages []glm.Message, maxTokens int) (string, glm.Usage, error)
}

// Config tunes the orchestrator. Zero fields fall back to defaults.
type Config struct {
	InputBudgetTokens int           // transcript token ceiling (default below)
	MaxOutputTokens   int           // completion cap (default below)
	CallTimeout       time.Duration // hard ceiling per GLM call (default below)
	GlobalMaxCalls    int           // process-wide cap per GlobalWindow (default below)
	GlobalWindow      time.Duration // rolling window for GlobalMaxCalls (default below)
}

const (
	// Default kept well under GLM-5's 200K window: room for the
	// completion plus a margin for the estimator under-counting
	// code-dense windows (see EstimateTokens). The admin's N and the
	// buffer cap bind long before this does.
	defaultInputBudget = 120_000
	defaultMaxOutput   = 2048
	defaultCallTimeout = 180 * time.Second

	// Process-wide GLM-call ceiling across ALL chats and admins. The
	// single-flight is per-chat only, so without this an admin present
	// in many chats (or a compromised admin account) is an unbounded
	// financial DoS on a paid API.
	defaultGlobalMaxCalls = 40
	defaultGlobalWindow   = time.Hour
)

// Service owns the buffer and orchestrates one summarization at a time
// per chat. It does not touch Telegram; the bot layer drives the
// placeholder/edit and maps typed errors to Russian text.
type Service struct {
	buf *Buffer
	llm Completer
	log *slog.Logger

	inputBudget int
	maxOutput   int
	callTimeout time.Duration

	// appCtx is the process lifetime context; background work derives
	// from it so SIGTERM cancels an in-flight GLM call cleanly. wg is
	// App.InFlight() so Stop() waits for that goroutine.
	appCtx context.Context
	wg     *sync.WaitGroup

	mu      sync.Mutex
	running map[int64]struct{} // single-flight per absChatID

	globalMax    int
	globalWindow time.Duration
	gmu          sync.Mutex
	gcalls       []time.Time // attempt timestamps in the rolling window
}

// NewService wires the orchestrator. buf and llm are required.
func NewService(buf *Buffer, llm Completer, cfg Config, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	s := &Service{
		buf:          buf,
		llm:          llm,
		log:          log,
		inputBudget:  cfg.InputBudgetTokens,
		maxOutput:    cfg.MaxOutputTokens,
		callTimeout:  cfg.CallTimeout,
		globalMax:    cfg.GlobalMaxCalls,
		globalWindow: cfg.GlobalWindow,
		appCtx:       context.Background(),
		running:      make(map[int64]struct{}),
	}
	if s.inputBudget <= 0 {
		s.inputBudget = defaultInputBudget
	}
	if s.maxOutput <= 0 {
		s.maxOutput = defaultMaxOutput
	}
	if s.callTimeout <= 0 {
		s.callTimeout = defaultCallTimeout
	}
	if s.globalMax <= 0 {
		s.globalMax = defaultGlobalMaxCalls
	}
	if s.globalWindow <= 0 {
		s.globalWindow = defaultGlobalWindow
	}
	return s
}

// SetAppContext binds the process lifetime context (mirrors the cleanup
// executor / DM console wiring). Call once at startup before Run.
func (s *Service) SetAppContext(ctx context.Context) { s.appCtx = ctx }

// AttachWaitGroup registers App.InFlight() so Stop() waits for an
// in-flight GLM call within the shutdown budget.
func (s *Service) AttachWaitGroup(wg *sync.WaitGroup) { s.wg = wg }

// OpContext derives a bounded context from the app lifetime context, so
// the post-call Telegram I/O (placeholder send, result edit) is
// cancelled by shutdown instead of running on an orphan
// context.Background() past App.Stop() and writing post-Close.
func (s *Service) OpContext(timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(s.appCtx, timeout)
}

// GlobalAllow enforces a process-wide ceiling on GLM calls across ALL
// chats and admins (the single-flight is per-chat only). It prunes the
// rolling window and records the attempt when allowed; false means the
// window is full (terminal for this invocation - the admin is told to
// retry later). Must be called after the per-chat slot is reserved so a
// busy chat never consumes global budget.
func (s *Service) GlobalAllow() bool {
	now := time.Now()
	s.gmu.Lock()
	defer s.gmu.Unlock()
	cutoff := now.Add(-s.globalWindow)
	kept := s.gcalls[:0]
	for _, t := range s.gcalls {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	s.gcalls = kept
	if len(s.gcalls) >= s.globalMax {
		return false
	}
	s.gcalls = append(s.gcalls, now)
	return true
}

// Record ingests one human message into the live window.
func (s *Service) Record(absChatID int64, e Entry) { s.buf.Record(absChatID, e) }

// UpdateEdited applies a Telegram edit to a message still in the window.
func (s *Service) UpdateEdited(absChatID int64, msgID int, newText string) {
	s.buf.Update(absChatID, msgID, newText)
}

// Available reports how many messages are currently in the live window.
func (s *Service) Available(absChatID int64) int {
	_, total := s.buf.Window(absChatID, 0)
	return total
}

// TryAcquire reserves the single-flight slot for a chat. The caller must
// call Release exactly once iff this returned true.
func (s *Service) TryAcquire(absChatID int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.running[absChatID]; ok {
		return false
	}
	s.running[absChatID] = struct{}{}
	return true
}

// Release frees the single-flight slot.
func (s *Service) Release(absChatID int64) {
	s.mu.Lock()
	delete(s.running, absChatID)
	s.mu.Unlock()
}

// Meta describes what actually went into a completed summary so the bot
// can attribute it ("итог M сообщений HH:MM-HH:MM").
type Meta struct {
	Included  int
	Requested int
	Available int
	From      time.Time
	To        time.Time
}

// Summarize builds the prompt from the chat's window and calls GLM. It
// derives its own deadline from the app context so a hung provider
// cannot outlive shutdown. Returns ErrNoMessages when the window is
// empty, ErrBusy is enforced by the caller via TryAcquire, and the
// glm.Err* sentinels (wrapped) on provider failures.
func (s *Service) Summarize(absChatID int64, requested int) (string, Meta, error) {
	entries, available := s.buf.Window(absChatID, requested)
	if len(entries) == 0 {
		return "", Meta{Available: 0, Requested: requested}, ErrNoMessages
	}
	built, ok := BuildPrompt(entries, requested, available, s.inputBudget)
	if !ok {
		return "", Meta{Available: available, Requested: requested}, ErrNoMessages
	}

	ctx, cancel := context.WithTimeout(s.appCtx, s.callTimeout)
	defer cancel()

	start := time.Now()
	text, usage, err := s.llm.Complete(ctx, built.Messages, s.maxOutput)
	meta := Meta{
		Included:  built.Included,
		Requested: built.Requested,
		Available: built.Available,
		From:      built.From,
		To:        built.To,
	}
	if err != nil {
		s.log.Warn("summarize call failed",
			"abs_chat_id", absChatID, "included", built.Included,
			"est_tokens", built.EstTokens, "elapsed_ms", time.Since(start).Milliseconds(),
			"error", err)
		return "", meta, err
	}
	s.log.Info("summarize ok",
		"abs_chat_id", absChatID, "included", built.Included,
		"est_tokens", built.EstTokens,
		"prompt_tokens", usage.PromptTokens, "completion_tokens", usage.CompletionTokens,
		"elapsed_ms", time.Since(start).Milliseconds())
	return text, meta, nil
}

// Go runs fn as a tracked background goroutine: registered in
// App.InFlight() (so Stop waits for it) and recovered (a panic in the
// GLM path must not take the process down). It mirrors how the cleanup
// executor spawns its workers.
func (s *Service) Go(fn func()) {
	if s.wg != nil {
		s.wg.Add(1)
	}
	go func() {
		defer func() {
			if s.wg != nil {
				s.wg.Done()
			}
			if r := recover(); r != nil {
				s.log.Error("summarize goroutine panic", "panic", r)
			}
		}()
		fn()
	}()
}

package bot

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"

	"github.com/veschin/bidlobot/internal/domain/membership"
	"github.com/veschin/bidlobot/internal/domain/monthstats"
	"github.com/veschin/bidlobot/internal/domain/stats"
	"github.com/veschin/bidlobot/internal/shared"
	"github.com/veschin/bidlobot/internal/testutil"
)

// ShutdownTimeout caps how long App.Stop blocks waiting for in-flight
// handlers. Telegram-side updates we lose during shutdown will be
// replayed on next start via long-polling offset, so a hard cap is
// preferable to a hung process.
const ShutdownTimeout = 10 * time.Second

type App struct {
	bot         *telego.Bot
	log         *slog.Logger
	handler     *th.BotHandler
	adminCache  *shared.AdminCache
	statsBuffer *stats.Buffer
	monthBuffer *monthstats.Buffer
	memberSvc   *membership.Service
	dispatcher  *CallbackDispatcher
	pendingGC   PendingGC
	inlineSvc   *InlineService
	games       *GamesRegistry
	dmConsole   *DMConsole
	cooldown    *cooldown

	// sender is the rate-limited + retried wrapper used for every
	// outgoing message on the public surface (help, onboarding, the
	// moderation-redirect notice). Distinct from `bot`, which stays the
	// raw *telego.Bot because telego's long-poll + handler machinery
	// needs the concrete type. Required NewApp parameter.
	sender shared.TelegramAPI

	// inFlight tracks handler goroutines AND background workers (e.g.
	// cleanup kick worker) so that Stop can wait for them within
	// ShutdownTimeout.
	inFlight sync.WaitGroup

	// healthCheck is the optional /health introspection target. nil when
	// HEALTH_PORT is set to 0.
	healthCheck  *healthChecker
	healthServer *HealthServer
}

// InFlight exposes the WaitGroup for executors that need to register
// background workers. Stop() blocks on this WaitGroup so any registered
// goroutine must respect cancellation via App's run context to ensure
// shutdown completes within ShutdownTimeout.
func (a *App) InFlight() *sync.WaitGroup {
	return &a.inFlight
}

// PendingGC is the narrow API the App needs to periodically clean up
// expired pending actions; declared here so wiring can pass either a
// PendingRepo or a fake without depending on the storage package.
type PendingGC interface {
	GarbageCollect(ctx context.Context, now time.Time) (int, error)
}

// NewApp wires the App. `sender` MUST be the rate-limited tgclient
// wrapper, not the raw *telego.Bot: it carries every public-surface
// send (help, onboarding, the moderation-redirect notice) so a busy
// chat stays inside Telegram's 20 msg/min/chat budget. It is a
// constructor parameter (not a setter) so the type system forbids
// forgetting it.
func NewApp(bot *telego.Bot, sender shared.TelegramAPI, log *slog.Logger, adminCache *shared.AdminCache, statsBuffer *stats.Buffer, monthBuffer *monthstats.Buffer, memberSvc *membership.Service, dispatcher *CallbackDispatcher, pendingGC PendingGC, inlineSvc *InlineService) *App {
	return &App{
		bot:         bot,
		sender:      sender,
		log:         log,
		adminCache:  adminCache,
		statsBuffer: statsBuffer,
		monthBuffer: monthBuffer,
		memberSvc:   memberSvc,
		dispatcher:  dispatcher,
		pendingGC:   pendingGC,
		inlineSvc:   inlineSvc,
		cooldown:    newCooldown(),
	}
}

// AttachHealth wires the /health and /version listener and the in-memory
// healthChecker the bot updates as it processes incoming events. dbOpen
// must report whether the bbolt instance is live; getMeOK should call
// the bot's GetMe (typically through the rate-limited wrapper). Either
// callback may be nil to skip that check.
//
// version and commit override the runtime/debug.ReadBuildInfo defaults.
// Pass empty strings to use whatever the binary was built with.
func (a *App) AttachHealth(dbOpen func() bool, getMeOK func(ctx context.Context) bool, version, commit string) error {
	port, start, err := healthPortFromEnv()
	if err != nil {
		return err
	}
	a.healthCheck = newHealthChecker(dbOpen, getMeOK, version, commit)
	if !start {
		a.log.Info("health server disabled (HEALTH_PORT=0)")
		return nil
	}
	srv, err := newHealthServer(port, a.log, a.healthCheck)
	if err != nil {
		return err
	}
	a.healthServer = srv
	return nil
}

// AttachGames installs the mini-games registry. Call before Run so that
// registerRoutes sees the wiring. Passing nil is a no-op.
func (a *App) AttachGames(g *GamesRegistry) {
	if g == nil {
		return
	}
	a.games = g
	if a.inlineSvc != nil && g.InlineRouter != nil {
		a.inlineSvc.SetGameRouter(g.InlineRouter)
	}
}

// AttachDMConsole installs the private-chat moderation console. Call
// before Run so registerRoutes wires it. Passing nil is a no-op (the
// bot then has no private control surface).
func (a *App) AttachDMConsole(d *DMConsole) {
	a.dmConsole = d
}

func (a *App) Run(ctx context.Context, statsH *stats.Handler) error {
	// Defense in depth: the constructor already requires sender, but a
	// future refactor that builds App via a struct literal would slip a
	// nil through to every public send path. Fail loudly at startup
	// rather than panic in a handler goroutine on first contact.
	if a.sender == nil {
		return errors.New("bot: nil sender; construct App via NewApp with the tgclient wrapper")
	}

	updates, err := a.bot.UpdatesViaLongPolling(ctx, &telego.GetUpdatesParams{
		AllowedUpdates: []string{
			"message",
			"callback_query",
			"my_chat_member",
			"chat_member",
			"message_reaction",
			"inline_query",
		},
	})
	if err != nil {
		return err
	}

	if err := setCommands(ctx, a.bot); err != nil {
		a.log.Warn("set commands failed", "error", err)
	}

	bh, err := th.NewBotHandler(a.bot, updates)
	if err != nil {
		return err
	}
	a.handler = bh

	if os.Getenv("RECORD_UPDATES") != "" {
		recPath := os.Getenv("RECORD_UPDATES")
		rec, err := testutil.NewRecorder(recPath)
		if err != nil {
			a.log.Error("recorder init failed", "error", err)
		} else {
			bh.Use(rec.Middleware())
			a.log.Info("recording updates", "path", recPath)
			defer func() {
				rec.Close()
				a.log.Info("recorded updates", "count", rec.Count())
			}()
		}
	}

	if a.healthCheck != nil {
		bh.Use(a.healthMiddleware())
	}
	bh.Use(a.inFlightMiddleware())

	registerRoutes(bh, a, statsH)

	go a.statsBuffer.Run(ctx, 60*time.Second)
	if a.monthBuffer != nil {
		go a.monthBuffer.Run(ctx, 60*time.Second)
	}
	if a.pendingGC != nil {
		go a.runPendingGC(ctx, time.Minute)
	}

	if a.healthServer != nil {
		a.healthServer.Start()
	}

	a.log.Info("bot started, polling for updates")
	startErr := make(chan error, 1)
	go func() { startErr <- bh.Start() }()

	select {
	case <-ctx.Done():
		return nil
	case err := <-startErr:
		// bh.Start returned without us asking it to (the normal
		// shutdown path cancels ctx, so we'd take the case above).
		// This means the long-poll/handler loop died on its own -
		// surface it so main cancels the app context and we shut down
		// instead of becoming a silent zombie that only /health
		// notices minutes later.
		if err != nil {
			a.log.Error("bot handler exited unexpectedly", "error", err)
			return err
		}
		a.log.Warn("bot handler stopped without shutdown signal")
		return nil
	}
}

// runPendingGC sweeps expired pending actions out of bbolt every
// interval. The 5-minute TTL means a 1-minute sweep keeps stale entries
// out of the way without thrashing on small chats.
func (a *App) runPendingGC(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			n, err := a.pendingGC.GarbageCollect(ctx, time.Now().UTC())
			if err != nil {
				a.log.Warn("pending GC failed", "error", err)
				continue
			}
			if n > 0 {
				a.log.Info("pending GC removed expired actions", "count", n)
			}
		}
	}
}

// Stop runs the shutdown sequence:
//  1. Tell the long-poll loop to stop accepting new updates (handler.StopWithContext).
//  2. Wait for in-flight handlers up to ShutdownTimeout.
//  3. Flush the stats buffer.
//  4. Stop the health listener.
//
// The order matters: in-flight handlers may still write to the stats
// buffer, so flushing must happen after they have settled.
func (a *App) Stop() {
	if a.handler != nil {
		ctx, cancel := context.WithTimeout(context.Background(), ShutdownTimeout)
		defer cancel()
		if err := a.handler.StopWithContext(ctx); err != nil && !errors.Is(err, context.Canceled) {
			a.log.Warn("handler stop", "error", err)
		}
	}

	// Wait for our own in-flight middleware-tracked work in addition to
	// telegohandler's internal WaitGroup, with the same hard deadline.
	waitCh := make(chan struct{})
	go func() {
		a.inFlight.Wait()
		close(waitCh)
	}()
	select {
	case <-waitCh:
	case <-time.After(ShutdownTimeout):
		a.log.Warn("shutdown: in-flight handlers did not finish in time", "timeout", ShutdownTimeout)
	}

	a.statsBuffer.Flush()
	if a.monthBuffer != nil {
		a.monthBuffer.Flush()
	}

	if a.healthServer != nil {
		a.healthServer.Stop()
	}

	a.log.Info("handler stopped, stats flushed")
}

// inFlightMiddleware tracks the per-update handler chain in App.inFlight
// so that Stop can wait for it. We Add(1) before the chain runs and
// Done() after, even on error - the goal is "don't drop work mid-flight",
// not "succeed".
func (a *App) inFlightMiddleware() th.Handler {
	return func(ctx *th.Context, update telego.Update) error {
		a.inFlight.Add(1)
		defer a.inFlight.Done()
		return ctx.Next(update)
	}
}

// healthMiddleware records the receipt of every update so /health can
// report freshness. Runs before any user-facing logic and never errors.
func (a *App) healthMiddleware() th.Handler {
	return func(ctx *th.Context, update telego.Update) error {
		a.healthCheck.MarkUpdate(time.Now())
		return ctx.Next(update)
	}
}

func (a *App) handleHelpDM(_ *th.Context, msg telego.Message) error {
	_, err := a.sender.SendMessage(context.Background(), &telego.SendMessageParams{
		ChatID: telego.ChatID{ID: msg.Chat.ID},
		Text:   helpDM,
	})
	return err
}

func (a *App) handleHelpSupergroup(_ *th.Context, msg telego.Message) error {
	_, err := a.sender.SendMessage(context.Background(), &telego.SendMessageParams{
		ChatID: telego.ChatID{ID: msg.Chat.ID},
		Text:   helpSupergroup,
	})
	return err
}

const helpDM = `BidloBot - управление IT-сообществом.

Добавьте меня в группу администратором (минимум: право ограничивать участников), затем отправьте /start, чтобы управлять чатом приватно.`

const helpSupergroup = `BidloBot - статистика, мини-игры и приватная модерация.

Здесь, в чате:
  /stats         - обзор чата
  /stats top     - топ участников
  /stats today   - активность за день
  /dice [emoji]  - бросок кубика
  /battle X Y    - голосование реакциями за 60с
  /quiz          - угадай язык по сниппету

Модерация и чистка неактивных - только в личке со мной
(участники чата ничего не видят). Откройте личный чат
со мной и отправьте /start.`

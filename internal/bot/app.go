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
	"github.com/veschin/bidlobot/internal/domain/moderation"
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
	memberSvc   *membership.Service
	dispatcher  *CallbackDispatcher
	pendingGC   PendingGC
	inlineSvc   *InlineService

	// inFlight tracks handler goroutines so that Stop can wait for
	// them within ShutdownTimeout.
	inFlight sync.WaitGroup

	// healthCheck is the optional /health introspection target. nil when
	// HEALTH_PORT is set to 0.
	healthCheck  *healthChecker
	healthServer *HealthServer
}

// PendingGC is the narrow API the App needs to periodically clean up
// expired pending actions; declared here so wiring can pass either a
// PendingRepo or a fake without depending on the storage package.
type PendingGC interface {
	GarbageCollect(ctx context.Context, now time.Time) (int, error)
}

func NewApp(bot *telego.Bot, log *slog.Logger, adminCache *shared.AdminCache, statsBuffer *stats.Buffer, memberSvc *membership.Service, dispatcher *CallbackDispatcher, pendingGC PendingGC, inlineSvc *InlineService) *App {
	return &App{
		bot:         bot,
		log:         log,
		adminCache:  adminCache,
		statsBuffer: statsBuffer,
		memberSvc:   memberSvc,
		dispatcher:  dispatcher,
		pendingGC:   pendingGC,
		inlineSvc:   inlineSvc,
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

func (a *App) Run(ctx context.Context, statsH *stats.Handler, modH *moderation.Handler) error {
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

	registerRoutes(bh, a, statsH, modH)

	go a.statsBuffer.Run(ctx, 60*time.Second)
	if a.pendingGC != nil {
		go a.runPendingGC(ctx, time.Minute)
	}

	if a.healthServer != nil {
		a.healthServer.Start()
	}

	a.log.Info("bot started, polling for updates")
	go bh.Start()

	<-ctx.Done()
	return nil
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
	_, err := a.bot.SendMessage(context.Background(), &telego.SendMessageParams{
		ChatID: telego.ChatID{ID: msg.Chat.ID},
		Text:   helpDM,
	})
	return err
}

func (a *App) handleHelpSupergroup(_ *th.Context, msg telego.Message) error {
	_, err := a.bot.SendMessage(context.Background(), &telego.SendMessageParams{
		ChatID: telego.ChatID{ID: msg.Chat.ID},
		Text:   helpSupergroup,
	})
	return err
}

const helpDM = `BidloBot - управление IT-сообществом.

Бот работает в supergroup-чатах. Добавь меня в группу с правами администратора (минимум: Restrict Members), чтобы начать.`

const helpSupergroup = `BidloBot - статистика и модерация чата.

Stats:
  /stats         - обзор чата
  /stats top     - топ участников
  /stats today   - активность за день
  /stats @user   - статистика пользователя

Moderation (только для админов):
  /warn @user [причина]   - выдать предупреждение
  /warns @user            - посмотреть предупреждения
  /warns clear @user      - сбросить предупреждения
  /mute @user [время]     - заглушить (по умолчанию 1ч)
  /unmute @user           - снять mute
  /ban @user [причина]    - забанить
  /unban @user            - разбанить`

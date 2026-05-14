package bot

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"

	"github.com/veschin/bidlobot/internal/domain/membership"
	"github.com/veschin/bidlobot/internal/domain/moderation"
	"github.com/veschin/bidlobot/internal/domain/stats"
	"github.com/veschin/bidlobot/internal/shared"
	"github.com/veschin/bidlobot/internal/testutil"
)

type App struct {
	bot         *telego.Bot
	log         *slog.Logger
	handler     *th.BotHandler
	adminCache  *shared.AdminCache
	statsBuffer *stats.Buffer
	memberSvc   *membership.Service
}

func NewApp(bot *telego.Bot, log *slog.Logger, adminCache *shared.AdminCache, statsBuffer *stats.Buffer, memberSvc *membership.Service) *App {
	return &App{
		bot:         bot,
		log:         log,
		adminCache:  adminCache,
		statsBuffer: statsBuffer,
		memberSvc:   memberSvc,
	}
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

	registerRoutes(bh, a, statsH, modH)

	go a.statsBuffer.Run(ctx, 60*time.Second)

	a.log.Info("bot started, polling for updates")
	go bh.Start()

	<-ctx.Done()
	return nil
}

func (a *App) Stop() {
	if a.handler != nil {
		a.handler.Stop()
	}
	a.statsBuffer.Flush()
	a.log.Info("handler stopped, stats flushed")
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

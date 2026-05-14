package bot

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"

	"github.com/veschin/bidlobot/internal/domain/moderation"
	"github.com/veschin/bidlobot/internal/domain/profile"
	"github.com/veschin/bidlobot/internal/domain/stats"
	"github.com/veschin/bidlobot/internal/shared"
	"github.com/veschin/bidlobot/internal/testutil"
	"github.com/veschin/bidlobot/internal/text"
)

type App struct {
	bot         *telego.Bot
	log         *slog.Logger
	handler     *th.BotHandler
	adminCache  *shared.AdminCache
	statsBuffer *stats.Buffer
}

func NewApp(bot *telego.Bot, log *slog.Logger, adminCache *shared.AdminCache, statsBuffer *stats.Buffer) *App {
	return &App{
		bot:         bot,
		log:         log,
		adminCache:  adminCache,
		statsBuffer: statsBuffer,
	}
}

func (a *App) Run(ctx context.Context, profileH *profile.Handler, statsH *stats.Handler, modH *moderation.Handler) error {
	updates, err := a.bot.UpdatesViaLongPolling(ctx, &telego.GetUpdatesParams{
		AllowedUpdates: []string{
			"message",
			"callback_query",
			"my_chat_member",
			"chat_member",
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

	registerRoutes(bh, a, profileH, statsH, modH)

	go a.statsBuffer.Run(ctx, 60*time.Second)
	go profileH.FSMSweeper(ctx)

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

const helpDM = text.MsgWelcomeDM + "\n\nIf you're currently registering, type /cancel to abort."

const helpSupergroup = `BidloBot - profiles, stats, moderation

Profiles:
  /register - create your profile
  /profile - view your profile
  /profile @user - view someone's profile
  /update - edit your profile
  /update field value - quick edit

Stats:
  /stats - chat overview
  /stats top - top contributors
  /stats today - today's activity
  /stats @user - user stats

Moderation (admins only):
  /warn @user reason - issue warning
  /warns @user - view warnings
  /warns clear @user - clear warnings
  /mute @user [duration] - mute (default: 1h)
  /unmute @user - unmute
  /ban @user [reason] - ban
  /unban @user - unban`

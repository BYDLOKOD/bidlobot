package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/mymmrac/telego"

	"github.com/veschin/bidlobot/internal/bot"
	"github.com/veschin/bidlobot/internal/domain/membership"
	"github.com/veschin/bidlobot/internal/domain/moderation"
	"github.com/veschin/bidlobot/internal/domain/stats"
	"github.com/veschin/bidlobot/internal/shared"
	"github.com/veschin/bidlobot/internal/storage"
)

func main() {
	log := setupLogger()

	token := os.Getenv("TG_BOT_TOKEN")
	if token == "" {
		log.Error("TG_BOT_TOKEN is required")
		os.Exit(1)
	}

	dbPath := envOr("DB_PATH", "./data")
	if err := os.MkdirAll(dbPath, 0755); err != nil {
		log.Error("create data dir", "error", err)
		os.Exit(1)
	}

	tgBot, err := telego.NewBot(token)
	if err != nil {
		log.Error("create telegram bot", "error", err)
		os.Exit(1)
	}

	store, err := storage.NewBoltStore(filepath.Join(dbPath, "bidlobot.db"))
	if err != nil {
		log.Error("open database", "error", err)
		os.Exit(1)
	}

	db := store.DB()

	botInfo, err := tgBot.GetMe(context.Background())
	if err != nil {
		log.Error("get bot info", "error", err)
		os.Exit(1)
	}
	log.Info("authenticated", "bot", botInfo.Username, "id", botInfo.ID)

	adminCache := shared.NewAdminCache(tgBot, botInfo.ID, log)

	statsRepo := storage.NewStatsRepo(db)
	warnRepo := storage.NewWarnRepo(db)
	memberRepo := storage.NewMembershipRepo(db)
	pendingRepo := storage.NewPendingRepo(db)

	memberSvc := membership.NewService(memberRepo, log)

	displayResolver := &membershipDisplayResolver{repo: memberRepo}
	statsLookup := &membershipStatsLookup{repo: memberRepo}
	modLookup := &membershipModerationLookup{repo: memberRepo}

	statsBuffer := stats.NewBuffer(statsRepo, log)
	statsSvc := stats.NewService(statsRepo, statsBuffer, displayResolver, log)
	statsHandler := stats.NewHandler(statsSvc, statsLookup, log)

	modSvc := moderation.NewService(warnRepo, tgBot, adminCache, log)
	modHandler := moderation.NewHandler(modSvc, adminCache, modLookup, log)

	dispatcher := bot.NewCallbackDispatcher(pendingRepo, adminCache, tgBot, log)
	inlineSvc := bot.NewInlineService(pendingRepo, log)
	// Phase 3d/3e will register destructive executors here.

	app := bot.NewApp(tgBot, log, adminCache, statsBuffer, memberSvc, dispatcher, pendingRepo, inlineSvc)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	go func() {
		if err := app.Run(ctx, statsHandler, modHandler); err != nil {
			log.Error("bot run error", "error", err)
			cancel()
		}
	}()

	<-ctx.Done()
	log.Info("shutdown signal received")

	app.Stop()

	if err := store.Close(); err != nil {
		log.Error("close database", "error", err)
	}

	log.Info("shutdown complete")
}

func setupLogger() *slog.Logger {
	level := slog.LevelInfo
	switch strings.ToLower(envOr("LOG_LEVEL", "info")) {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}

	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: level,
	}))
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// Three thin adapters around MembershipRepo, one per consumer interface
// the cross-domain wiring needs. Putting them in cmd keeps the domain
// packages free of cross-imports.

type membershipStatsLookup struct {
	repo *storage.MembershipRepo
}

func (l *membershipStatsLookup) GetByUsername(ctx context.Context, absChatID int64, username string) (int64, error) {
	m, err := l.repo.GetMemberByUsername(ctx, absChatID, username)
	if err != nil {
		return 0, err
	}
	return m.UserID, nil
}

type membershipModerationLookup struct {
	repo *storage.MembershipRepo
}

func (l *membershipModerationLookup) GetByUsername(ctx context.Context, absChatID int64, username string) (int64, bool, error) {
	m, err := l.repo.GetMemberByUsername(ctx, absChatID, username)
	if err != nil {
		return 0, false, err
	}
	return m.UserID, m.IsBot, nil
}

type membershipDisplayResolver struct {
	repo *storage.MembershipRepo
}

func (r *membershipDisplayResolver) UserDisplay(ctx context.Context, absChatID, userID int64) string {
	m, err := r.repo.GetMember(ctx, userID, absChatID)
	if err != nil {
		return ""
	}
	return shared.UserDisplay(m.Username, m.FirstName)
}

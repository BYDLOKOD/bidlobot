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

	statsBuffer := stats.NewBuffer(statsRepo, log)
	statsSvc := stats.NewService(statsRepo, statsBuffer, log)
	statsHandler := stats.NewHandler(statsSvc, nil, log)

	modSvc := moderation.NewService(warnRepo, tgBot, adminCache, log)
	modHandler := moderation.NewHandler(modSvc, adminCache, nil, log)

	app := bot.NewApp(tgBot, log, adminCache, statsBuffer)

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

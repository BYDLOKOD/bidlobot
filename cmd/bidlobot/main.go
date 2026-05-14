package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/mymmrac/telego"

	"github.com/veschin/bidlobot/internal/bot"
	"github.com/veschin/bidlobot/internal/domain/cleanup"
	"github.com/veschin/bidlobot/internal/domain/membership"
	"github.com/veschin/bidlobot/internal/domain/moderation"
	"github.com/veschin/bidlobot/internal/domain/stats"
	"github.com/veschin/bidlobot/internal/shared"
	"github.com/veschin/bidlobot/internal/shared/ratelimit"
	"github.com/veschin/bidlobot/internal/shared/retry"
	"github.com/veschin/bidlobot/internal/shared/tgclient"
	"github.com/veschin/bidlobot/internal/storage"
)

// version and commit may be overridden via:
//
//	go build -ldflags "-X main.version=v1.0.0 -X main.commit=$(git rev-parse HEAD)"
//
// Otherwise they fall back to runtime/debug.ReadBuildInfo at startup.
var (
	version = ""
	commit  = ""
)

func main() {
	flagVersion := flag.Bool("version", false, "print build version and exit")
	flagCheck := flag.Bool("check-config", false, "validate config and exit (0 ok, 1 invalid)")
	flag.Parse()

	meta := versionFromRuntime(version, commit)
	if *flagVersion {
		fmt.Println(meta.String())
		return
	}

	cfg := loadConfig()
	if err := cfg.Validate(); err != nil {
		fmt.Fprintln(os.Stderr, "configuration invalid:")
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
	if *flagCheck {
		fmt.Println("configuration ok")
		return
	}

	log := setupLogger(cfg.LogLevel)
	log.Info("starting", "build", meta.String())

	if err := os.MkdirAll(cfg.DBPath, 0o755); err != nil {
		log.Error("create data dir", "error", err)
		os.Exit(1)
	}

	tgBot, err := telego.NewBot(cfg.Token)
	if err != nil {
		log.Error("create telegram bot", "error", err)
		os.Exit(1)
	}

	store, err := storage.NewBoltStore(filepath.Join(cfg.DBPath, "bidlobot.db"))
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

	limiter := ratelimit.New(ratelimit.Config{Logger: log})
	defer limiter.Close()

	tgClient, err := tgclient.New(tgclient.Config{
		Bot:         tgBot,
		Limiter:     limiter,
		RetryPolicy: retry.Policy{},
		Migrator:    store,
		Admin:       adminCache,
		Logger:      log,
	})
	if err != nil {
		log.Error("init telegram client wrapper", "error", err)
		os.Exit(1)
	}

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

	// Moderation routes its writes through the wrapped client so 429/5xx,
	// migration, and per-chat rate limits all apply to ban/restrict/etc.
	modSvc := moderation.NewService(warnRepo, tgClient, adminCache, log)
	modHandler := moderation.NewHandler(modSvc, adminCache, modLookup, log)

	dispatcher := bot.NewCallbackDispatcher(pendingRepo, adminCache, tgBot, log)
	inlineSvc := bot.NewInlineService(pendingRepo, log)

	modExecutor := bot.NewModerationExecutor(modSvc, memberRepo, adminCache, log)
	modExecutor.RegisterAll(dispatcher)

	cleanupSvc := cleanup.NewService(memberRepo, tgBot, log)
	cleanupExecutor := bot.NewCleanupExecutor(cleanupSvc, pendingRepo, tgBot, log)
	cleanupExecutor.RegisterAll(dispatcher)

	app := bot.NewApp(tgBot, log, adminCache, statsBuffer, memberSvc, dispatcher, pendingRepo, inlineSvc)
	if err := app.AttachHealth(
		func() bool {
			return db != nil && db.Path() != ""
		},
		func(ctx context.Context) bool {
			cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
			defer cancel()
			_, gerr := tgClient.GetMe(cctx)
			return gerr == nil
		},
		meta.Version,
		meta.Commit,
	); err != nil {
		log.Error("attach health", "error", err)
		os.Exit(1)
	}

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

func setupLogger(level string) *slog.Logger {
	lvl := slog.LevelInfo
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl}))
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

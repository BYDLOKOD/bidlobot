package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/mymmrac/telego"
	bolt "go.etcd.io/bbolt"

	"github.com/veschin/bidlobot/internal/bot"
	"github.com/veschin/bidlobot/internal/domain/captcha"
	"github.com/veschin/bidlobot/internal/domain/cleanup"
	"github.com/veschin/bidlobot/internal/domain/gracekick"
	"github.com/veschin/bidlobot/internal/domain/membership"
	"github.com/veschin/bidlobot/internal/domain/moderation"
	"github.com/veschin/bidlobot/internal/domain/monthstats"
	"github.com/veschin/bidlobot/internal/domain/stats"
	"github.com/veschin/bidlobot/internal/domain/summarize"
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

	statsBuffer := stats.NewBuffer(statsRepo, log)
	statsSvc := stats.NewService(statsRepo, statsBuffer, displayResolver, log)

	// Retroactive monthly nominations (chat-export.org parity). Fed by
	// the same live message handler and by the DM history import; the
	// importer's idempotency keeps the two from double-counting.
	monthRepo := storage.NewMonthStatsRepo(db)
	monthBuffer := monthstats.NewBuffer(monthRepo, log)
	monthSvc := monthstats.NewService(monthRepo, monthBuffer, displayResolver, log)

	statsHandler := stats.NewHandler(statsSvc, monthSvc, statsLookup, tgClient, log)

	// Moderation routes its writes through the wrapped client so 429/5xx,
	// migration, and per-chat rate limits all apply to ban/restrict/etc.
	// The service is consumed only by the private DM console now - the
	// public slash/inline handlers were removed (privacy principle).
	modSvc := moderation.NewService(warnRepo, tgClient, adminCache, log)

	dispatcher := bot.NewCallbackDispatcher(pendingRepo, adminCache, tgBot, log)
	inlineSvc := bot.NewInlineService(log)

	modExecutor := bot.NewModerationExecutor(modSvc, memberRepo, adminCache, log)
	modExecutor.RegisterAll(dispatcher)

	// Cleanup kicks go through the rate-limited + retried wrapper so that
	// a 200-candidate sweep never trips Telegram's per-chat budget and
	// transient 429/5xx don't kill the worker mid-flight. Progress
	// EditMessageText calls go through the wrapper for the same reason.
	// cleanupSvc is the evidence-graded previewer + ban+unban executor.
	// It is driven by the DM /cleanup campaign (gracekick), never by a
	// public callback - the old immediate-kick CleanupExecutor was
	// removed (the owner's model is the daily public grace lifecycle).
	cleanupSvc := cleanup.NewService(memberRepo, tgClient, log)
	// Chat summarization via Pi/OMP (always on). Validates the binary at
	// startup; a missing or non-executable binary fails fast.
	piBinary := cfg.PIBinary
	piModel := cfg.PIModel
	pi := summarize.NewPiRunner(piBinary, piModel)
	summarizeSvc := summarize.NewService(
		summarize.NewBuffer(summarize.BufferConfig{}),
		pi,
		summarize.Config{},
		log,
	)
	// Validate Pi binary is executable before wiring into the bot.
	if _, err := exec.LookPath(piBinary); err != nil {
		log.Error("Pi binary unavailable")
		os.Exit(1)
	}
	log.Info("chat summarization enabled")
	// tgClient (rate-limited + retried) is the public-surface sender:
	// help, onboarding - same budget as games/stats so a busy chat stays
	// inside Telegram's 20 msg/min/chat.
	app := bot.NewApp(tgBot, tgClient, log, adminCache, statsBuffer, monthBuffer, memberSvc, dispatcher, pendingRepo, inlineSvc)
	app.SetBotOwnerID(cfg.BotOwnerID)
	if err := app.AttachHealth(
		// dbOpen probes bbolt with a no-op view txn. Path() returning a
		// non-empty string is a tautology (it's set at open time and
		// never cleared on Close), so we issue an actual transaction to
		// confirm the underlying file handle is alive.
		func() bool {
			if db == nil {
				return false
			}
			return db.View(func(_ *bolt.Tx) error { return nil }) == nil
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

	// Phase 4 mini-games: dice / battle / quiz. Constructor wires the
	// inline router and slash handlers; AttachGames installs them on App.
	app.AttachGames(buildGames(db, tgClient, botInfo.Username, adminCache, log))

	// Chat-scoped referral catalog: /refs lists, /refreg registers,
	// /refreport is admin moderation. Always on; the handler is nil-
	// safe against a missing storage layer.
	app.AttachReferrals(bot.NewReferralHandler(tgClient, storage.NewReferralRepo(db), adminCache, log))

	// Summarization via Pi/OMP. Always-on; validated binary at startup.
	app.AttachSummarize(summarizeSvc, tgClient)

	// Inactive-cleanup campaign. Command-driven: nothing happens until
	// an admin runs `/cleanup`. The daily scheduler then drives the
	// public tag -> grace -> kick lifecycle. cleanupSvc is the ban+unban
	// kicker.
	gkRepo := storage.NewGraceKickRepo(db)
	gkSvc := gracekick.NewService(
		gkRepo, cleanupSvc, memberRepo, tgClient,
		gracekick.Config{
			Grace: cfg.CleanupGrace,
			Batch: cfg.CleanupDailyBatch,
		},
		log,
	)
	app.AttachDailyCleanup(gkSvc, cfg.CleanupDailyAtMin)
	log.Info("inactive-cleanup campaign wired (command-driven)",
		"daily_at_utc", cfg.CleanupDailyAtRaw,
		"grace", cfg.CleanupGraceRaw, "batch", cfg.CleanupDailyBatch)

	// New-member captcha (opt-in via CAPTCHA_ENABLED). When off, the bot
	// is unchanged. The service reuses tgClient (rate-limited + retried)
	// for every send/edit/restrict/kick; the kick is a self-contained
	// ban+unban sequence, so it has no dependency on cleanupSvc.
	if cfg.CaptchaEnabled {
		captchaRepo := storage.NewCaptchaRepo(db)
		captchaSvc := captcha.NewService(captchaRepo, tgClient.Bot(), log, cfg.CaptchaTimeout)
		app.AttachCaptcha(captchaSvc)
		log.Info("captcha enabled", "timeout", cfg.CaptchaTimeoutRaw)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	summarizeSvc.SetAppContext(ctx)
	summarizeSvc.AttachWaitGroup(app.InFlight())

	go func() {
		if err := app.Run(ctx, statsHandler); err != nil {
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

type membershipDisplayResolver struct {
	repo *storage.MembershipRepo
}

func (r *membershipDisplayResolver) UserDisplay(ctx context.Context, absChatID, userID int64) string {
	m, err := r.repo.GetMember(ctx, userID, absChatID)
	if err != nil {
		return ""
	}
	username, firstName := m.Username, m.FirstName
	if m.KnownVia == membership.SourceImport {
		// A Telegram-Desktop export's `from` is the display name AS THE
		// EXPORTING ACCOUNT'S ADDRESS BOOK SEES IT, not the member's own
		// profile/chat name. The operator did the export, so an
		// import-only user's `FirstName` is the operator's PRIVATE
		// CONTACT LABEL for that person (whatever the operator saved
		// them as in their own address book). Surfacing it in public
		// stats both misnames the user and leaks the operator's
		// contacts. Drop it: fall back to the neutral
		// "User <id>" (the caller's empty-string fallback) until the
		// user writes live, at which point SourceMessage overwrites
		// SourceImport with their real profile name+handle and the row
		// self-heals - durably: KnownVia precedence is monotonic, so a
		// later periodic re-import will NOT downgrade them back to
		// SourceImport (see storage.MembershipRepo.UpsertMember).
		username, firstName = "", ""
	}
	// Stats lists show name + handle together (handle WITHOUT '@' - an
	// '@handle' would notify that member every time anyone reads stats;
	// see shared.UserDisplayFull).
	d := shared.UserDisplayFull(username, firstName)
	if d == "" {
		return "" // nothing known -> caller falls back to "User <id>"
	}
	if m.Username == "" {
		// No @handle: a display name alone is NOT unique - several
		// members can share one, and Telegram-Desktop-imported users
		// have no username at all. Append the stable numeric id so two
		// distinct same-name users never collapse into one line. The
		// id disappears automatically once the user writes live and a
		// @handle (globally unique) becomes available.
		d += fmt.Sprintf(" (id %d)", userID)
	}
	return d
}

package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"runtime/debug"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// HealthDefaults centralize the policy choices for the /health endpoint.
const (
	// HealthFreshnessWindow is the maximum age of the last successful
	// getUpdates poll cycle before /health flips to 503. The heartbeat is
	// recorded on every completed cycle - including empty long-poll
	// responses - so five minutes of silence means the poll loop is
	// wedged or Telegram is unreachable, never merely "the chat is
	// quiet". Five minutes spans several 30s long-poll cycles, so a
	// single wedged cycle is absorbed and a sustained outage trips within
	// a typical alerting interval.
	HealthFreshnessWindow = 5 * time.Minute

	// HealthStartupGrace gives the bot time to complete its first poll
	// cycle after a restart (Telegram cold starts can be slow). Without
	// this grace a fresh bot would always look degraded.
	HealthStartupGrace = HealthFreshnessWindow

	// healthEnvPort env var name; the value parses as a TCP port. "0"
	// disables the listener entirely. Empty disables.
	healthEnvPort = "HEALTH_PORT"
)

// HealthStatus is the external response shape. Stable contract for any
// external uptime probe.
type HealthStatus struct {
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

// VersionInfo is the response of GET /version. Populated from
// runtime/debug.ReadBuildInfo plus optional ldflags-injected globals.
type VersionInfo struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	GoVersion string `json:"go_version"`
	BuildTime string `json:"build_time,omitempty"`
}

// healthChecker captures everything /health needs to know about the
// running bot. Fields are filled by the App at construction; nothing
// here knows about Telegram or bbolt directly.
//
// lastPoll is the freshness clock: the long-poll loop heartbeats it via
// MarkPoll after every successful getUpdates cycle. getMeOK is
// best-effort and cached: a 60s window prevents the handler from
// amplifying transient Telegram 5xx into a restart loop while still
// surfacing a sustained outage within ~1.5 healthcheck intervals.
type healthChecker struct {
	startedAt time.Time
	lastPoll  atomic.Int64 // unix seconds of the last completed poll cycle
	dbOpen    func() bool
	getMeOK   func(ctx context.Context) bool

	// getMe result cache.
	getMeMu       sync.Mutex
	getMeCachedOK bool
	getMeCachedAt time.Time

	// version metadata
	version VersionInfo
}

// getMeCacheTTL bounds how long a cached getMe result is trusted.
// 60s = two healthcheck intervals, so a real outage flips /health red
// in 60-90s, but a single Telegram blip is absorbed.
const getMeCacheTTL = 60 * time.Second

// HealthServer wraps http.Server lifecycle. Created via newHealthServer;
// Start spawns a goroutine; Stop blocks until the listener is gone.
type HealthServer struct {
	srv      *http.Server
	listener net.Listener
	addr     string
	log      *slog.Logger

	stopOnce sync.Once
	doneCh   chan struct{}
}

// healthPortFromEnv parses HEALTH_PORT. Returns the port (>0) and a
// boolean indicating whether the listener should be started. Returns
// an error only when the value is set but unparseable.
func healthPortFromEnv() (int, bool, error) {
	raw := os.Getenv(healthEnvPort)
	if raw == "" {
		return 8080, true, nil // default
	}
	port, err := strconv.Atoi(raw)
	if err != nil {
		return 0, false, fmt.Errorf("HEALTH_PORT: invalid integer %q: %w", raw, err)
	}
	if port == 0 {
		return 0, false, nil
	}
	if port < 1 || port > 65535 {
		return 0, false, fmt.Errorf("HEALTH_PORT: out of range 1..65535: %d", port)
	}
	return port, true, nil
}

// newHealthServer builds an unstarted HealthServer bound to addr. Returns
// (nil, nil) when the server is intentionally disabled (HEALTH_PORT=0).
func newHealthServer(port int, log *slog.Logger, hc *healthChecker) (*HealthServer, error) {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", healthHandler(hc))
	mux.HandleFunc("/livez", livezHandler())
	mux.HandleFunc("/version", versionHandler(hc))

	addr := fmt.Sprintf(":%d", port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("health listen %s: %w", addr, err)
	}

	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	return &HealthServer{
		srv:      srv,
		listener: ln,
		addr:     ln.Addr().String(),
		log:      log,
		doneCh:   make(chan struct{}),
	}, nil
}

// Start runs the HTTP server in a background goroutine.
func (h *HealthServer) Start() {
	go func() {
		defer close(h.doneCh)
		err := h.srv.Serve(h.listener)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			h.log.Error("health server stopped with error", "error", err, "addr", h.addr)
		}
	}()
	h.log.Info("health server listening", "addr", h.addr)
}

// Stop performs a graceful shutdown with a fixed deadline. Safe to call
// multiple times.
func (h *HealthServer) Stop() {
	h.stopOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := h.srv.Shutdown(ctx); err != nil {
			h.log.Warn("health server shutdown", "error", err)
		}
		<-h.doneCh
	})
}

// Addr returns the actual listening address ("127.0.0.1:port"). Useful
// for tests using port :0.
func (h *HealthServer) Addr() string { return h.addr }

// healthHandler returns the chosen status code and body for a probe.
// Pure function over a healthChecker so tests can call it via httptest
// without spinning a real listener.
func healthHandler(hc *healthChecker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		now := time.Now()
		degraded := func(reason string) {
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(HealthStatus{Status: "degraded", Reason: reason})
		}

		if hc.dbOpen != nil && !hc.dbOpen() {
			degraded("database closed")
			return
		}

		// Poll freshness check, with startup grace. A completed poll
		// cycle is liveness here, so a quiet chat does not flip /health.
		last := hc.lastPoll.Load()
		if last == 0 {
			if now.Sub(hc.startedAt) > HealthStartupGrace {
				degraded("no successful Telegram poll since startup")
				return
			}
		} else {
			lastT := time.Unix(last, 0)
			if now.Sub(lastT) > HealthFreshnessWindow {
				degraded(fmt.Sprintf("last successful poll %s ago (>%s)",
					now.Sub(lastT).Truncate(time.Second), HealthFreshnessWindow))
				return
			}
		}

		// Bot reachability check, cached. See getMeCacheTTL: brief
		// Telegram 5xx bursts must not flip /health red repeatedly,
		// because compose `restart: unless-stopped` would then bounce
		// the bot during the very Telegram incident the bot is
		// trying to ride out.
		if hc.getMeOK != nil && !hc.checkGetMeCached(r.Context()) {
			degraded("getMe failed")
			return
		}

		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(HealthStatus{Status: "ok"})
	}
}

// livezHandler answers /livez with a static 200 {"status":"ok"} and no
// checks at all. It is the probe for container platforms whose liveness
// probe restarts the process on failure: serving HTTP is the liveness
// proof, so a Telegram outage, a closed DB, or a quiet chat can never
// restart the container through this endpoint. Dependency health stays
// on /health.
func livezHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(HealthStatus{Status: "ok"})
	}
}

func versionHandler(hc *healthChecker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(hc.version)
	}
}

// newHealthChecker constructs the checker with build info populated from
// runtime/debug.ReadBuildInfo. The caller must heartbeat it via MarkPoll
// as poll cycles complete.
func newHealthChecker(dbOpen func() bool, getMeOK func(ctx context.Context) bool, version, commit string) *healthChecker {
	hc := &healthChecker{
		startedAt: time.Now(),
		dbOpen:    dbOpen,
		getMeOK:   getMeOK,
		version:   versionInfoFromRuntime(version, commit),
	}
	return hc
}

// MarkPoll records the timestamp of a completed getUpdates poll cycle.
// Called by the bot's long-poll loop after every successful fetch, which
// includes empty long-poll responses - that is exactly the heartbeat
// /health's freshness window is keyed on.
func (hc *healthChecker) MarkPoll(t time.Time) {
	hc.lastPoll.Store(t.Unix())
}

// checkGetMeCached returns the cached getMe verdict if still fresh, or
// re-runs getMeOK against ctx with a 2s timeout. The cache is a small
// state machine guarded by a Mutex - lock contention is a non-issue
// since /health requests at compose-default 30s intervals.
func (hc *healthChecker) checkGetMeCached(parent context.Context) bool {
	hc.getMeMu.Lock()
	if !hc.getMeCachedAt.IsZero() && time.Since(hc.getMeCachedAt) < getMeCacheTTL {
		ok := hc.getMeCachedOK
		hc.getMeMu.Unlock()
		return ok
	}
	hc.getMeMu.Unlock()

	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	ok := hc.getMeOK(ctx)

	hc.getMeMu.Lock()
	hc.getMeCachedOK = ok
	hc.getMeCachedAt = time.Now()
	hc.getMeMu.Unlock()

	return ok
}

func versionInfoFromRuntime(overrideVersion, overrideCommit string) VersionInfo {
	v := VersionInfo{
		Version:   overrideVersion,
		Commit:    overrideCommit,
		GoVersion: "",
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		if v.GoVersion == "" {
			v.GoVersion = info.GoVersion
		}
		if v.Version == "" || v.Version == "(devel)" {
			v.Version = info.Main.Version
		}
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				if v.Commit == "" {
					v.Commit = s.Value
				}
			case "vcs.time":
				v.BuildTime = s.Value
			}
		}
	}
	if v.Version == "" {
		v.Version = "unknown"
	}
	return v
}

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
	// HealthFreshnessWindow is the maximum age of the last incoming update
	// before /health flips to 503. Five minutes is short enough to detect
	// a wedged long-poll loop within a typical alerting interval but long
	// enough to absorb a quiet supergroup over the weekend.
	HealthFreshnessWindow = 5 * time.Minute

	// HealthStartupGrace gives the bot time to receive its first update
	// after a restart. Without this grace a fresh bot would always look
	// degraded.
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
type healthChecker struct {
	startedAt   time.Time
	lastUpdate  atomic.Int64 // unix seconds
	dbOpen      func() bool
	getMeOK     func(ctx context.Context) bool

	// version metadata
	version VersionInfo
}

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

		// Update freshness check, with startup grace.
		last := hc.lastUpdate.Load()
		if last == 0 {
			if now.Sub(hc.startedAt) > HealthStartupGrace {
				degraded("no updates received since startup")
				return
			}
		} else {
			lastT := time.Unix(last, 0)
			if now.Sub(lastT) > HealthFreshnessWindow {
				degraded(fmt.Sprintf("last update %s ago (>%s)",
					now.Sub(lastT).Truncate(time.Second), HealthFreshnessWindow))
				return
			}
		}

		// Bot reachability check is best-effort: a single failure does
		// not flip /health red - only repeat failures matter, and those
		// will manifest as stale updates.
		if hc.getMeOK != nil {
			ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
			defer cancel()
			if !hc.getMeOK(ctx) {
				degraded("getMe failed")
				return
			}
		}

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
// runtime/debug.ReadBuildInfo. Caller must update lastUpdate as
// updates arrive.
func newHealthChecker(dbOpen func() bool, getMeOK func(ctx context.Context) bool, version, commit string) *healthChecker {
	hc := &healthChecker{
		startedAt: time.Now(),
		dbOpen:    dbOpen,
		getMeOK:   getMeOK,
		version:   versionInfoFromRuntime(version, commit),
	}
	return hc
}

// MarkUpdate records the timestamp of the latest received update. Called
// from the bot's incoming-update path.
func (hc *healthChecker) MarkUpdate(t time.Time) {
	hc.lastUpdate.Store(t.Unix())
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

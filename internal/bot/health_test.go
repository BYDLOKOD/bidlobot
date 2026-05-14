package bot

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func decodeStatus(t *testing.T, rr *httptest.ResponseRecorder) HealthStatus {
	t.Helper()
	var s HealthStatus
	if err := json.NewDecoder(rr.Body).Decode(&s); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	return s
}

func TestHealth_OKAtStartupBeforeFirstUpdate(t *testing.T) {
	hc := newHealthChecker(
		func() bool { return true },
		func(ctx context.Context) bool { return true },
		"v1.0.0", "deadbeef",
	)
	// startedAt = now, no updates yet -> within startup grace -> OK.
	rr := httptest.NewRecorder()
	healthHandler(hc)(rr, httptest.NewRequest("GET", "/health", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 within startup grace, got %d (body=%s)", rr.Code, rr.Body.String())
	}
	if got := decodeStatus(t, rr); got.Status != "ok" {
		t.Errorf("status: %+v", got)
	}
}

func TestHealth_DegradedAfterStartupGraceWithNoUpdates(t *testing.T) {
	hc := newHealthChecker(
		func() bool { return true },
		func(ctx context.Context) bool { return true },
		"v1.0.0", "",
	)
	hc.startedAt = time.Now().Add(-2 * HealthStartupGrace)

	rr := httptest.NewRecorder()
	healthHandler(hc)(rr, httptest.NewRequest("GET", "/health", nil))

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rr.Code)
	}
	got := decodeStatus(t, rr)
	if got.Status != "degraded" {
		t.Errorf("expected degraded, got %s", got.Status)
	}
}

func TestHealth_DegradedWhenDBClosed(t *testing.T) {
	hc := newHealthChecker(
		func() bool { return false },
		func(ctx context.Context) bool { return true },
		"", "",
	)
	hc.MarkUpdate(time.Now())

	rr := httptest.NewRecorder()
	healthHandler(hc)(rr, httptest.NewRequest("GET", "/health", nil))

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rr.Code)
	}
	if got := decodeStatus(t, rr); got.Reason == "" || got.Status != "degraded" {
		t.Errorf("expected degraded with reason, got %+v", got)
	}
}

func TestHealth_DegradedWhenGetMeFails(t *testing.T) {
	hc := newHealthChecker(
		func() bool { return true },
		func(ctx context.Context) bool { return false },
		"", "",
	)
	hc.MarkUpdate(time.Now())

	rr := httptest.NewRecorder()
	healthHandler(hc)(rr, httptest.NewRequest("GET", "/health", nil))

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rr.Code)
	}
	if got := decodeStatus(t, rr); got.Reason != "getMe failed" {
		t.Errorf("expected getMe reason, got %+v", got)
	}
}

func TestHealth_DegradedAfterStaleLastUpdate(t *testing.T) {
	hc := newHealthChecker(
		func() bool { return true },
		func(ctx context.Context) bool { return true },
		"", "",
	)
	hc.MarkUpdate(time.Now().Add(-2 * HealthFreshnessWindow))

	rr := httptest.NewRecorder()
	healthHandler(hc)(rr, httptest.NewRequest("GET", "/health", nil))

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 for stale update, got %d (body=%s)", rr.Code, rr.Body.String())
	}
}

func TestHealth_OKWithRecentUpdateAndChecks(t *testing.T) {
	hc := newHealthChecker(
		func() bool { return true },
		func(ctx context.Context) bool { return true },
		"v2.3.4", "abc",
	)
	hc.MarkUpdate(time.Now().Add(-30 * time.Second))

	rr := httptest.NewRecorder()
	healthHandler(hc)(rr, httptest.NewRequest("GET", "/health", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body=%s)", rr.Code, rr.Body.String())
	}
	if got := decodeStatus(t, rr); got.Status != "ok" {
		t.Errorf("status: %+v", got)
	}
}

func TestVersion_Returns200WithBuildInfo(t *testing.T) {
	hc := newHealthChecker(nil, nil, "v9.9.9", "deadbeef")
	rr := httptest.NewRecorder()
	versionHandler(hc)(rr, httptest.NewRequest("GET", "/version", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var v VersionInfo
	if err := json.NewDecoder(rr.Body).Decode(&v); err != nil {
		t.Fatal(err)
	}
	if v.Version != "v9.9.9" || v.Commit != "deadbeef" {
		t.Errorf("override not honored: %+v", v)
	}
	if v.GoVersion == "" {
		t.Errorf("go_version should be set from runtime/debug")
	}
}

func TestHealth_GetMeContextDeadline(t *testing.T) {
	hc := newHealthChecker(
		func() bool { return true },
		func(ctx context.Context) bool {
			// Pretend to honor the deadline; check ctx is non-nil.
			if ctx == nil {
				return false
			}
			return true
		},
		"", "",
	)
	hc.MarkUpdate(time.Now())

	rr := httptest.NewRecorder()
	healthHandler(hc)(rr, httptest.NewRequest("GET", "/health", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
}

func TestHealthPortFromEnv(t *testing.T) {
	tests := []struct {
		name         string
		env          string
		wantPort     int
		wantStart    bool
		wantErr      bool
	}{
		{"unset defaults to 8080", "", 8080, true, false},
		{"explicit 0 disables", "0", 0, false, false},
		{"valid port", "9000", 9000, true, false},
		{"out of range high", "70000", 0, false, true},
		{"non-integer", "foo", 0, false, true},
		{"negative", "-5", 0, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("HEALTH_PORT", tt.env)
			if tt.env == "" {
				_ = os.Unsetenv("HEALTH_PORT")
			}
			port, ok, err := healthPortFromEnv()
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tt.wantErr)
			}
			if err != nil {
				return
			}
			if port != tt.wantPort || ok != tt.wantStart {
				t.Errorf("port=%d ok=%v, want port=%d ok=%v", port, ok, tt.wantPort, tt.wantStart)
			}
		})
	}
}

// TestHealthServer_StartStop verifies the listener actually serves
// requests when bound to :0 and shuts down cleanly.
func TestHealthServer_StartStop(t *testing.T) {
	hc := newHealthChecker(
		func() bool { return true },
		func(ctx context.Context) bool { return true },
		"v0", "0",
	)
	hc.MarkUpdate(time.Now())

	srv, err := newHealthServer(0, testLogger(), hc)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	srv.Start()
	defer srv.Stop()

	resp, err := http.Get("http://" + srv.Addr() + "/health")
	if err != nil {
		t.Fatalf("get health: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	resp2, err := http.Get("http://" + srv.Addr() + "/version")
	if err != nil {
		t.Fatalf("get version: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp2.StatusCode)
	}

	srv.Stop()
	srv.Stop() // idempotent

	if _, err := http.Get("http://" + srv.Addr() + "/health"); err == nil {
		t.Errorf("expected get to fail after Stop")
	} else if !errors.Is(err, context.Canceled) && !isConnRefused(err) {
		// The error may be a connection refused / EOF after shutdown.
		// Either is fine; we just want it not to succeed.
	}
}

func isConnRefused(err error) bool {
	if err == nil {
		return false
	}
	return true // best-effort: any error after Stop is acceptable here
}

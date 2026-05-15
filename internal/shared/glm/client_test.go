package glm

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// noSleep makes the retry backoff instantaneous so retry paths are
// deterministic and fast in unit tests.
func noSleep(_ context.Context, _ time.Duration) error { return nil }

func testClient(t *testing.T, url string) *Client {
	t.Helper()
	c, err := New(Config{APIKey: "id.secret", BaseURL: url, Model: "glm-test", Sleep: noSleep})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestComplete_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer id.secret" {
			t.Errorf("auth header = %q, want Bearer id.secret", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":" итог "}}],"usage":{"prompt_tokens":11,"completion_tokens":3,"total_tokens":14}}`))
	}))
	defer srv.Close()

	text, usage, err := testClient(t, srv.URL).Complete(context.Background(), []Message{{Role: "user", Content: "hi"}}, 64)
	if err != nil {
		t.Fatalf("Complete err = %v", err)
	}
	if text != "итог" { // trimmed
		t.Fatalf("text = %q, want trimmed 'итог'", text)
	}
	if usage.TotalTokens != 14 {
		t.Fatalf("usage = %+v, want total 14", usage)
	}
}

func TestComplete_ErrorMapping(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"auth", http.StatusUnauthorized, `{"error":{"code":"401","message":"invalid api key"}}`, ErrAuth},
		{"forbidden", http.StatusForbidden, `{"error":{"message":"forbidden"}}`, ErrAuth},
		{"ctx_too_long", http.StatusBadRequest, `{"error":{"code":"1305","message":"maximum context length exceeded"}}`, ErrContextTooLong},
		{"bad_request_other", http.StatusBadRequest, `{"error":{"message":"unknown model foo"}}`, ErrProvider},
		{"empty_choices", http.StatusOK, `{"choices":[]}`, ErrEmpty},
		{"quota_429_code_1113", http.StatusTooManyRequests, `{"error":{"code":"1113","message":"余额不足或无可用资源包,请充值。"}}`, ErrQuota},
		{"quota_403_english", http.StatusForbidden, `{"error":{"message":"insufficient balance, please recharge"}}`, ErrQuota},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			_, _, err := testClient(t, srv.URL).Complete(context.Background(), []Message{{Role: "user", Content: "x"}}, 16)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want Is(%v)", err, tc.want)
			}
		})
	}
}

func TestComplete_RetryThenSuccess_429(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"rate"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer srv.Close()
	text, _, err := testClient(t, srv.URL).Complete(context.Background(), []Message{{Role: "user", Content: "x"}}, 8)
	if err != nil || text != "ok" {
		t.Fatalf("got (%q,%v), want ok/nil after one 429 retry", text, err)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls = %d, want 2", calls.Load())
	}
}

func TestComplete_429ExhaustedIsRateLimited(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"rate"}}`))
	}))
	defer srv.Close()
	_, _, err := testClient(t, srv.URL).Complete(context.Background(), []Message{{Role: "user", Content: "x"}}, 8)
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("err = %v, want ErrRateLimited", err)
	}
}

func TestComplete_5xxLadderThenProvider(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":{"message":"upstream"}}`))
	}))
	defer srv.Close()
	_, _, err := testClient(t, srv.URL).Complete(context.Background(), []Message{{Role: "user", Content: "x"}}, 8)
	if !errors.Is(err, ErrProvider) {
		t.Fatalf("err = %v, want ErrProvider", err)
	}
	if calls.Load() != 3 { // 1 + max5xxRetries(2)
		t.Fatalf("calls = %d, want 3", calls.Load())
	}
}

func TestComplete_5xxThenRecovers(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"recovered"}}]}`))
	}))
	defer srv.Close()
	text, _, err := testClient(t, srv.URL).Complete(context.Background(), []Message{{Role: "user", Content: "x"}}, 8)
	if err != nil || text != "recovered" {
		t.Fatalf("got (%q,%v), want recovered/nil", text, err)
	}
}

func TestComplete_TransportErrorIsProvider(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close() // now unreachable -> dial error, ctx not done
	_, _, err := testClient(t, url).Complete(context.Background(), []Message{{Role: "user", Content: "x"}}, 8)
	if !errors.Is(err, ErrProvider) {
		t.Fatalf("err = %v, want ErrProvider", err)
	}
}

func TestComplete_CanceledCtxIsTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(50 * time.Millisecond)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"late"}}]}`))
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	_, _, err := testClient(t, srv.URL).Complete(ctx, []Message{{Role: "user", Content: "x"}}, 8)
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v, want ErrTimeout", err)
	}
}

func TestNew_RejectsEmptyKey(t *testing.T) {
	if _, err := New(Config{APIKey: "   "}); err == nil {
		t.Fatalf("expected error for blank api key")
	}
}

// TestSmokeLive hits the real provider. Skipped unless RUN_GLM_SMOKE=1
// AND GLM_API_KEY is set, so `go test ./...` never spends money or
// touches the network. It also discovers which model id the key's
// account can actually call.
func TestSmokeLive(t *testing.T) {
	if os.Getenv("RUN_GLM_SMOKE") != "1" {
		t.Skip("set RUN_GLM_SMOKE=1 (and GLM_API_KEY) to run the live smoke")
	}
	key := strings.TrimSpace(os.Getenv("GLM_API_KEY"))
	if key == "" {
		t.Skip("GLM_API_KEY not set")
	}
	base := strings.TrimSpace(os.Getenv("GLM_BASE_URL"))
	candidates := []string{strings.TrimSpace(os.Getenv("GLM_MODEL")), "glm-5", "glm-5.1", "glm-4.6"}
	var lastErr error
	for _, m := range candidates {
		if m == "" {
			continue
		}
		c, err := New(Config{APIKey: key, BaseURL: base, Model: m})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		text, usage, cerr := c.Complete(ctx, []Message{
			{Role: "user", Content: "Reply with the single word: pong"},
		}, 16)
		cancel()
		if cerr == nil {
			t.Logf("LIVE OK model=%q reply=%q usage=%+v", m, text, usage)
			return
		}
		lastErr = cerr
		t.Logf("model %q failed: %v", m, cerr)
	}
	t.Fatalf("no candidate model succeeded; last error: %v", lastErr)
}

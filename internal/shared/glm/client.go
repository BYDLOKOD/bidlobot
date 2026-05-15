// Package glm is a minimal client for the Zhipu BigModel
// (open.bigmodel.cn) OpenAI-compatible chat-completions endpoint, used
// only for admin-triggered chat summarization.
//
// Auth is a plain Bearer of the "{id}.{secret}" API key - the modern
// bigmodel.cn v4 surface accepts the raw key directly, no JWT signing
// (verified against docs.bigmodel.cn, May 2026). The key is held in
// memory only and is never logged, never embedded in an error, never
// written to disk by this package.
//
// Retry policy is deliberately tighter than the Telegram one: a
// summarization request can be ~150K tokens of input, so each attempt is
// expensive in both latency and the operator's money. We retry a 429
// once (honoring Retry-After) and a 5xx on a short bounded ladder; every
// other status fails fast with a typed error the bot layer maps to a
// Russian user message. retry.Do is NOT reused here because it only
// classifies *telegoapi.Error; the pure backoff primitives are.
package glm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/veschin/bidlobot/internal/shared/retry"
)

// DefaultBaseURL is the general pay-as-you-go OpenAI-compatible root
// (no trailing slash) - the right fallback for a standard API key.
//
// A GLM *Coding Plan* subscription key does NOT work here: the general
// endpoint has no resource package for it and returns provider code
// 1113. Such keys must override GLM_BASE_URL with the coding endpoint
// "https://api.z.ai/api/coding/paas/v4" (verified live this session
// with glm-4.6). z.ai documents the coding endpoint as intended for
// supported coding tools, so using it for an arbitrary bot is outside
// its stated use - that is an operator/ToS decision, hence configurable
// rather than hard-coded here.
const DefaultBaseURL = "https://open.bigmodel.cn/api/paas/v4"

// DefaultModel is used when GLM_MODEL is unset. Configurable because
// Zhipu rotates flagship ids and the coding plan exposes a different
// set (glm-4.6 / glm-4.7 / glm-5.1 / glm-4.5-air); glm-4.6 is the one
// smoke-verified live this session.
const DefaultModel = "glm-5"

// Typed errors. The bot layer matches these with errors.Is to choose the
// Russian message it edits into the placeholder; callers must not show
// the wrapped detail to users (it may name the provider/internal state).
var (
	ErrAuth        = errors.New("glm: authentication failed")
	ErrRateLimited = errors.New("glm: rate limited")
	// ErrQuota is the account-out-of-funds / no-resource-package state.
	// bigmodel.cn returns it as an HTTP 429 with provider code 1113, so
	// it is indistinguishable from real throttling by status alone -
	// retrying or telling the admin "try later" would both be wrong.
	// This is terminal and actionable: the operator must top up.
	ErrQuota          = errors.New("glm: insufficient balance / no resource package")
	ErrContextTooLong = errors.New("glm: input context too long")
	ErrProvider       = errors.New("glm: provider error")
	ErrTimeout        = errors.New("glm: request timed out")
	ErrEmpty          = errors.New("glm: empty completion")
)

// Message is one OpenAI-style chat message.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Usage is the token accounting echoed by the provider; logged (never
// the key) so the operator can see real cost per call.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// Config bundles construction inputs. Only APIKey is required.
type Config struct {
	APIKey  string
	BaseURL string       // optional; default DefaultBaseURL
	Model   string       // optional; default DefaultModel
	HTTP    *http.Client // optional; a sane default is built if nil
	Logger  *slog.Logger // optional; slog.Default if nil
	// Sleep is the retry backoff wait; injectable so tests run without
	// real wall-clock delays. Defaults to retry.DefaultSleep (ctx-aware).
	Sleep func(ctx context.Context, d time.Duration) error
}

// Client talks to one bigmodel.cn-compatible endpoint with one key.
type Client struct {
	apiKey  string
	baseURL string
	model   string
	http    *http.Client
	log     *slog.Logger
	sleep   func(ctx context.Context, d time.Duration) error
}

// New validates cfg and returns a Client. The key is trimmed; an
// all-blank key is rejected up front so the feature fails loudly at
// wiring time rather than on the first admin invocation.
func New(cfg Config) (*Client, error) {
	key := strings.TrimSpace(cfg.APIKey)
	if key == "" {
		return nil, errors.New("glm: empty api key")
	}
	base := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if base == "" {
		base = DefaultBaseURL
	}
	model := strings.TrimSpace(cfg.Model)
	if model == "" {
		model = DefaultModel
	}
	hc := cfg.HTTP
	if hc == nil {
		// No client-level timeout: the per-call ctx deadline owns the
		// budget (a 150K-token request legitimately runs for minutes).
		// We still cap the connection/handshake so a black-holed host
		// fails fast instead of hanging until the ctx deadline.
		hc = &http.Client{
			Transport: &http.Transport{
				DialContext:           (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
				TLSHandshakeTimeout:   10 * time.Second,
				ResponseHeaderTimeout: 0, // governed by ctx
				MaxIdleConns:          4,
				IdleConnTimeout:       90 * time.Second,
			},
		}
	}
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	sl := cfg.Sleep
	if sl == nil {
		sl = retry.DefaultSleep
	}
	return &Client{apiKey: key, baseURL: base, model: model, http: hc, log: log, sleep: sl}, nil
}

// Model returns the configured model id (for logging/observability).
func (c *Client) Model() string { return c.model }

type completionRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Temperature float64   `json:"temperature"`
	MaxTokens   int       `json:"max_tokens"`
	Stream      bool      `json:"stream"`
}

type completionResponse struct {
	Choices []struct {
		Message      Message `json:"message"`
		FinishReason string  `json:"finish_reason"`
	} `json:"choices"`
	Usage Usage `json:"usage"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// Complete sends messages and returns the assistant text. The caller
// owns the deadline via ctx; maxTokens caps the completion length.
//
// Retry: one extra attempt on 429 (Retry-After honored, capped), a
// bounded 5xx ladder; classification is on HTTP status, not body
// guesswork, except a 400 whose message names a length/token/context
// problem is surfaced as ErrContextTooLong so the bot can tell the admin
// to lower N. The key never appears in any returned error.
func (c *Client) Complete(ctx context.Context, messages []Message, maxTokens int) (string, Usage, error) {
	if len(messages) == 0 {
		return "", Usage{}, errors.New("glm: no messages")
	}
	if maxTokens <= 0 {
		maxTokens = 2048
	}
	body, err := json.Marshal(completionRequest{
		Model:       c.model,
		Messages:    messages,
		Temperature: 0.3,
		MaxTokens:   maxTokens,
		Stream:      false,
	})
	if err != nil {
		return "", Usage{}, fmt.Errorf("glm: marshal request: %w", err)
	}

	const (
		max429Retries = 1
		max5xxRetries = 2
	)
	var (
		retried429 int
		retried5xx int
	)
	for {
		if cerr := ctx.Err(); cerr != nil {
			return "", Usage{}, ErrTimeout
		}
		text, usage, status, retryAfter, callErr := c.do(ctx, body)
		if callErr == nil {
			if strings.TrimSpace(text) == "" {
				return "", usage, ErrEmpty
			}
			return text, usage, nil
		}

		// A billing/quota failure can arrive under 429 (code 1113), 403,
		// or even 200-with-error depending on the gateway. Classify it
		// before the status switch so it is never retried and never
		// mis-reported as transient throttling.
		if isQuotaError(callErr) {
			return "", Usage{}, ErrQuota
		}

		switch {
		case errors.Is(callErr, errTransport):
			// Connection/read failure or ctx deadline. Distinguish a
			// genuine timeout (so the user gets "lower N") from a
			// generic network blip (provider error, not retried here:
			// a partial multi-minute attempt is too costly to repeat
			// blindly).
			if ctx.Err() != nil {
				return "", Usage{}, ErrTimeout
			}
			var ne net.Error
			if errors.As(callErr, &ne) && ne.Timeout() {
				return "", Usage{}, ErrTimeout
			}
			c.log.Warn("glm transport error", "model", c.model, "error", callErr.Error())
			return "", Usage{}, ErrProvider

		case status == http.StatusUnauthorized || status == http.StatusForbidden:
			return "", Usage{}, ErrAuth

		case status == http.StatusTooManyRequests:
			if retried429 >= max429Retries {
				return "", Usage{}, ErrRateLimited
			}
			retried429++
			delay := retryAfter
			if delay <= 0 {
				delay = 3 * time.Second
			}
			if delay > 30*time.Second {
				delay = 30 * time.Second
			}
			if werr := c.sleep(ctx, retry.DefaultJitter(delay)); werr != nil {
				return "", Usage{}, ErrTimeout
			}

		case status == http.StatusBadRequest:
			// 400 is terminal. Only a length/token/context message means
			// "shrink and retry on the user's side"; anything else is a
			// provider/request fault we cannot fix by retrying.
			if isContextLengthError(callErr) {
				return "", Usage{}, ErrContextTooLong
			}
			c.log.Warn("glm bad request", "model", c.model, "detail", callErr.Error())
			return "", Usage{}, ErrProvider

		case status >= 500 && status <= 599:
			if retried5xx >= max5xxRetries {
				return "", Usage{}, ErrProvider
			}
			retried5xx++
			if werr := c.sleep(ctx, retry.DefaultJitter(retry.ServerBackoff(retried5xx))); werr != nil {
				return "", Usage{}, ErrTimeout
			}

		default:
			c.log.Warn("glm unexpected status", "model", c.model, "status", status)
			return "", Usage{}, ErrProvider
		}
	}
}

// errTransport marks a pre-HTTP failure (dial/read/marshal) so Complete
// can branch on it without string matching.
var errTransport = errors.New("glm: transport")

// do performs exactly one HTTP attempt. It returns the parsed text and
// usage on 2xx, or (status, retryAfter, err) describing the failure. The
// returned err for an HTTP error wraps a sanitized provider message
// (never the request, never the key).
func (c *Client) do(ctx context.Context, body []byte) (text string, usage Usage, status int, retryAfter time.Duration, err error) {
	req, rerr := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
	if rerr != nil {
		return "", Usage{}, 0, 0, fmt.Errorf("%w: build request: %v", errTransport, rerr)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, derr := c.http.Do(req)
	if derr != nil {
		// derr can embed the URL but never our Authorization header.
		return "", Usage{}, 0, 0, fmt.Errorf("%w: %w", errTransport, derr)
	}
	defer resp.Body.Close()

	// Cap the body we read: a healthy completion is small; a runaway or
	// HTML error page should not be slurped unbounded.
	raw, berr := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if berr != nil {
		return "", Usage{}, resp.StatusCode, 0, fmt.Errorf("%w: read body: %v", errTransport, berr)
	}

	if resp.StatusCode == http.StatusTooManyRequests {
		retryAfter = parseRetryAfter(resp.Header.Get("Retry-After"))
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := extractProviderMessage(raw)
		c.log.Warn("glm http error",
			"model", c.model, "status", resp.StatusCode, "detail", msg)
		return "", Usage{}, resp.StatusCode, retryAfter,
			fmt.Errorf("glm: http %d: %s", resp.StatusCode, msg)
	}

	var parsed completionResponse
	if jerr := json.Unmarshal(raw, &parsed); jerr != nil {
		return "", Usage{}, resp.StatusCode, 0, fmt.Errorf("%w: decode response: %v", errTransport, jerr)
	}
	if parsed.Error != nil && parsed.Error.Message != "" {
		// Some gateways return 200 with an embedded error object.
		return "", parsed.Usage, resp.StatusCode, 0,
			fmt.Errorf("glm: provider error: %s", parsed.Error.Message)
	}
	if len(parsed.Choices) == 0 {
		return "", parsed.Usage, resp.StatusCode, 0, nil // -> ErrEmpty upstream
	}
	return strings.TrimSpace(parsed.Choices[0].Message.Content), parsed.Usage, resp.StatusCode, 0, nil
}

// isContextLengthError is a best-effort sniff of a 400 detail string for
// the provider's various ways of saying "too many input tokens".
func isContextLengthError(err error) bool {
	s := strings.ToLower(err.Error())
	for _, needle := range []string{"maximum context", "context length", "context window", "too long", "max_tokens", "exceeds the model", "1305"} {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}

// isQuotaError sniffs a failed-attempt error for the provider's
// out-of-funds signals. Matched on the bigmodel.cn numeric code and the
// human strings it uses (Chinese and English), so it survives a code
// renumber or an endpoint that phrases it differently.
func isQuotaError(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	for _, needle := range []string{
		"1113", // bigmodel.cn provider code
		"余额",   // "balance"
		"资源包",  // "resource package"
		"欠费",   // "in arrears"
		"recharge", "insufficient balance", "no available resource",
		"quota", "out of credit", "billing",
	} {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}

// extractProviderMessage pulls a short human message out of an error
// body without ever reflecting our request. Falls back to a truncated
// raw snippet when the body is not the expected JSON shape.
func extractProviderMessage(raw []byte) string {
	var env struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &env) == nil {
		if env.Error.Message != "" {
			if env.Error.Code != "" {
				return env.Error.Code + ": " + env.Error.Message
			}
			return env.Error.Message
		}
		if env.Message != "" {
			return env.Message
		}
	}
	s := strings.TrimSpace(string(raw))
	if len(s) > 240 {
		s = s[:240] + "..."
	}
	if s == "" {
		s = "(empty body)"
	}
	return s
}

// parseRetryAfter accepts the integer-seconds form Zhipu uses; an HTTP
// date form or garbage yields 0 (caller applies a default).
func parseRetryAfter(v string) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

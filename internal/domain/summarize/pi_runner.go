package summarize

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"golang.org/x/sys/unix"
)

// Typed errors the caller's error-mapping layer can match with errors.Is.
// ErrProviderFailure wraps any exec failure: binary not found, nonzero
// exit, empty stdout. ErrTimeout wraps a context deadline or cancellation
// that terminated the process.
var (
	ErrProviderFailure = errors.New("summarize: provider failure")
	ErrTimeout         = errors.New("summarize: timeout")
)

// Runner is the command-execution seam. The real implementation uses
// os/exec; tests inject a fake that captures args and returns canned
// output.
type Runner interface {
	Run(ctx context.Context, binary string, args []string, stdin string) (string, error)
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, binary string, args []string, stdin string) (string, error) {
	fd, err := unix.MemfdCreate("bidlobot-summarize", unix.MFD_CLOEXEC)
	if err != nil {
		return "", err
	}
	prompt := os.NewFile(uintptr(fd), "bidlobot-summarize")
	defer prompt.Close()
	if _, err := prompt.WriteString(stdin); err != nil {
		return "", err
	}
	if _, err := prompt.Seek(0, 0); err != nil {
		return "", err
	}

	args = append(args, "@/proc/self/fd/3")
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.ExtraFiles = []*os.File{prompt}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err = cmd.Run()
	if err != nil {
		// Never leak stderr, argv, or credentials in the error.
		return "", err
	}

	out := strings.TrimSpace(stdout.String())
	if out == "" {
		return "", errors.New("empty output")
	}
	return out, nil
}

// Completion is the public result of one OMP turn. CostUSD comes from the
// provider-reported token usage and OMP's model-price metadata.
type Completion struct {
	Text    string
	CostUSD float64
}

type ompJSONEvent struct {
	Type    string          `json:"type"`
	Message *ompJSONMessage `json:"message"`
}

type ompJSONMessage struct {
	Role    string           `json:"role"`
	Content []ompJSONContent `json:"content"`
	Usage   *ompJSONUsage    `json:"usage"`
}

type ompJSONContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type ompJSONUsage struct {
	Cost *ompJSONCost `json:"cost"`
}

type ompJSONCost struct {
	Total *float64 `json:"total"`
}

// PiRunner wraps an injectable command runner with the OMP/Pi binary and
// model selector. Call Complete to run summarization.
type PiRunner struct {
	binary string
	model  string
	runner Runner
}

// NewPiRunner creates a PiRunner. binary is the OMP/Pi executable path
// (default "omp"); model is the fully qualified model selector (e.g.
// "deepseek/deepseek-v4-flash").
func NewPiRunner(binary, model string) *PiRunner {
	return &PiRunner{
		binary: binary,
		model:  model,
		runner: execRunner{},
	}
}

// Complete invokes the Pi runner: the transcript is exposed to OMP as an
// anonymous in-memory file, the system instruction through --system-prompt,
// and the model through --model. JSON mode supplies both the final text and
// provider-reported cost. Returns ErrTimeout when the context is done, or
// ErrProviderFailure wrapped on any runner or output-contract error.
func (r *PiRunner) Complete(ctx context.Context, systemPrompt, transcript string) (Completion, error) {
	args := []string{
		"--mode", "json",
		"--no-session", "--no-tools", "--no-lsp",
		"--no-extensions", "--no-skills", "--no-rules",
		"--thinking=minimal", "-p",
		"--system-prompt", systemPrompt,
		"--model", r.model,
	}

	out, err := r.runner.Run(ctx, r.binary, args, transcript)
	if err != nil {
		if ctx.Err() != nil {
			return Completion{}, ErrTimeout
		}
		return Completion{}, fmt.Errorf("%w: %v", ErrProviderFailure, err)
	}
	completion, err := parseOMPJSON(out)
	if err != nil {
		return Completion{}, fmt.Errorf("%w: invalid OMP JSON output", ErrProviderFailure)
	}
	return completion, nil
}

func parseOMPJSON(out string) (Completion, error) {
	dec := json.NewDecoder(strings.NewReader(out))
	var completion Completion
	found := false
	for {
		var event ompJSONEvent
		if err := dec.Decode(&event); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return Completion{}, err
		}
		if event.Type != "message_end" || event.Message == nil || event.Message.Role != "assistant" {
			continue
		}

		var text strings.Builder
		for _, content := range event.Message.Content {
			if content.Type == "text" {
				text.WriteString(content.Text)
			}
		}
		if strings.TrimSpace(text.String()) == "" ||
			event.Message.Usage == nil ||
			event.Message.Usage.Cost == nil ||
			event.Message.Usage.Cost.Total == nil {
			continue
		}
		completion = Completion{
			Text:    strings.TrimSpace(text.String()),
			CostUSD: *event.Message.Usage.Cost.Total,
		}
		found = true
	}
	if !found {
		return Completion{}, errors.New("missing assistant completion or usage")
	}
	return completion, nil
}

package summarize

import (
	"bufio"
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

// Runner is the command-execution seam. The real implementation streams
// OMP's JSONL output into a final Completion; tests inject a fake that
// captures args and returns a canned result.
type Runner interface {
	Run(ctx context.Context, binary string, args []string, stdin string) (Completion, error)
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, binary string, args []string, stdin string) (Completion, error) {
	fd, err := unix.MemfdCreate("bidlobot-summarize", unix.MFD_CLOEXEC)
	if err != nil {
		return Completion{}, err
	}
	prompt := os.NewFile(uintptr(fd), "bidlobot-summarize")
	defer prompt.Close()
	if _, err := prompt.WriteString(stdin); err != nil {
		return Completion{}, err
	}
	if _, err := prompt.Seek(0, 0); err != nil {
		return Completion{}, err
	}

	args = append(args, "@/proc/self/fd/3")
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.ExtraFiles = []*os.File{prompt}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Completion{}, err
	}
	// OMP diagnostics are intentionally never exposed: they may contain
	// provider details, and retaining an unbounded stderr was pure memory
	// overhead on the only path where the caller discards it.
	cmd.Stderr = io.Discard

	if err := cmd.Start(); err != nil {
		return Completion{}, err
	}
	completion, parseErr := parseOMPJSON(stdout)
	if parseErr != nil {
		// Stop a malformed or oversized producer immediately. Waiting
		// without killing could deadlock once its stdout pipe fills.
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return Completion{}, parseErr
	}
	if err := cmd.Wait(); err != nil {
		return Completion{}, err
	}
	return completion, nil
}

// Completion is the public result of one OMP turn. CostUSD comes from the
// provider-reported token usage and OMP's model-price metadata.
type Completion struct {
	Text    string
	CostUSD float64
}

// maxOMPJSONEventBytes is a hard per-event ceiling. OMP emits one JSON
// object per line; a valid tool-free summary event is tiny compared with
// this limit. Bounding the scanner prevents one malformed or runaway event
// from replacing the old whole-stream OOM with a single-event OOM.
const maxOMPJSONEventBytes = 4 << 20

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

	completion, err := r.runner.Run(ctx, r.binary, args, transcript)
	if err != nil {
		if ctx.Err() != nil {
			return Completion{}, ErrTimeout
		}
		return Completion{}, fmt.Errorf("%w: %v", ErrProviderFailure, err)
	}
	return completion, nil
}

func parseOMPJSON(in io.Reader) (Completion, error) {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 64<<10), maxOMPJSONEventBytes)

	var completion Completion
	found := false
	for scanner.Scan() {
		line := scanner.Bytes()
		var header struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(line, &header); err != nil {
			return Completion{}, err
		}
		if header.Type != "message_end" {
			continue
		}

		var event ompJSONEvent
		if err := json.Unmarshal(line, &event); err != nil {
			return Completion{}, err
		}
		if event.Message == nil || event.Message.Role != "assistant" {
			continue
		}

		var text strings.Builder
		for _, content := range event.Message.Content {
			if content.Type == "text" {
				text.WriteString(content.Text)
			}
		}
		body := strings.TrimSpace(text.String())
		if body == "" ||
			event.Message.Usage == nil ||
			event.Message.Usage.Cost == nil ||
			event.Message.Usage.Cost.Total == nil {
			continue
		}
		completion = Completion{
			Text:    body,
			CostUSD: *event.Message.Usage.Cost.Total,
		}
		found = true
	}
	if err := scanner.Err(); err != nil {
		return Completion{}, err
	}
	if !found {
		return Completion{}, errors.New("missing assistant completion or usage")
	}
	return completion, nil
}

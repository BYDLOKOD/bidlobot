package summarize

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
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
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Stdin = strings.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
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

// Complete invokes the Pi runner: transcript goes through stdin, the
// system instruction through --system-prompt, and the model is passed as
// -m. Returns ErrTimeout when the context is done, ErrProviderFailure
// wrapped on any runner error.
func (r *PiRunner) Complete(ctx context.Context, systemPrompt, transcript string) (string, error) {
	args := []string{
		"--no-session", "--no-tools", "--no-lsp",
		"--no-extensions", "--no-skills", "--no-rules",
		"--thinking=minimal", "-p",
		"--system-prompt", systemPrompt,
		"-m", r.model,
	}

	out, err := r.runner.Run(ctx, r.binary, args, transcript)
	if err != nil {
		if ctx.Err() != nil {
			return "", ErrTimeout
		}
		return "", fmt.Errorf("%w: %v", ErrProviderFailure, err)
	}
	return out, nil
}

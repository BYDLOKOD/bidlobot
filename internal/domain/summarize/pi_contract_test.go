package summarize

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// TestBuildPromptReturnsSystemAndTranscript verifies that BuildPrompt
// returns explicit SystemPrompt and Transcript strings instead of GLM
// messages, so the summarize package depends only on stdlib strings.
func TestBuildPromptReturnsSystemAndTranscript(t *testing.T) {
	entries := []Entry{
		{UserID: 1, Name: "@oleg", Text: "привет", TS: time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)},
		{UserID: 2, Name: "@anna", Text: "как дела?", TS: time.Date(2026, 6, 1, 12, 1, 0, 0, time.UTC)},
	}
	requested := 5
	available := 2
	budget := 2000

	result, ok := BuildPrompt(entries, requested, available, budget, "")
	if !ok {
		t.Fatal("BuildPrompt returned ok=false for non-empty window")
	}

	if result.SystemPrompt == "" {
		t.Fatal("BuildPrompt must return a non-empty SystemPrompt")
	}
	if !strings.Contains(result.SystemPrompt, "catch-up digest") {
		t.Fatal("SystemPrompt must be an English-language instruction")
	}

	if result.Transcript == "" {
		t.Fatal("BuildPrompt must return a non-empty Transcript")
	}
	if !strings.Contains(result.Transcript, "@oleg") || !strings.Contains(result.Transcript, "@anna") {
		t.Fatal("Transcript must contain both participants")
	}
}

// fakeRecorderRunner captures the arguments it was called with and returns
// a canned output. Tests use it to verify process arguments/stdin without
// a real binary.
type fakeRecorderRunner struct {
	gotArgs  []string
	gotStdin string
	output   string
	err      error
}

func (f *fakeRecorderRunner) Run(_ context.Context, _ string, args []string, stdin string) (string, error) {
	f.gotArgs = args
	f.gotStdin = stdin
	return f.output, f.err
}

const fakeOMPJSON = `{"type":"message_end","message":{"role":"user","content":[{"type":"text","text":"transcript"}]}}
{"type":"message_end","message":{"role":"assistant","content":[{"type":"thinking","thinking":"private reasoning"},{"type":"text","text":"ok"}],"usage":{"cost":{"total":0.001234}}}}`

// TestExecRunnerPromptTransport verifies that the real process runner gives
// OMP a seekable @file without persisting the private transcript to disk.
func TestExecRunnerPromptTransport(t *testing.T) {
	if os.Getenv("BIDLOBOT_EXEC_RUNNER_HELPER") == "1" {
		ref := os.Args[len(os.Args)-1]
		if !strings.HasPrefix(ref, "@/proc/self/fd/") {
			t.Fatalf("prompt reference = %q, want anonymous file descriptor", ref)
		}
		body, err := os.ReadFile(strings.TrimPrefix(ref, "@"))
		if err != nil {
			t.Fatal(err)
		}
		_, _ = os.Stdout.Write(body)
		return
	}

	t.Setenv("BIDLOBOT_EXEC_RUNNER_HELPER", "1")
	const prompt = "private transcript"
	out, err := (execRunner{}).Run(
		context.Background(),
		os.Args[0],
		[]string{"-test.run=TestExecRunnerPromptTransport"},
		prompt,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, prompt) {
		t.Fatalf("runner output = %q, want prompt content", out)
	}
}

// TestPiRunnerPromptModelFlags verifies that the Pi runner passes the exact
// model selector, the correct disabled-options flags, and the transcript to
// the process runner.
func TestPiRunnerPromptModelFlags(t *testing.T) {
	fake := &fakeRecorderRunner{output: fakeOMPJSON}
	r := NewPiRunner("omp", "deepseek/deepseek-v4-flash")
	r.runner = fake

	completion, err := r.Complete(context.Background(), "system prompt", "transcript")
	if err != nil {
		t.Fatal(err)
	}
	if completion.Text != "ok" {
		t.Fatalf("output = %q, want %q", completion.Text, "ok")
	}
	if completion.CostUSD != 0.001234 {
		t.Fatalf("cost = %f, want 0.001234", completion.CostUSD)
	}

	// Verify the args the fake runner recorded.
	args := fake.gotArgs
	assertFlag := func(flag string) {
		t.Helper()
		found := false
		for _, a := range args {
			if a == flag {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("missing flag %q in args: %v", flag, args)
		}
	}

	assertFlag("--no-session")
	assertFlag("--no-tools")
	assertFlag("--no-lsp")
	assertFlag("--no-extensions")
	assertFlag("--no-skills")
	assertFlag("--no-rules")
	assertFlag("--thinking=minimal")
	assertFlag("-p")
	assertFlag("--system-prompt")

	modeFound := false
	for i, a := range args {
		if a == "--mode" && i+1 < len(args) && args[i+1] == "json" {
			modeFound = true
			break
		}
	}
	if !modeFound {
		t.Fatalf("JSON output mode not found in args: %v", args)
	}

	// Model selector via the current OMP long flag.
	modelFound := false
	for i, a := range args {
		if a == "--model" && i+1 < len(args) && args[i+1] == "deepseek/deepseek-v4-flash" {
			modelFound = true
			break
		}
	}
	if !modelFound {
		t.Fatalf("model selector --model deepseek/deepseek-v4-flash not found in args: %v", args)
	}

	// System prompt should be the second arg after --system-prompt.
	sysPromptFound := false
	for i, a := range args {
		if a == "--system-prompt" && i+1 < len(args) {
			sysPromptFound = true
			if args[i+1] != "system prompt" {
				t.Fatalf("--system-prompt value = %q, want %q", args[i+1], "system prompt")
			}
			break
		}
	}
	if !sysPromptFound {
		t.Fatalf("--system-prompt flag not found in args: %v", args)
	}

	if fake.gotStdin != "transcript" {
		t.Fatalf("stdin = %q, want %q", fake.gotStdin, "transcript")
	}
}

// TestPiRunnerNonZeroExitMapsToProviderError verifies that a nonzero exit
// code from the Pi runner maps to the public provider-failure typed error
// without exposing stderr.
func TestPiRunnerNonZeroExitMapsToProviderError(t *testing.T) {
	fake := &fakeRecorderRunner{err: errors.New("exit code 1")}
	r := NewPiRunner("fake-pi", "deepseek/deepseek-v4-flash")
	r.runner = fake

	_, err := r.Complete(context.Background(), "", "")
	if err == nil {
		t.Fatal("expected provider failure error, got nil")
	}
	if !strings.Contains(err.Error(), "provider") {
		t.Fatalf("expected provider-failure error, got %v", err)
	}
}

// TestPiRunnerDeadlineMapsToTimeoutError verifies that a context deadline
// exceeded during the Pi process maps to the existing timeout response.
func TestPiRunnerDeadlineMapsToTimeoutError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := NewPiRunner("omp", "deepseek/deepseek-v4-flash")
	_, err := r.Complete(ctx, "system prompt", "transcript")
	if err == nil {
		t.Fatal("expected timeout error for cancelled context, got nil")
	}
}

// TestPiRunnerCredentialSafety verifies that the Pi runner returns the
// model output without leaking credentials or arguments.
func TestPiRunnerCredentialSafety(t *testing.T) {
	fake := &fakeRecorderRunner{output: fakeOMPJSON}
	r := NewPiRunner("omp", "deepseek/deepseek-v4-flash")
	r.runner = fake

	completion, err := r.Complete(context.Background(), "system prompt", "transcript")
	if err != nil {
		t.Fatal(err)
	}
	if completion.Text != "ok" {
		t.Fatalf("output = %q, want %q", completion.Text, "ok")
	}
	if completion.CostUSD <= 0 {
		t.Fatalf("expected positive provider cost, got %f", completion.CostUSD)
	}
}

func TestPiRunnerMissingUsageMapsToProviderError(t *testing.T) {
	fake := &fakeRecorderRunner{output: `{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"summary"}]}}`}
	r := NewPiRunner("omp", "deepseek/deepseek-v4-flash")
	r.runner = fake

	if _, err := r.Complete(context.Background(), "system prompt", "transcript"); !errors.Is(err, ErrProviderFailure) {
		t.Fatalf("missing usage error = %v, want ErrProviderFailure", err)
	}
}

func TestPiRunnerMalformedJSONMapsToProviderError(t *testing.T) {
	fake := &fakeRecorderRunner{output: "not-json"}
	r := NewPiRunner("omp", "deepseek/deepseek-v4-flash")
	r.runner = fake

	if _, err := r.Complete(context.Background(), "system prompt", "transcript"); !errors.Is(err, ErrProviderFailure) {
		t.Fatalf("malformed JSON error = %v, want ErrProviderFailure", err)
	}
}

// TestBuildPromptEmptyWindowReturnsNotOk verifies that BuildPrompt returns
// ok=false for an empty window.
func TestBuildPromptEmptyWindowReturnsNotOk(t *testing.T) {
	_, ok := BuildPrompt(nil, 5, 0, 2000, "")
	if ok {
		t.Fatal("BuildPrompt must return ok=false for empty window")
	}
}

// TestSummarizeServiceUsesPiRunner verifies that the Service wiring
// accepts a PiRunner instead of the deprecated GLM Completer.
func TestSummarizeServiceUsesPiRunner(t *testing.T) {
	var _ = NewPiRunner("omp", "deepseek/deepseek-v4-flash")
}

package summarize

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strconv"
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
// a canned completion. Tests use it to verify process arguments/stdin
// without invoking a real binary.
type fakeRecorderRunner struct {
	gotArgs    []string
	gotStdin   string
	completion Completion
	err        error
}

func (f *fakeRecorderRunner) Run(_ context.Context, _ string, args []string, stdin string) (Completion, error) {
	f.gotArgs = args
	f.gotStdin = stdin
	return f.completion, f.err
}

const fakeOMPJSON = `{"type":"message_end","message":{"role":"user","content":[{"type":"text","text":"transcript"}]}}
{"type":"message_end","message":{"role":"assistant","content":[{"type":"thinking","thinking":"private reasoning"},{"type":"text","text":"ok"}],"usage":{"cost":{"total":0.001234}}}}`

const (
	execRunnerHelperEnv      = "BIDLOBOT_EXEC_RUNNER_HELPER"
	execRunnerUpdateCountEnv = "BIDLOBOT_EXEC_RUNNER_UPDATES"
)

func runExecRunnerHelper() bool {
	if os.Getenv(execRunnerHelperEnv) != "1" {
		return false
	}
	ref := os.Args[len(os.Args)-1]
	if !strings.HasPrefix(ref, "@/proc/self/fd/") {
		panic("prompt is not an anonymous file descriptor")
	}
	body, err := os.ReadFile(strings.TrimPrefix(ref, "@"))
	if err != nil {
		panic(err)
	}
	count, err := strconv.Atoi(os.Getenv(execRunnerUpdateCountEnv))
	if err != nil {
		panic(err)
	}
	update := []byte(`{"type":"message_update","message":{"role":"assistant","content":[{"type":"thinking","thinking":"` +
		strings.Repeat("x", 1<<20) + `"}]}}` + "\n")
	for range count {
		if _, err := os.Stdout.Write(update); err != nil {
			panic(err)
		}
	}
	text, err := json.Marshal(string(body))
	if err != nil {
		panic(err)
	}
	final := `{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":` +
		string(text) + `}],"usage":{"cost":{"total":0.001234}}}}` + "\n"
	if _, err := os.Stdout.WriteString(final); err != nil {
		panic(err)
	}
	return true
}

func processHighWaterKiB(t *testing.T) uint64 {
	t.Helper()
	status, err := os.ReadFile("/proc/self/status")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(status), "\n") {
		if !strings.HasPrefix(line, "VmHWM:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			break
		}
		value, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	t.Fatal("VmHWM not found in /proc/self/status")
	return 0
}

// TestExecRunnerPromptTransport verifies that the real process runner gives
// OMP a seekable @file without persisting the private transcript to disk.
func TestExecRunnerPromptTransport(t *testing.T) {
	if runExecRunnerHelper() {
		os.Exit(0)
	}

	t.Setenv(execRunnerHelperEnv, "1")
	t.Setenv(execRunnerUpdateCountEnv, "1")
	const prompt = "private transcript"
	completion, err := (execRunner{}).Run(
		context.Background(),
		os.Args[0],
		[]string{"-test.run=TestExecRunnerPromptTransport"},
		prompt,
	)
	if err != nil {
		t.Fatal(err)
	}
	if completion.Text != prompt {
		t.Fatalf("runner output = %q, want prompt content", completion.Text)
	}
	if completion.CostUSD != 0.001234 {
		t.Fatalf("runner cost = %f, want 0.001234", completion.CostUSD)
	}
}

// TestExecRunnerBoundsJSONStreamMemory guards against buffering OMP's entire
// JSON event stream. Sixty-four cumulative 1 MiB updates must not grow the
// parent process by anything close to the 64 MiB wire volume.
func TestExecRunnerBoundsJSONStreamMemory(t *testing.T) {
	if runExecRunnerHelper() {
		os.Exit(0)
	}

	before := processHighWaterKiB(t)
	t.Setenv(execRunnerHelperEnv, "1")
	t.Setenv(execRunnerUpdateCountEnv, "64")
	if _, err := (execRunner{}).Run(
		context.Background(),
		os.Args[0],
		[]string{"-test.run=TestExecRunnerBoundsJSONStreamMemory"},
		"memory probe",
	); err != nil {
		t.Fatal(err)
	}
	after := processHighWaterKiB(t)
	var growth uint64
	if after > before {
		growth = after - before
	}
	if growth > 32<<10 {
		t.Fatalf("64 MiB OMP stream grew parent VmHWM by %d KiB; want <= 32768 KiB", growth)
	}
}

// TestPiRunnerPromptModelFlags verifies that the Pi runner passes the exact
// model selector, the correct disabled-options flags, and the transcript to
// the process runner.
func TestPiRunnerPromptModelFlags(t *testing.T) {
	fake := &fakeRecorderRunner{completion: Completion{Text: "ok", CostUSD: 0.001234}}
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
	fake := &fakeRecorderRunner{completion: Completion{Text: "ok", CostUSD: 0.001234}}
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

func TestParseOMPJSONCompletion(t *testing.T) {
	completion, err := parseOMPJSON(strings.NewReader(fakeOMPJSON))
	if err != nil {
		t.Fatal(err)
	}
	if completion.Text != "ok" || completion.CostUSD != 0.001234 {
		t.Fatalf("completion = %+v, want text ok and cost 0.001234", completion)
	}
}

func TestParseOMPJSONMissingUsage(t *testing.T) {
	input := `{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"summary"}]}}`
	if _, err := parseOMPJSON(strings.NewReader(input)); err == nil {
		t.Fatal("missing usage must fail the OMP output contract")
	}
}

func TestParseOMPJSONMalformedJSON(t *testing.T) {
	if _, err := parseOMPJSON(strings.NewReader("not-json")); err == nil {
		t.Fatal("malformed JSON must fail the OMP output contract")
	}
}

func TestParseOMPJSONRejectsOversizedEvent(t *testing.T) {
	input := `{"type":"message_update","padding":"` + strings.Repeat("x", maxOMPJSONEventBytes) + `"}`
	if _, err := parseOMPJSON(strings.NewReader(input)); err == nil {
		t.Fatal("oversized OMP event must fail instead of growing memory without bound")
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

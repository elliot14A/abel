package run_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/elliot14A/abel/internal/core/run"
)

func TestLogTailKeepsTheMostRecentLines(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		capacity int
		write    int
		want     []string
		dropped  int
	}{
		"under capacity":      {5, 3, []string{"0", "1", "2"}, 0},
		"exactly at capacity": {3, 3, []string{"0", "1", "2"}, 0},
		"over capacity":       {3, 7, []string{"4", "5", "6"}, 4},
		"far over capacity":   {2, 1000, []string{"998", "999"}, 998},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			tail := run.NewLogTail(tc.capacity)
			for i := range tc.write {
				tail.Add(fmt.Sprint(i))
			}
			if diff := cmp.Diff(tc.want, tail.Lines()); diff != "" {
				t.Errorf("Lines() (-want +got):\n%s", diff)
			}
			if got := tail.Dropped(); got != tc.dropped {
				t.Errorf("Dropped() = %d, want %d", got, tc.dropped)
			}
		})
	}
}

func TestLogTailEmptyAndReset(t *testing.T) {
	t.Parallel()

	tail := run.NewLogTail(2)
	if got := tail.Lines(); len(got) != 0 {
		t.Errorf("fresh tail = %v, want empty", got)
	}

	for _, line := range []string{"a", "b", "c"} {
		tail.Add(line)
	}
	tail.Reset()
	if got := tail.Lines(); len(got) != 0 {
		t.Errorf("after Reset = %v, want empty", got)
	}
	if got := tail.Dropped(); got != 0 {
		t.Errorf("after Reset Dropped() = %d, want 0", got)
	}
}

func TestLogTailLinesIsACopy(t *testing.T) {
	t.Parallel()

	tail := run.NewLogTail(3)
	for _, line := range []string{"a", "b"} {
		tail.Add(line)
	}

	lines := tail.Lines()
	lines[0] = "mutated"

	if got := tail.Lines()[0]; got != "a" {
		t.Errorf("Lines() aliases internal storage: got %q after caller mutation", got)
	}
}

func TestIsSecretName(t *testing.T) {
	t.Parallel()

	secret := []string{"GITHUB_TOKEN", "npm_token", "AWS_SECRET_ACCESS_KEY", "DB_PASSWORD", "MyApiKey", "SESSION_ID", "AUTH_HEADER"}
	public := []string{"CI", "PATH", "NODE_ENV", "LOG_LEVEL", "HOME", "GOFLAGS"}

	for _, name := range secret {
		if !run.IsSecretName(name) {
			t.Errorf("IsSecretName(%q) = false, want true", name)
		}
	}
	for _, name := range public {
		if run.IsSecretName(name) {
			t.Errorf("IsSecretName(%q) = true, want false", name)
		}
	}
}

func TestFailureRedact(t *testing.T) {
	t.Parallel()

	f := run.Failure{
		Command: "curl -H \"Authorization: Bearer ghp_supersecretvalue\" https://api.example.com",
		Env: map[string]string{
			"GITHUB_TOKEN": "ghp_supersecretvalue",
			"NODE_ENV":     "test",
			"EMPTY_TOKEN":  "",
		},
		LogTail: []string{
			"curl: using ghp_supersecretvalue",
			"nothing sensitive here",
		},
	}

	got := f.Redact()

	if got.Env["GITHUB_TOKEN"] != run.Redacted {
		t.Errorf("secret env value = %q, want %q", got.Env["GITHUB_TOKEN"], run.Redacted)
	}
	if got.Env["NODE_ENV"] != "test" {
		t.Errorf("non-secret env value was altered: %q", got.Env["NODE_ENV"])
	}
	for _, line := range got.LogTail {
		if strings.Contains(line, "ghp_supersecretvalue") {
			t.Errorf("secret leaked into the log tail: %q", line)
		}
	}
	if strings.Contains(got.Command, "ghp_supersecretvalue") {
		t.Errorf("secret leaked into the command: %q", got.Command)
	}
	if got.LogTail[1] != "nothing sensitive here" {
		t.Errorf("unrelated log line was altered: %q", got.LogTail[1])
	}

	if f.Env["GITHUB_TOKEN"] != "ghp_supersecretvalue" {
		t.Error("Redact mutated the receiver")
	}
}

func TestFailureRedactHandlesOverlappingSecrets(t *testing.T) {
	t.Parallel()

	f := run.Failure{
		Env: map[string]string{
			"SHORT_TOKEN": "abc",
			"LONG_TOKEN":  "abcdef",
		},
		LogTail: []string{"value=abcdef"},
	}

	if got := f.Redact().LogTail[0]; got != "value="+run.Redacted {
		t.Errorf("LogTail[0] = %q, want %q", got, "value="+run.Redacted)
	}
}

func TestCaptureFailureCopiesCallerState(t *testing.T) {
	t.Parallel()

	plan := run.Plan{JobID: "lint", JobName: "Lint", Image: "alpine:3"}
	step := run.Step{
		Index: 2, Name: "lint", Script: "biome check .",
		Env: map[string]string{"CI": "true"}, WorkingDir: "/abel/workspace", SourceLine: 12,
	}
	tail := run.NewLogTail(2)
	tail.Add("line one")
	tail.Add("line two")
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)

	f := run.CaptureFailure(plan, step, 1, tail, now)

	tail.Add("mutated")
	step.Env["CI"] = "mutated"

	if f.LogTail[0] != "line one" {
		t.Errorf("LogTail follows the caller's tail after capture: %q", f.LogTail[0])
	}
	if f.Env["CI"] != "true" {
		t.Errorf("Env aliases the caller's map: %q", f.Env["CI"])
	}
	if f.StepIndex != 2 || f.ExitCode != 1 || f.JobID != "lint" || !f.CapturedAt.Equal(now) {
		t.Errorf("captured failure = %+v", *f)
	}
}

func TestResultSummary(t *testing.T) {
	t.Parallel()

	green := run.Result{
		JobID:    "lint",
		Duration: 1500 * time.Millisecond,
		Steps: []run.StepResult{
			{Skipped: true}, {ExitCode: 0}, {ExitCode: 0},
		},
	}
	if !green.OK() {
		t.Error("Result with no Failure reports not OK")
	}
	if got := green.Summary(); !strings.Contains(got, "passed") ||
		!strings.Contains(got, "2 step(s) run") || !strings.Contains(got, "1 skipped") {
		t.Errorf("Summary() = %q", got)
	}

	red := run.Result{
		JobID:    "lint",
		Duration: time.Second,
		Steps:    []run.StepResult{{ExitCode: 1}},
		Failure:  &run.Failure{StepIndex: 0, StepName: "typecheck"},
	}
	if red.OK() {
		t.Error("Result with a Failure reports OK")
	}

	if got := red.Summary(); !strings.Contains(got, "step 1 (typecheck)") {
		t.Errorf("Summary() = %q, want a 1-based step number", got)
	}
}

func TestPlanRunnable(t *testing.T) {
	t.Parallel()

	allSkipped := run.Plan{Steps: []run.Step{{Skip: true}, {Skip: true}}}
	if allSkipped.Runnable() {
		t.Error("a plan of only skipped steps reports Runnable")
	}
	mixed := run.Plan{Steps: []run.Step{{Skip: true}, {}}}
	if !mixed.Runnable() {
		t.Error("a plan with one live step reports not Runnable")
	}
}

func TestRedactScrubsTheStepNameToo(t *testing.T) {
	t.Parallel()

	f := run.Failure{
		StepName: `curl -H "Authorization: super-secret-value"`,
		Command:  `curl -H "Authorization: super-secret-value"`,
		LogTail:  []string{"sent super-secret-value"},
		Env:      map[string]string{"API_TOKEN": "super-secret-value", "GREETING": "hello"},
	}

	got := f.Redact()
	for name, field := range map[string]string{
		"StepName": got.StepName,
		"Command":  got.Command,
		"LogTail":  strings.Join(got.LogTail, "\n"),
		"Env":      got.Env["API_TOKEN"],
	} {
		if strings.Contains(field, "super-secret-value") {
			t.Errorf("%s still carries the secret: %q", name, field)
		}
	}
	if got.Env["GREETING"] != "hello" {
		t.Errorf("a non-secret variable was redacted: %q", got.Env["GREETING"])
	}
}

func TestRedactTextUsesTheSameSecretSet(t *testing.T) {
	t.Parallel()

	env := map[string]string{"DEPLOY_TOKEN": "abc123", "HOME": "/root"}
	got := run.RedactText("push with abc123 from /root", env)
	if strings.Contains(got, "abc123") {
		t.Errorf("RedactText left the secret in %q", got)
	}
	if !strings.Contains(got, "/root") {
		t.Errorf("RedactText scrubbed a non-secret value: %q", got)
	}
}

func TestRedactLines(t *testing.T) {
	t.Parallel()

	env := map[string]string{"API_TOKEN": "s3cret", "GREETING": "hello"}
	got := run.RedactLines([]string{"using s3cret now", "hello world", "s3cret again"}, env)
	want := []string{"using *** now", "hello world", "*** again"}

	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("mismatch (-want +got):\n%s", diff)
	}
	if run.RedactLines(nil, env) != nil {
		t.Error("RedactLines(nil) should stay nil")
	}
}

func TestCaptureFailureRecordsWhatTheTailDropped(t *testing.T) {
	t.Parallel()

	tail := run.NewLogTail(2)
	for _, line := range []string{"one", "two", "three", "four", "five"} {
		tail.Add(line)
	}

	f := run.CaptureFailure(run.Plan{JobID: "lint"}, run.Step{}, 1, tail,
		time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC))

	if diff := cmp.Diff([]string{"four", "five"}, f.LogTail); diff != "" {
		t.Errorf("tail mismatch (-want +got):\n%s", diff)
	}
	if f.LogDropped != 3 {
		t.Errorf("LogDropped = %d, want 3; a truncated tail must say so", f.LogDropped)
	}
}

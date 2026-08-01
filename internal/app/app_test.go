package app_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/elliot14A/abel/internal/app"
	"github.com/elliot14A/abel/internal/core/errs"
	"github.com/elliot14A/abel/internal/core/run"
	"github.com/elliot14A/abel/internal/core/run/runfake"
	"github.com/elliot14A/abel/internal/core/workflow"
	"github.com/elliot14A/abel/internal/infra/store"
)

type fakeWorkflows struct {
	files []workflow.File
	err   error
	calls int
}

func (f *fakeWorkflows) Load(context.Context) ([]workflow.File, error) {
	f.calls++
	return f.files, f.err
}

func workflows(t *testing.T, docs ...string) *fakeWorkflows {
	t.Helper()
	files := make([]workflow.File, 0, len(docs))
	for i, doc := range docs {
		file, err := workflow.Parse(pathFor(i), []byte(doc))
		if err != nil {
			t.Fatalf("fixture %d does not parse: %v", i, err)
		}
		files = append(files, file)
	}
	return &fakeWorkflows{files: files}
}

func pathFor(i int) string {
	return ".github/workflows/" + string(rune('a'+i)) + ".yml"
}

const ciWorkflow = `
name: CI
jobs:
  lint:
    runs-on: ubuntu-latest
    env:
      GITHUB_TOKEN: ghp_leakme
    steps:
      - uses: actions/checkout@v4
      - name: install
        run: npm ci
      - name: typecheck
        run: tsc --noEmit
      - name: test
        run: npm test
`

func frozen(step time.Duration) run.Clock {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	return run.ClockFunc(func() time.Time {
		now = now.Add(step)
		return now
	})
}

func TestRunJobRunsEveryStepWhenGreen(t *testing.T) {
	t.Parallel()

	runner := &runfake.Runner{}
	failures := store.NewMemory()
	uc := app.NewRunJob(workflows(t, ciWorkflow), runner, failures, frozen(time.Second), nil)

	var logs bytes.Buffer
	result, err := uc.Execute(t.Context(), app.RunJobInput{JobID: "lint", Logs: &logs})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if !result.OK() {
		t.Fatalf("result is not OK: %+v", result.Failure)
	}
	session := runner.Sessions()[0]
	if diff := cmp.Diff([]string{"install", "typecheck", "test"}, session.Executed()); diff != "" {
		t.Errorf("executed steps (-want +got):\n%s", diff)
	}

	if len(result.Steps) != 4 || !result.Steps[0].Skipped {
		t.Errorf("skipped steps are missing from the result: %+v", result.Steps)
	}
	if session.Closed() != 1 {
		t.Errorf("session closed %d times, want exactly 1", session.Closed())
	}
}

func TestRunJobStopsAtTheFirstFailureAndCapturesContext(t *testing.T) {
	t.Parallel()

	runner := runfake.Failing("typecheck", 2, "src/a.ts(3,1): error TS2304\nFound 1 error.\n")
	failures := store.NewMemory()
	uc := app.NewRunJob(workflows(t, ciWorkflow), runner, failures, frozen(time.Second), nil)

	var logs bytes.Buffer
	result, err := uc.Execute(t.Context(), app.RunJobInput{JobID: "lint", Logs: &logs})
	if err != nil {
		t.Fatalf("Execute returned an error for a failing step: %v", err)
	}

	if result.OK() {
		t.Fatal("result is OK, want a failure")
	}

	if diff := cmp.Diff([]string{"install", "typecheck"}, runner.Sessions()[0].Executed()); diff != "" {
		t.Errorf("executed steps (-want +got):\n%s", diff)
	}

	f := result.Failure
	if f.StepName != "typecheck" || f.ExitCode != 2 || f.Command != "tsc --noEmit" {
		t.Errorf("failure = %+v", *f)
	}
	if diff := cmp.Diff([]string{"src/a.ts(3,1): error TS2304", "Found 1 error."}, f.LogTail); diff != "" {
		t.Errorf("log tail (-want +got):\n%s", diff)
	}
	if f.Source != ".github/workflows/a.yml" || f.Line == 0 {
		t.Errorf("failure does not point back at the workflow: source=%q line=%d", f.Source, f.Line)
	}

	if !strings.Contains(logs.String(), "TS2304") {
		t.Errorf("logs were not streamed: %q", logs.String())
	}

	stored, err := failures.Get(t.Context(), "lint")
	if err != nil {
		t.Fatalf("failure was not stored: %v", err)
	}
	if stored.StepName != "typecheck" {
		t.Errorf("stored failure = %+v", stored)
	}
}

func TestRunJobReportsStepsAsTheyHappen(t *testing.T) {
	t.Parallel()

	runner := runfake.Failing("typecheck", 1, "boom\n")
	uc := app.NewRunJob(workflows(t, ciWorkflow), runner, store.NewMemory(), frozen(time.Second), nil)

	var events []string
	_, err := uc.Execute(t.Context(), app.RunJobInput{
		JobID:       "lint",
		OnStepStart: func(s run.Step) { events = append(events, "start:"+s.Name) },
		OnStepEnd: func(r run.StepResult) {
			switch {
			case r.Skipped:
				events = append(events, "skip:"+r.Step.Name)
			default:
				events = append(events, fmt.Sprintf("end:%s:%d", r.Step.Name, r.ExitCode))
			}
		},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	want := []string{
		"skip:actions/checkout@v4",
		"start:install", "end:install:0",
		"start:typecheck", "end:typecheck:1",
	}
	if diff := cmp.Diff(want, events); diff != "" {
		t.Errorf("step events (-want +got):\n%s", diff)
	}
}

func TestRunJobWorksWithoutObservers(t *testing.T) {
	t.Parallel()

	uc := app.NewRunJob(workflows(t, ciWorkflow), &runfake.Runner{}, store.NewMemory(), frozen(time.Second), nil)

	if _, err := uc.Execute(t.Context(), app.RunJobInput{JobID: "lint"}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
}

func TestRunJobRedactsSecretsBeforeStoring(t *testing.T) {
	t.Parallel()

	runner := runfake.Failing("typecheck", 1, "auth failed for ghp_leakme\n")
	failures := store.NewMemory()
	uc := app.NewRunJob(workflows(t, ciWorkflow), runner, failures, frozen(time.Second), nil)

	result, err := uc.Execute(t.Context(), app.RunJobInput{JobID: "lint"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if got := result.Failure.Env["GITHUB_TOKEN"]; got != run.Redacted {
		t.Errorf("token in captured env = %q, want redacted", got)
	}
	for _, line := range result.Failure.LogTail {
		if strings.Contains(line, "ghp_leakme") {
			t.Errorf("token leaked into the log tail served to agents: %q", line)
		}
	}
}

func TestRunJobClearsAStaleFailureOnceGreen(t *testing.T) {
	t.Parallel()

	failures := store.NewMemory()
	if err := failures.Put(t.Context(), run.Failure{JobID: "lint", StepName: "old"}); err != nil {
		t.Fatalf("seed the store: %v", err)
	}

	uc := app.NewRunJob(workflows(t, ciWorkflow), &runfake.Runner{}, failures, frozen(time.Second), nil)
	if _, err := uc.Execute(t.Context(), app.RunJobInput{JobID: "lint"}); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if _, err := failures.Get(t.Context(), "lint"); errs.KindOf(err) != errs.KindNotFound {
		t.Errorf("stale failure survived a green run (err: %v)", err)
	}
}

func TestRunJobClosesTheSessionEvenWhenAStepCannotRun(t *testing.T) {
	t.Parallel()

	runner := &runfake.Runner{Steps: map[string]runfake.Script{
		"install": {Err: errors.New("daemon went away")},
	}}
	uc := app.NewRunJob(workflows(t, ciWorkflow), runner, store.NewMemory(), frozen(time.Second), nil)

	_, err := uc.Execute(t.Context(), app.RunJobInput{JobID: "lint"})
	if err == nil {
		t.Fatal("Execute succeeded, want an error")
	}
	if got := errs.KindOf(err); got != errs.KindInternal {
		t.Errorf("kind = %q (err: %v)", got, err)
	}
	if got := runner.Sessions()[0].Closed(); got != 1 {
		t.Errorf("session closed %d times after an exec error, want 1", got)
	}
}

func TestRunJobReportsAnUnavailableDaemonAsADependencyFailure(t *testing.T) {
	t.Parallel()

	runner := runfake.Unavailable("cannot connect to the Docker daemon")
	uc := app.NewRunJob(workflows(t, ciWorkflow), runner, store.NewMemory(), frozen(time.Second), nil)

	_, err := uc.Execute(t.Context(), app.RunJobInput{JobID: "lint"})
	if got := errs.KindOf(err); got != errs.KindDependency {
		t.Fatalf("kind = %q, want DEPENDENCY_UNAVAILABLE (err: %v)", got, err)
	}
	if meta := errs.MetaOf(err); meta["job"] != "lint" || meta["image"] == "" {
		t.Errorf("error metadata = %v, want job and image", meta)
	}
}

func TestRunJobHonoursCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	uc := app.NewRunJob(workflows(t, ciWorkflow), &runfake.Runner{}, store.NewMemory(), frozen(time.Second), nil)
	_, err := uc.Execute(ctx, app.RunJobInput{JobID: "lint"})
	if got := errs.KindOf(err); got != errs.KindCancelled {
		t.Errorf("kind = %q, want CANCELLED (err: %v)", got, err)
	}
}

func TestRunJobAttachesAShellOnlyWhenAsked(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		shell    bool
		terminal bool
		want     bool
	}{
		"not requested":             {false, true, false},
		"requested with a terminal": {true, true, true},
		"requested without one":     {true, false, false},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			runner := &runfake.Runner{}
			uc := app.NewRunJob(workflows(t, ciWorkflow), runner, store.NewMemory(), frozen(time.Second), nil)

			in := app.RunJobInput{JobID: "lint", Shell: tc.shell}
			if tc.terminal {
				in.Terminal = app.Terminal{In: strings.NewReader(""), Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}
			}
			if _, err := uc.Execute(t.Context(), in); err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if got := runner.Sessions()[0].Attached(); got != tc.want {
				t.Errorf("attached = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRunJobRejectsAJobWithNothingToRun(t *testing.T) {
	t.Parallel()

	const usesOnly = `
jobs:
  setup:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
`
	uc := app.NewRunJob(workflows(t, usesOnly), &runfake.Runner{}, store.NewMemory(), frozen(time.Second), nil)

	_, err := uc.Execute(t.Context(), app.RunJobInput{JobID: "setup"})
	if got := errs.KindOf(err); got != errs.KindUnsupported {
		t.Errorf("kind = %q, want UNSUPPORTED (err: %v)", got, err)
	}
}

func TestPlanReportsAnUnknownJobWithAlternatives(t *testing.T) {
	t.Parallel()

	uc := app.NewRunJob(workflows(t, ciWorkflow), &runfake.Runner{}, store.NewMemory(), frozen(time.Second), nil)

	_, err := uc.Plan(t.Context(), "linr", workflow.Options{})
	if got := errs.KindOf(err); got != errs.KindNotFound {
		t.Fatalf("kind = %q, want NOT_FOUND (err: %v)", got, err)
	}
	if !strings.Contains(err.Error(), "lint") {
		t.Errorf("error does not suggest the real job name: %v", err)
	}
}

func TestPlanReportsADuplicateJobIDAsAConflict(t *testing.T) {
	t.Parallel()

	const other = `
jobs:
  lint:
    runs-on: ubuntu-latest
    steps:
      - run: echo different
`
	uc := app.NewRunJob(workflows(t, ciWorkflow, other), &runfake.Runner{}, store.NewMemory(), frozen(time.Second), nil)

	_, err := uc.Plan(t.Context(), "lint", workflow.Options{})
	if got := errs.KindOf(err); got != errs.KindConflict {
		t.Fatalf("kind = %q, want CONFLICT (err: %v)", got, err)
	}
	for _, path := range []string{"a.yml", "b.yml"} {
		if !strings.Contains(err.Error(), path) {
			t.Errorf("conflict does not name %s: %v", path, err)
		}
	}
}

func TestListJobs(t *testing.T) {
	t.Parallel()

	const second = `
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - run: make
`
	uc := app.NewListJobs(workflows(t, ciWorkflow, second))

	refs, err := uc.Execute(t.Context())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	want := []app.JobRef{
		{JobID: "lint", JobName: "lint", WorkflowName: "CI", WorkflowPath: ".github/workflows/a.yml", RunsOn: []string{"ubuntu-latest"}, Runnable: true},
		{JobID: "build", JobName: "build", WorkflowName: "b", WorkflowPath: ".github/workflows/b.yml", RunsOn: []string{"ubuntu-latest"}, Runnable: true},
	}
	if diff := cmp.Diff(want, refs); diff != "" {
		t.Errorf("jobs (-want +got):\n%s", diff)
	}
}

func TestGetFailure(t *testing.T) {
	t.Parallel()

	failures := store.NewMemory()
	seed := run.Failure{
		JobID:   "lint",
		Env:     map[string]string{"NPM_TOKEN": "npm_leakme"},
		LogTail: []string{"used npm_leakme"},
	}
	if err := failures.Put(t.Context(), seed); err != nil {
		t.Fatalf("seed: %v", err)
	}

	uc := app.NewGetFailure(failures)

	t.Run("returns the stored failure", func(t *testing.T) {
		got, err := uc.Execute(t.Context(), "lint")
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}

		if got.Env["NPM_TOKEN"] != run.Redacted || strings.Contains(got.LogTail[0], "npm_leakme") {
			t.Errorf("failure was served unredacted: %+v", got)
		}
	})

	t.Run("an unknown job is NOT_FOUND", func(t *testing.T) {
		_, err := uc.Execute(t.Context(), "nope")
		if got := errs.KindOf(err); got != errs.KindNotFound {
			t.Errorf("kind = %q, want NOT_FOUND", got)
		}
	})

	t.Run("an empty job name is VALIDATION", func(t *testing.T) {
		_, err := uc.Execute(t.Context(), "")
		if got := errs.KindOf(err); got != errs.KindValidation {
			t.Errorf("kind = %q, want VALIDATION", got)
		}
	})
}

func TestMarkFixed(t *testing.T) {
	t.Parallel()

	failures := store.NewMemory()
	if err := failures.Put(t.Context(), run.Failure{JobID: "lint", StepName: "typecheck"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	uc := app.NewMarkFixed(failures, frozen(time.Second))

	got, err := uc.Execute(t.Context(), "lint", "")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !got.Fixed {
		t.Error("returned failure is not marked fixed")
	}

	stored, err := failures.Get(t.Context(), "lint")
	if err != nil {
		t.Fatalf("the record was deleted rather than marked: %v", err)
	}
	if !stored.Fixed {
		t.Error("the stored record is not marked fixed")
	}

	if _, err := uc.Execute(t.Context(), "lint", ""); errs.KindOf(err) != errs.KindConflict {
		t.Errorf("second MarkFixed = %v, want CONFLICT", err)
	}
}

func TestMarkFixedOnAnUnknownJob(t *testing.T) {
	t.Parallel()

	uc := app.NewMarkFixed(store.NewMemory(), frozen(time.Second))
	if _, err := uc.Execute(t.Context(), "nope", ""); errs.KindOf(err) != errs.KindNotFound {
		t.Errorf("kind = %q, want NOT_FOUND", errs.KindOf(err))
	}
}

func TestRunJobCapturesStepOutputOnlyWhenAsked(t *testing.T) {
	t.Parallel()

	runner := &runfake.Runner{Steps: map[string]runfake.Script{
		"install":   {Output: "installing\ndone\n"},
		"typecheck": {Output: "checking\n"},
	}}

	tests := []struct {
		name    string
		capture bool
		want    [][]string
	}{
		{name: "off by default", capture: false, want: [][]string{nil, nil, nil, nil}},
		{
			name:    "on when requested",
			capture: true,
			want:    [][]string{nil, {"installing", "done"}, {"checking"}, nil},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			uc := app.NewRunJob(workflows(t, ciWorkflow), runner, store.NewMemory(),
				frozen(time.Second), nil)
			result, err := uc.Execute(t.Context(), app.RunJobInput{
				JobID: "lint", CaptureOutput: tt.capture,
			})
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}

			got := make([][]string, 0, len(result.Steps))
			for _, s := range result.Steps {
				got = append(got, s.Output)
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("step output mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestRunJobRedactsCapturedStepOutput(t *testing.T) {
	t.Parallel()

	runner := &runfake.Runner{Steps: map[string]runfake.Script{
		"install": {Output: "authenticating with ghp_leakme\n"},
	}}
	uc := app.NewRunJob(workflows(t, ciWorkflow), runner, store.NewMemory(),
		frozen(time.Second), nil)

	result, err := uc.Execute(t.Context(), app.RunJobInput{JobID: "lint", CaptureOutput: true})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	for _, s := range result.Steps {
		for _, line := range s.Output {
			if strings.Contains(line, "ghp_leakme") {
				t.Errorf("step %d output leaked the token: %q", s.Step.Index+1, line)
			}
		}
	}
	if got := result.Steps[1].Output; len(got) != 1 || !strings.Contains(got[0], run.Redacted) {
		t.Errorf("output = %q, want the token replaced with %q", got, run.Redacted)
	}
}

func TestMarkFixedRecordsTheClaim(t *testing.T) {
	t.Parallel()

	failures := store.NewMemory()
	seed := run.Failure{JobID: "lint", StepName: "typecheck", ExitCode: 2}
	if err := failures.Put(t.Context(), seed); err != nil {
		t.Fatalf("seed: %v", err)
	}

	uc := app.NewMarkFixed(failures, frozen(time.Second))
	got, err := uc.Execute(t.Context(), "lint", "widened the tsconfig target")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if !got.Fixed {
		t.Error("the claim was not recorded")
	}
	if got.FixNote != "widened the tsconfig target" {
		t.Errorf("note = %q, want the caller's note", got.FixNote)
	}
	if got.FixedAt.IsZero() {
		t.Error("FixedAt was not stamped from the injected clock")
	}

	stored, err := failures.Get(t.Context(), "lint")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stored.FixNote != got.FixNote || !stored.FixedAt.Equal(got.FixedAt) {
		t.Errorf("the claim was not persisted: %+v", stored)
	}

	if _, err := uc.Execute(t.Context(), "lint", "again"); errs.KindOf(err) != errs.KindConflict {
		t.Errorf("second claim kind = %q, want CONFLICT", errs.KindOf(err))
	}
}

func TestListJobsReportsRunnability(t *testing.T) {
	t.Parallel()

	const mixed = `
name: CI
jobs:
  lint:
    runs-on: ubuntu-latest
    steps:
      - run: npm run lint
  mac:
    runs-on: macos-latest
    steps:
      - run: xcodebuild
`
	refs, err := app.NewListJobs(workflows(t, mixed)).Execute(t.Context())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	got := map[string]app.JobRef{}
	for _, ref := range refs {
		got[ref.JobID] = ref
	}

	if !got["lint"].Runnable {
		t.Errorf("lint is not marked runnable: %+v", got["lint"])
	}
	if got["lint"].Unsupported != "" {
		t.Errorf("lint carries a reason it should not: %q", got["lint"].Unsupported)
	}
	if got["mac"].Runnable {
		t.Error("a macos-latest job was marked runnable")
	}
	if !strings.Contains(got["mac"].Unsupported, "macos-latest") {
		t.Errorf("reason = %q, want it to name the runner", got["mac"].Unsupported)
	}
}

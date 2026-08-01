package mcpserver_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/elliot14A/abel/internal/app"
	"github.com/elliot14A/abel/internal/core/errs"
	"github.com/elliot14A/abel/internal/core/run"
	"github.com/elliot14A/abel/internal/core/run/runfake"
	"github.com/elliot14A/abel/internal/core/workflow"
	"github.com/elliot14A/abel/internal/infra/store"
	"github.com/elliot14A/abel/internal/mcpserver"
)

const ciWorkflow = `
name: CI
jobs:
  lint:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - name: typecheck
        run: tsc --noEmit
`

type fixedWorkflows struct{ files []workflow.File }

func (f fixedWorkflows) Load(context.Context) ([]workflow.File, error) { return f.files, nil }

func connect(t *testing.T, runner run.Runner, failures app.FailureStore) *mcp.ClientSession {
	t.Helper()
	return connectWith(t, runner, failures, nil)
}

func connectWith(
	t *testing.T, runner run.Runner, failures app.FailureStore, opts *mcp.ClientOptions,
) *mcp.ClientSession {
	t.Helper()

	file, err := workflow.Parse(".github/workflows/ci.yml", []byte(ciWorkflow))
	if err != nil {
		t.Fatalf("fixture does not parse: %v", err)
	}
	workflows := fixedWorkflows{files: []workflow.File{file}}
	clock := run.ClockFunc(func() time.Time { return time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC) })

	server := mcpserver.New("test", mcpserver.Tools{
		RunJob:     app.NewRunJob(workflows, runner, failures, clock, nil),
		GetFailure: app.NewGetFailure(failures),
		MarkFixed:  app.NewMarkFixed(failures, clock),
		ListJobs:   app.NewListJobs(workflows),
	})

	serverTransport, clientTransport := mcp.NewInMemoryTransports()

	serverErr := make(chan error, 1)
	go func() { serverErr <- server.Run(t.Context(), serverTransport) }()
	t.Cleanup(func() {
		select {
		case err := <-serverErr:
			if err != nil && !strings.Contains(err.Error(), "closed") &&
				!errors.Is(err, context.Canceled) {
				t.Errorf("server.Run: %v", err)
			}
		default:
		}
	})

	client := mcp.NewClient(&mcp.Implementation{Name: "test-agent", Version: "1"}, opts)
	session, err := client.Connect(t.Context(), clientTransport, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func call(t *testing.T, s *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	res, err := s.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool(%s): %v", name, err)
	}
	return res
}

func decode[T any](t *testing.T, res *mcp.CallToolResult) T {
	t.Helper()
	if res.IsError {
		t.Fatalf("tool returned an error: %s", text(res))
	}
	var out T
	data, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structured content: %v", err)
	}
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("decode structured content %s: %v", data, err)
	}
	return out
}

func text(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

func TestServerExposesTheAgentfixToolSurface(t *testing.T) {
	t.Parallel()

	session := connect(t, &runfake.Runner{}, store.NewMemory())

	tools, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	got := map[string]bool{}
	for _, tool := range tools.Tools {
		got[tool.Name] = true
		if tool.Description == "" {
			t.Errorf("tool %q has no description", tool.Name)
		}
		if tool.InputSchema == nil {
			t.Errorf("tool %q has no input schema", tool.Name)
		}
	}

	for _, want := range []string{"run_job", "plan_job", "get_failure", "mark_fixed", "list_jobs"} {
		if !got[want] {
			t.Errorf("tool %q is missing; have %v", want, got)
		}
	}
}

func TestRunJobToolReportsAFailure(t *testing.T) {
	t.Parallel()

	runner := runfake.Failing("typecheck", 2, "src/a.ts(3,1): error TS2304\n")
	session := connect(t, runner, store.NewMemory())

	out := decode[mcpserver.RunJobOutput](t, call(t, session, "run_job", map[string]any{"job": "lint"}))

	if out.Passed {
		t.Fatal("passed = true, want false")
	}
	if out.Failure == nil {
		t.Fatal("no failure context in the response")
	}
	if out.Failure.StepName != "typecheck" || out.Failure.ExitCode != 2 {
		t.Errorf("failure = %+v", *out.Failure)
	}

	if len(out.Steps) != 2 || !out.Steps[0].Skipped || out.Steps[0].SkipReason == "" {
		t.Errorf("steps = %+v", out.Steps)
	}
	if len(out.Warnings) == 0 {
		t.Error("no warnings reported for the skipped uses: step")
	}
}

func TestGetFailureAndMarkFixedRoundTrip(t *testing.T) {
	t.Parallel()

	failures := store.NewMemory()
	session := connect(t, runfake.Failing("typecheck", 1, "boom\n"), failures)

	res := call(t, session, "get_failure", map[string]any{"job": "lint"})
	if !res.IsError {
		t.Fatal("get_failure on a job that never ran succeeded")
	}
	if !strings.Contains(text(res), "NOT_FOUND") {
		t.Errorf("error text does not carry the kind: %s", text(res))
	}
	if !strings.Contains(text(res), "list_jobs") {
		t.Errorf("NOT_FOUND error gives the agent no next step: %s", text(res))
	}

	call(t, session, "run_job", map[string]any{"job": "lint"})

	failure := decode[mcpserver.FailureOutput](t, call(t, session, "get_failure", map[string]any{"job": "lint"}))
	if failure.Failure.StepName != "typecheck" {
		t.Errorf("failure = %+v", failure.Failure)
	}

	fixed := decode[mcpserver.MarkFixedOutput](t, call(t, session, "mark_fixed", map[string]any{"job": "lint"}))
	if !fixed.Fixed {
		t.Error("mark_fixed did not report the job as fixed")
	}
	if !strings.Contains(fixed.NextStep, "run_job") {
		t.Errorf("mark_fixed does not tell the agent to verify: %q", fixed.NextStep)
	}

	if res := call(t, session, "mark_fixed", map[string]any{"job": "lint"}); !res.IsError {
		t.Error("a second mark_fixed succeeded")
	}
}

func TestToolErrorsCarryKindAndGuidance(t *testing.T) {
	t.Parallel()

	session := connect(t, runfake.Unavailable("cannot connect to the Docker daemon"), store.NewMemory())

	res := call(t, session, "run_job", map[string]any{"job": "lint"})
	if !res.IsError {
		t.Fatal("run_job succeeded with no daemon")
	}
	msg := text(res)
	if !strings.Contains(msg, "DEPENDENCY_UNAVAILABLE") {
		t.Errorf("error text does not carry the kind: %s", msg)
	}

	if !strings.Contains(msg, "do not retry in a loop") {
		t.Errorf("error text does not tell the agent to stop: %s", msg)
	}
}

func TestListJobsTool(t *testing.T) {
	t.Parallel()

	session := connect(t, &runfake.Runner{}, store.NewMemory())

	out := decode[mcpserver.JobsOutput](t, call(t, session, "list_jobs", map[string]any{}))
	if len(out.Jobs) != 1 || out.Jobs[0].JobID != "lint" {
		t.Errorf("jobs = %+v", out.Jobs)
	}
	if out.Jobs[0].WorkflowPath != ".github/workflows/ci.yml" {
		t.Errorf("workflow path = %q, want the repo-relative path", out.Jobs[0].WorkflowPath)
	}
}

func TestIsCleanShutdown(t *testing.T) {
	t.Parallel()

	clean := map[string]error{
		"client closed the pipe": io.EOF,
		"wrapped EOF":            fmt.Errorf("server is closing: %w", io.EOF),
		"ctrl-c":                 context.Canceled,
	}
	for name, err := range clean {
		if !mcpserver.IsCleanShutdown(err) {
			t.Errorf("%s: IsCleanShutdown(%v) = false, want true", name, err)
		}
	}
	if mcpserver.IsCleanShutdown(errors.New("connection reset by peer")) {
		t.Error("a real transport failure was treated as a clean shutdown")
	}
}

func TestPlanJobReportsTheResolvedPlan(t *testing.T) {
	t.Parallel()

	session := connect(t, &runfake.Runner{}, store.NewMemory())
	out := decode[mcpserver.PlanJobOutput](t,
		call(t, session, "plan_job", map[string]any{"job": "lint"}))

	if out.Job != "lint" || out.Image == "" {
		t.Errorf("job = %q, image = %q; want the resolved job and image", out.Job, out.Image)
	}
	if out.Source != ".github/workflows/ci.yml" {
		t.Errorf("workflow_path = %q, want the source file", out.Source)
	}

	want := []mcpserver.StepOutput{
		{
			Index:      0,
			Name:       "actions/checkout@v4",
			Skipped:    true,
			SkipReason: "skipped `actions/checkout`: your working tree is already mounted",
		},
		{Index: 1, Name: "typecheck"},
	}
	if diff := cmp.Diff(want, out.Steps); diff != "" {
		t.Errorf("steps mismatch (-want +got):\n%s", diff)
	}
}

func TestPlanJobStartsNoContainer(t *testing.T) {
	t.Parallel()

	runner := &runfake.Runner{}
	session := connect(t, runner, store.NewMemory())

	call(t, session, "plan_job", map[string]any{"job": "lint"})

	if n := len(runner.Sessions()); n != 0 {
		t.Errorf("plan_job started %d container session(s); it must touch nothing", n)
	}
}

func TestPlanJobHonoursTheImageOverride(t *testing.T) {
	t.Parallel()

	session := connect(t, &runfake.Runner{}, store.NewMemory())
	out := decode[mcpserver.PlanJobOutput](t,
		call(t, session, "plan_job", map[string]any{"job": "lint", "image": "alpine:3"}))

	if out.Image != "alpine:3" {
		t.Errorf("image = %q, want the override", out.Image)
	}
}

func TestPlanJobOnAnUnknownJobIsNotFound(t *testing.T) {
	t.Parallel()

	session := connect(t, &runfake.Runner{}, store.NewMemory())
	res := call(t, session, "plan_job", map[string]any{"job": "nope"})

	if !res.IsError {
		t.Fatal("plan_job succeeded for a job that does not exist")
	}
	body := text(res)
	if !strings.Contains(body, string(errs.KindNotFound)) {
		t.Errorf("error %q does not carry the NOT_FOUND kind", body)
	}
	if !strings.Contains(body, "lint") {
		t.Errorf("error %q does not list the available jobs", body)
	}
}

func TestRunJobHonoursTheTailRequest(t *testing.T) {
	t.Parallel()

	var lines strings.Builder
	for i := range 50 {
		fmt.Fprintf(&lines, "line %d\n", i)
	}
	runner := &runfake.Runner{
		Steps: map[string]runfake.Script{
			"typecheck": {Output: lines.String(), ExitCode: 2},
		},
	}

	tests := []struct {
		name string
		args map[string]any
		want int
	}{
		{name: "an explicit tail caps the log", args: map[string]any{"job": "lint", "tail": 5}, want: 5},
		{name: "omitting tail keeps the default", args: map[string]any{"job": "lint"}, want: 50},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			session := connect(t, runner, store.NewMemory())
			out := decode[mcpserver.RunJobOutput](t, call(t, session, "run_job", tt.args))

			if out.Failure == nil {
				t.Fatal("run_job reported no failure for a step that exited 2")
			}
			if got := len(out.Failure.LogTail); got != tt.want {
				t.Errorf("log tail has %d line(s), want %d", got, tt.want)
			}
		})
	}
}

func TestRunJobTailKeepsTheMostRecentLines(t *testing.T) {
	t.Parallel()

	runner := &runfake.Runner{
		Steps: map[string]runfake.Script{
			"typecheck": {Output: "first\nsecond\nthird\n", ExitCode: 1},
		},
	}
	session := connect(t, runner, store.NewMemory())
	out := decode[mcpserver.RunJobOutput](t,
		call(t, session, "run_job", map[string]any{"job": "lint", "tail": 2}))

	if out.Failure == nil {
		t.Fatal("run_job reported no failure")
	}
	if diff := cmp.Diff([]string{"second", "third"}, out.Failure.LogTail); diff != "" {
		t.Errorf("tail mismatch (-want +got):\n%s", diff)
	}
}

func TestRunJobReturnsStepOutputOnlyWhenAsked(t *testing.T) {
	t.Parallel()

	newRunner := func() *runfake.Runner {
		return &runfake.Runner{Steps: map[string]runfake.Script{
			"typecheck": {Output: "checking\nall good\n"},
		}}
	}

	t.Run("the default returns none", func(t *testing.T) {
		t.Parallel()

		session := connect(t, newRunner(), store.NewMemory())
		out := decode[mcpserver.RunJobOutput](t,
			call(t, session, "run_job", map[string]any{"job": "lint"}))

		for _, s := range out.Steps {
			if len(s.Output) != 0 {
				t.Errorf("step %d returned output without being asked: %q", s.Index+1, s.Output)
			}
		}
	})

	t.Run("output all returns every step", func(t *testing.T) {
		t.Parallel()

		session := connect(t, newRunner(), store.NewMemory())
		out := decode[mcpserver.RunJobOutput](t,
			call(t, session, "run_job", map[string]any{"job": "lint", "output": "all"}))

		var typecheck *mcpserver.StepOutput
		for i, s := range out.Steps {
			if s.Name == "typecheck" {
				typecheck = &out.Steps[i]
			}
		}
		if typecheck == nil {
			t.Fatalf("no typecheck step in %+v", out.Steps)
		}
		if diff := cmp.Diff([]string{"checking", "all good"}, typecheck.Output); diff != "" {
			t.Errorf("output mismatch (-want +got):\n%s", diff)
		}
	})
}

func TestRunJobTimeoutIsReportedClearly(t *testing.T) {
	t.Parallel()

	runner := &runfake.Runner{Steps: map[string]runfake.Script{
		"typecheck": {Delay: 30 * time.Second},
	}}
	session := connect(t, runner, store.NewMemory())

	res := call(t, session, "run_job", map[string]any{"job": "lint", "timeout": 1})
	if !res.IsError {
		t.Fatal("a run that outlived its timeout reported success")
	}

	body := text(res)
	for _, want := range []string{string(errs.KindCancelled), "1s timeout", "lint"} {
		if !strings.Contains(body, want) {
			t.Errorf("error %q is missing %q", body, want)
		}
	}
}

func TestRunJobWithoutATimeoutIsUnbounded(t *testing.T) {
	t.Parallel()

	runner := &runfake.Runner{Steps: map[string]runfake.Script{
		"typecheck": {Delay: 50 * time.Millisecond},
	}}
	session := connect(t, runner, store.NewMemory())

	out := decode[mcpserver.RunJobOutput](t,
		call(t, session, "run_job", map[string]any{"job": "lint"}))
	if !out.Passed {
		t.Errorf("a slow but finite run failed without a timeout: %s", out.Summary)
	}
}

type progressLog struct {
	mu   sync.Mutex
	seen []*mcp.ProgressNotificationParams
}

func (p *progressLog) add(params *mcp.ProgressNotificationParams) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.seen = append(p.seen, params)
}

func (p *progressLog) all() []*mcp.ProgressNotificationParams {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]*mcp.ProgressNotificationParams(nil), p.seen...)
}

func (p *progressLog) waitFor(t *testing.T, n int) []*mcp.ProgressNotificationParams {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		got := p.all()
		if len(got) >= n || time.Now().After(deadline) {
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func connectWithProgress(
	t *testing.T, runner run.Runner, failures app.FailureStore, log *progressLog,
) *mcp.ClientSession {
	t.Helper()
	return connectWith(t, runner, failures, &mcp.ClientOptions{
		ProgressNotificationHandler: func(_ context.Context, req *mcp.ProgressNotificationClientRequest) {
			log.add(req.Params)
		},
	})
}

func TestRunJobReportsProgressWhenTheClientAsks(t *testing.T) {
	t.Parallel()

	var log progressLog
	session := connectWithProgress(t, &runfake.Runner{}, store.NewMemory(), &log)

	params := &mcp.CallToolParams{
		Name:      "run_job",
		Arguments: map[string]any{"job": "lint"},
	}
	params.SetProgressToken("t-1")
	if _, err := session.CallTool(t.Context(), params); err != nil {
		t.Fatalf("CallTool: %v", err)
	}

	seen := log.waitFor(t, 1)
	if len(seen) == 0 {
		t.Fatal("the client asked for progress and received none")
	}
	for _, p := range seen {
		if p.ProgressToken != "t-1" {
			t.Errorf("notification carried token %v, want t-1", p.ProgressToken)
		}
		if p.Total != 2 {
			t.Errorf("total = %v, want 2 (the plan's step count)", p.Total)
		}
	}
	last := seen[len(seen)-1]
	if last.Message != "typecheck" {
		t.Errorf("final message = %q, want the last step's name", last.Message)
	}
}

func TestRunJobIsSilentWhenNoProgressTokenIsSent(t *testing.T) {
	t.Parallel()

	var log progressLog
	session := connectWithProgress(t, &runfake.Runner{}, store.NewMemory(), &log)

	call(t, session, "run_job", map[string]any{"job": "lint"})

	if seen := log.all(); len(seen) != 0 {
		t.Errorf("sent %d unrequested progress notification(s)", len(seen))
	}
}

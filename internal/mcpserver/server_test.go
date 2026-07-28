package mcpserver_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/elliot14A/abel/internal/app"
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

// connect wires a real MCP server to a real MCP client over the SDK's
// in-memory transport, so these tests exercise the actual protocol — schema
// inference, tool dispatch, error payloads — rather than calling handlers
// directly.
func connect(t *testing.T, runner run.Runner, failures app.FailureStore) *mcp.ClientSession {
	t.Helper()

	file, err := workflow.Parse(".github/workflows/ci.yml", []byte(ciWorkflow))
	if err != nil {
		t.Fatalf("fixture does not parse: %v", err)
	}
	workflows := fixedWorkflows{files: []workflow.File{file}}
	clock := run.ClockFunc(func() time.Time { return time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC) })

	server := mcpserver.New("test", mcpserver.UseCases{
		RunJob:     app.NewRunJob(workflows, runner, failures, clock),
		GetFailure: app.NewGetFailure(failures),
		MarkFixed:  app.NewMarkFixed(failures),
		ListJobs:   app.NewListJobs(workflows),
	})

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	// The server's error is reported in Cleanup rather than from the goroutine:
	// calling t.Errorf after the test has finished panics.
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

	client := mcp.NewClient(&mcp.Implementation{Name: "test-agent", Version: "1"}, nil)
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
	// The shared agentfix contract: run_*, get_*, mark_fixed.
	for _, want := range []string{"run_job", "get_failure", "mark_fixed", "list_jobs"} {
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
	// The skipped checkout must be reported with its reason, so the agent does
	// not conclude the step passed.
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

	// Nothing has run yet.
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

	// Marking twice without a re-run is a conflict the agent must see.
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
	// An agent that retries a dead daemon in a loop is the failure mode this
	// hint exists to prevent.
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

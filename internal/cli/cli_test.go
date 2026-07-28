package cli_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/elliot14A/abel/internal/cli"
	"github.com/elliot14A/abel/internal/core/run"
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
  mac:
    runs-on: macos-14
    steps:
      - run: xcodebuild
`

// repo builds a throwaway checkout with a workflow in it.
func repo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, ".github", "workflows")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ci.yml"), []byte(ciWorkflow), 0o600); err != nil {
		t.Fatalf("write workflow: %v", err)
	}
	return root
}

type result struct {
	code           int
	stdout, stderr string
}

// invoke drives the CLI exactly as main does, in-process. Commands that need a
// Docker daemon are covered by the docker package's integration suite; these
// cover the transport itself — parsing, wiring, output and exit codes.
func invoke(t *testing.T, root string, args ...string) result {
	t.Helper()
	var out, errOut bytes.Buffer
	code := cli.Main(t.Context(), append([]string{"--repo", root, "--color", "never"}, args...),
		cli.IO{In: strings.NewReader(""), Out: &out, Err: &errOut})
	return result{code: code, stdout: out.String(), stderr: errOut.String()}
}

func TestVersion(t *testing.T) {
	t.Parallel()

	got := invoke(t, repo(t), "version")
	if got.code != cli.ExitOK {
		t.Errorf("exit = %d, want %d (stderr: %s)", got.code, cli.ExitOK, got.stderr)
	}
	if !strings.HasPrefix(got.stdout, "abel ") {
		t.Errorf("stdout = %q", got.stdout)
	}
}

func TestJobsListsEveryJob(t *testing.T) {
	t.Parallel()

	got := invoke(t, repo(t), "jobs")
	if got.code != cli.ExitOK {
		t.Fatalf("exit = %d (stderr: %s)", got.code, got.stderr)
	}
	for _, want := range []string{"lint", "mac", ".github/workflows/ci.yml"} {
		if !strings.Contains(got.stdout, want) {
			t.Errorf("output is missing %q:\n%s", want, got.stdout)
		}
	}
}

func TestDryRunNeedsNoDaemon(t *testing.T) {
	t.Parallel()

	got := invoke(t, repo(t), "run", "lint", "--dry-run")
	if got.code != cli.ExitOK {
		t.Fatalf("exit = %d (stderr: %s)", got.code, got.stderr)
	}
	if !strings.Contains(got.stdout, "typecheck") {
		t.Errorf("plan does not list the step:\n%s", got.stdout)
	}
	if !strings.Contains(got.stdout, "already mounted") {
		t.Errorf("plan does not explain the skipped checkout:\n%s", got.stdout)
	}
}

func TestDryRunJSONIsTheOnlyThingOnStdout(t *testing.T) {
	t.Parallel()

	got := invoke(t, repo(t), "run", "lint", "--dry-run", "--json")
	if got.code != cli.ExitOK {
		t.Fatalf("exit = %d (stderr: %s)", got.code, got.stderr)
	}

	var plan run.Plan
	if err := json.Unmarshal([]byte(got.stdout), &plan); err != nil {
		t.Fatalf("stdout is not a single JSON document (%v):\n%s", err, got.stdout)
	}
	if plan.JobID != "lint" || plan.Image == "" || len(plan.Steps) != 2 {
		t.Errorf("plan = %+v", plan)
	}
}

func TestExitCodesMatchTheTaxonomy(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		args []string
		want int
	}{
		"unknown job is NOT_FOUND":            {[]string{"run", "nope", "--dry-run"}, cli.ExitNotFound},
		"unmappable runner is UNSUPPORTED":    {[]string{"run", "mac", "--dry-run"}, cli.ExitUnsupported},
		"no captured failure is NOT_FOUND":    {[]string{"failure", "lint"}, cli.ExitNotFound},
		"an unknown flag is a usage error":    {[]string{"run", "lint", "--nonsense"}, cli.ExitUsage},
		"an unknown command is a usage error": {[]string{"frobnicate"}, cli.ExitUsage},
	}

	root := repo(t)
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := invoke(t, root, tc.args...)
			if got.code != tc.want {
				t.Errorf("exit = %d, want %d\nstdout: %s\nstderr: %s",
					got.code, tc.want, got.stdout, got.stderr)
			}
		})
	}
}

func TestMissingWorkflowDirectoryIsExplained(t *testing.T) {
	t.Parallel()

	got := invoke(t, t.TempDir(), "jobs")
	if got.code != cli.ExitNotFound {
		t.Errorf("exit = %d, want %d", got.code, cli.ExitNotFound)
	}
	if !strings.Contains(got.stderr, ".github/workflows") {
		t.Errorf("error does not say what is missing: %s", got.stderr)
	}
}

func TestUnknownRepoIsRejectedBeforeAnythingElse(t *testing.T) {
	t.Parallel()

	got := invoke(t, filepath.Join(t.TempDir(), "does-not-exist"), "jobs")
	if got.code != cli.ExitNotFound {
		t.Errorf("exit = %d, want %d (stderr: %s)", got.code, cli.ExitNotFound, got.stderr)
	}
}

func TestHelpIsAvailableAndDescribesTheTool(t *testing.T) {
	t.Parallel()

	got := invoke(t, repo(t), "--help")
	// kong writes help to stdout and reports it via a special error; either way
	// the user must see the commands.
	combined := got.stdout + got.stderr
	for _, want := range []string{"run", "mcp", "jobs", "failure"} {
		if !strings.Contains(combined, want) {
			t.Errorf("help does not mention %q:\n%s", want, combined)
		}
	}
}

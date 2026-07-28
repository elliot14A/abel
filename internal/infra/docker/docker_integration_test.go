//go:build integration

// This file is the other half of the contract that runfake stands in for.
// Everything above the run.Runner port is unit-tested against the fake; this
// suite proves the fake is not lying, against a real daemon.
//
//	make test-integration
package docker_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/elliot14A/abel/internal/core/errs"
	"github.com/elliot14A/abel/internal/core/run"
	"github.com/elliot14A/abel/internal/infra/docker"
)

// testImage is deliberately tiny: the suite should cost seconds, not minutes.
const testImage = "alpine:3"

func newRunner(t *testing.T, repo string) *docker.Runner {
	t.Helper()
	r, err := docker.New(t.Context(), docker.Config{RepoRoot: repo, ContainerPrefix: "abel-test"})
	if err != nil {
		t.Fatalf("docker.New: %v (is the daemon running?)", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

func planFor(workdir string, steps ...run.Step) run.Plan {
	return run.Plan{
		JobID: "itest", JobName: "itest", Image: testImage,
		Workdir: workdir, Steps: steps,
	}
}

func step(index int, name, script string) run.Step {
	return run.Step{Index: index, Name: name, Script: script, Shell: "sh", WorkingDir: run.DefaultWorkdir}
}

func TestSessionRunsStepsAndReportsExitCodes(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "marker.txt"), []byte("mounted"), 0o600); err != nil {
		t.Fatalf("seed repo: %v", err)
	}

	session, err := newRunner(t, repo).Start(t.Context(), planFor(run.DefaultWorkdir))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = session.Close(t.Context()) })

	t.Run("the repository is mounted at the workdir", func(t *testing.T) {
		var out bytes.Buffer
		code, err := session.Exec(t.Context(), step(0, "read marker", "cat marker.txt"), &out)
		if err != nil {
			t.Fatalf("Exec: %v", err)
		}
		if code != 0 {
			t.Fatalf("exit code = %d, want 0 (output: %q)", code, out.String())
		}
		if !strings.Contains(out.String(), "mounted") {
			t.Errorf("output = %q, want the mounted file's contents", out.String())
		}
	})

	t.Run("a non-zero exit is a value, not an error", func(t *testing.T) {
		code, err := session.Exec(t.Context(), step(1, "fail", "exit 7"), nil)
		if err != nil {
			t.Fatalf("Exec returned an error for a failing step: %v", err)
		}
		if code != 7 {
			t.Errorf("exit code = %d, want 7", code)
		}
	})

	t.Run("stderr is interleaved with stdout", func(t *testing.T) {
		var out bytes.Buffer
		if _, err := session.Exec(t.Context(), step(2, "both", "echo out; echo err >&2"), &out); err != nil {
			t.Fatalf("Exec: %v", err)
		}
		for _, want := range []string{"out", "err"} {
			if !strings.Contains(out.String(), want) {
				t.Errorf("output %q is missing %q", out.String(), want)
			}
		}
	})

	t.Run("state carries between steps, as it does in a real job", func(t *testing.T) {
		if _, err := session.Exec(t.Context(), step(3, "write", "echo hello > /tmp/carried"), nil); err != nil {
			t.Fatalf("Exec: %v", err)
		}
		var out bytes.Buffer
		code, err := session.Exec(t.Context(), step(4, "read", "cat /tmp/carried"), &out)
		if err != nil || code != 0 {
			t.Fatalf("Exec = (%d, %v)", code, err)
		}
		if !strings.Contains(out.String(), "hello") {
			t.Errorf("output = %q, want the file written by the previous step", out.String())
		}
	})

	t.Run("step env reaches the command", func(t *testing.T) {
		s := step(5, "env", "echo $ABEL_TEST")
		s.Env = map[string]string{"ABEL_TEST": "visible"}
		var out bytes.Buffer
		if _, err := session.Exec(t.Context(), s, &out); err != nil {
			t.Fatalf("Exec: %v", err)
		}
		if !strings.Contains(out.String(), "visible") {
			t.Errorf("output = %q, want the injected variable", out.String())
		}
	})

	t.Run("a step's working directory is honoured", func(t *testing.T) {
		s := step(6, "pwd", "pwd")
		s.WorkingDir = "/tmp"
		var out bytes.Buffer
		if _, err := session.Exec(t.Context(), s, &out); err != nil {
			t.Fatalf("Exec: %v", err)
		}
		if !strings.Contains(out.String(), "/tmp") {
			t.Errorf("output = %q, want /tmp", out.String())
		}
	})
}

func TestCloseIsIdempotentAndRemovesTheContainer(t *testing.T) {
	session, err := newRunner(t, t.TempDir()).Start(t.Context(), planFor(run.DefaultWorkdir))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	if err := session.Close(t.Context()); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := session.Close(t.Context()); err != nil {
		t.Errorf("second Close: %v, want nil (Close must be idempotent)", err)
	}
	if _, err := session.Exec(t.Context(), step(0, "after close", "true"), nil); err == nil {
		t.Error("Exec on a closed session succeeded")
	}
}

func TestStartOnAMissingImageIsNotFound(t *testing.T) {
	plan := planFor(run.DefaultWorkdir)
	plan.Image = "abel-does-not-exist.invalid/nope:0"

	_, err := newRunner(t, t.TempDir()).Start(t.Context(), plan)
	if err == nil {
		t.Fatal("Start succeeded on a missing image")
	}
	switch errs.KindOf(err) {
	case errs.KindNotFound, errs.KindDependency:
		// Either is defensible: the registry may 404 or be unreachable.
	default:
		t.Errorf("kind = %q, want NOT_FOUND or DEPENDENCY_UNAVAILABLE (err: %v)", errs.KindOf(err), err)
	}
}

package workflow_test

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/elliot14A/abel/internal/core/errs"
	"github.com/elliot14A/abel/internal/core/run"
	"github.com/elliot14A/abel/internal/core/workflow"
)

func TestResolveMergesEnvInPrecedenceOrder(t *testing.T) {
	t.Parallel()

	plan, err := workflow.Resolve(load(t, "ci.yml"), "lint", workflow.Options{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	install := stepNamed(t, plan, "install")
	want := map[string]string{
		"CI":        "true",
		"LOG_LEVEL": "debug",
		"NODE_ENV":  "test",
	}
	if diff := cmp.Diff(want, install.Env); diff != "" {
		t.Errorf("install env (-want +got):\n%s", diff)
	}

	lint := stepNamed(t, plan, "lint")
	if lint.Env["LOG_LEVEL"] != "trace" {
		t.Errorf("step env did not override job env: LOG_LEVEL = %q, want trace", lint.Env["LOG_LEVEL"])
	}

	lint.Env["LOG_LEVEL"] = "mutated"
	if install.Env["LOG_LEVEL"] != "debug" {
		t.Error("step environments share backing storage")
	}
}

func TestResolveWorkingDirectory(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		job  string
		step string
		want string
	}{
		"job defaults apply":      {"lint", "install", run.DefaultWorkdir + "/app"},
		"step overrides defaults": {"test", "unit tests", run.DefaultWorkdir + "/server"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			plan, err := workflow.Resolve(load(t, "ci.yml"), tc.job, workflow.Options{})
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if got := stepNamed(t, plan, tc.step).WorkingDir; got != tc.want {
				t.Errorf("WorkingDir = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestResolveImage(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		job  string
		opts workflow.Options
		want string
	}{
		"runs-on maps to a runner image": {"lint", workflow.Options{}, "catthehacker/ubuntu:act-latest"},
		"container beats runs-on":        {"test", workflow.Options{}, "golang:1.26"},
		"--image beats everything":       {"test", workflow.Options{Image: "alpine:3"}, "alpine:3"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			plan, err := workflow.Resolve(load(t, "ci.yml"), tc.job, tc.opts)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if plan.Image != tc.want {
				t.Errorf("Image = %q, want %q", plan.Image, tc.want)
			}
		})
	}
}

func TestResolveSkipsUsesStepsWithAReason(t *testing.T) {
	t.Parallel()

	plan, err := workflow.Resolve(load(t, "ci.yml"), "lint", workflow.Options{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	checkout, setup := plan.Steps[0], plan.Steps[1]
	if !checkout.Skip || !strings.Contains(checkout.SkipReason, "already mounted") {
		t.Errorf("checkout skip reason = %q", checkout.SkipReason)
	}
	if !setup.Skip || !strings.Contains(setup.SkipReason, "toolchains") {
		t.Errorf("setup-node skip reason = %q", setup.SkipReason)
	}

	for i, s := range plan.Steps {
		if s.Index != i {
			t.Errorf("step %d has Index %d", i, s.Index)
		}
	}
	if !plan.Runnable() {
		t.Error("plan reports nothing to run, but three run: steps remain")
	}
}

func TestResolveWarnsAboutEveryIgnoredFeature(t *testing.T) {
	t.Parallel()

	plan, err := workflow.Resolve(load(t, "unsupported.yml"), "matrix-job", workflow.Options{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	joined := strings.Join(warningMessages(plan), "\n")
	for _, want := range []string{"matrix", "job-level `if:`", "`if:` is not evaluated", "expressions"} {
		if !strings.Contains(joined, want) {
			t.Errorf("warnings do not mention %q:\n%s", want, joined)
		}
	}
	for _, w := range plan.Warnings {
		if w.SourceLine == 0 {
			t.Errorf("warning %q has no source line", w.Message)
		}
	}
}

func TestResolveErrors(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		file, job string
		want      errs.Kind
	}{
		"unknown job":        {"ci.yml", "nope", errs.KindNotFound},
		"unmappable runner":  {"unsupported.yml", "mac-job", errs.KindUnsupported},
		"unsupported shell":  {"unsupported.yml", "powershell-job", errs.KindUnsupported},
		"no runner declared": {"unsupported.yml", "no-runner", errs.KindValidation},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := workflow.Resolve(load(t, tc.file), tc.job, workflow.Options{})
			if err == nil {
				t.Fatal("Resolve succeeded, want an error")
			}
			if got := errs.KindOf(err); got != tc.want {
				t.Errorf("kind = %q, want %q (err: %v)", got, tc.want, err)
			}
		})
	}
}

func TestResolveUnknownJobListsAvailableJobs(t *testing.T) {
	t.Parallel()

	_, err := workflow.Resolve(load(t, "ci.yml"), "typo", workflow.Options{})
	if err == nil {
		t.Fatal("Resolve succeeded, want an error")
	}
	for _, job := range []string{"lint", "test"} {
		if !strings.Contains(err.Error(), job) {
			t.Errorf("error does not offer %q as an alternative: %v", job, err)
		}
	}
}

func TestStepCommand(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		shell string
		want  []string
	}{
		"bash fails fast and on pipe errors": {
			"bash", []string{"bash", "-e", "-o", "pipefail", "-c", "make test"},
		},
		"sh fails fast": {"sh", []string{"sh", "-e", "-c", "make test"}},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			step := run.Step{Script: "make test", Shell: tc.shell}
			if diff := cmp.Diff(tc.want, step.Command()); diff != "" {
				t.Errorf("Command() (-want +got):\n%s", diff)
			}
		})
	}
}

func stepNamed(t *testing.T, plan run.Plan, name string) run.Step {
	t.Helper()
	for _, s := range plan.Steps {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("no step named %q in plan for %s", name, plan.JobID)
	return run.Step{}
}

func warningMessages(plan run.Plan) []string {
	out := make([]string, 0, len(plan.Warnings))
	for _, w := range plan.Warnings {
		out = append(out, w.String())
	}
	return out
}

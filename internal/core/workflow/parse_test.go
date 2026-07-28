package workflow_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/elliot14A/abel/internal/core/errs"
	"github.com/elliot14A/abel/internal/core/workflow"
)

func load(t *testing.T, name string) workflow.File {
	t.Helper()
	path := filepath.Join("testdata", name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	f, err := workflow.Parse(path, data)
	if err != nil {
		t.Fatalf("Parse(%s) = %v, want no error", name, err)
	}
	return f
}

func TestParseCIFixture(t *testing.T) {
	t.Parallel()

	f := load(t, "ci.yml")

	if f.Name != "CI" {
		t.Errorf("Name = %q, want %q", f.Name, "CI")
	}
	if diff := cmp.Diff([]string{"lint", "test"}, f.JobIDs); diff != "" {
		t.Errorf("JobIDs are not in declaration order (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(map[string]string{"CI": "true", "LOG_LEVEL": "info"}, f.Env); diff != "" {
		t.Errorf("workflow env (-want +got):\n%s", diff)
	}
	if f.Defaults.Shell != "bash" {
		t.Errorf("defaults.run.shell = %q, want bash", f.Defaults.Shell)
	}
}

func TestParseNormalisesPolymorphicFields(t *testing.T) {
	t.Parallel()

	f := load(t, "ci.yml")

	lint, _ := f.Job("lint")
	if diff := cmp.Diff([]string{"ubuntu-latest"}, lint.RunsOn); diff != "" {
		t.Errorf("scalar runs-on was not normalised to a slice (-want +got):\n%s", diff)
	}

	test, _ := f.Job("test")
	if diff := cmp.Diff([]string{"lint"}, test.Needs); diff != "" {
		t.Errorf("needs (-want +got):\n%s", diff)
	}
	if test.Container.Image != "golang:1.26" {
		t.Errorf("container.image = %q, want golang:1.26", test.Container.Image)
	}

	if diff := cmp.Diff(map[string]string{"CGO_ENABLED": "0"}, test.Container.Env); diff != "" {
		t.Errorf("container env (-want +got):\n%s", diff)
	}

	if f.Env["CI"] != "true" {
		t.Errorf("env CI = %q, want the string %q", f.Env["CI"], "true")
	}
}

func TestParseRecordsSourceLines(t *testing.T) {
	t.Parallel()

	f := load(t, "ci.yml")
	lint, _ := f.Job("lint")

	if lint.Line == 0 {
		t.Error("job line was not recorded; diagnostics cannot point at the source")
	}
	for i, step := range lint.Steps {
		if step.Line == 0 {
			t.Errorf("step %d has no source line", i)
		}
	}

	if got := lint.Steps[3].Line; got != 32 {
		t.Errorf("typecheck step line = %d, want 32", got)
	}
}

func TestParseStepLabels(t *testing.T) {
	t.Parallel()

	f := load(t, "ci.yml")
	lint, _ := f.Job("lint")

	want := []string{"actions/checkout@v4", "actions/setup-node@v4", "install", "typecheck", "lint"}
	got := make([]string, 0, len(lint.Steps))
	for _, s := range lint.Steps {
		got = append(got, s.Label())
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("step labels (-want +got):\n%s", diff)
	}
}

func TestParseRejectsBadInput(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		yaml string
		want errs.Kind
	}{
		"empty document":   {"", errs.KindValidation},
		"no jobs key":      {"name: x\non: push\n", errs.KindValidation},
		"empty jobs":       {"jobs:\n", errs.KindValidation},
		"broken yaml":      {"jobs:\n  a:\n   - x\n  b: [\n", errs.KindValidation},
		"jobs is a list":   {"jobs:\n  - lint\n", errs.KindValidation},
		"env is a list":    {"env:\n  - A=1\njobs:\n  a:\n    steps: []\n", errs.KindValidation},
		"runs-on is a map": {"jobs:\n  a:\n    runs-on:\n      label: x\n    steps: []\n", errs.KindValidation},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := workflow.Parse("bad.yml", []byte(tc.yaml))
			if err == nil {
				t.Fatal("Parse succeeded, want an error")
			}
			if got := errs.KindOf(err); got != tc.want {
				t.Errorf("kind = %q, want %q (err: %v)", got, tc.want, err)
			}
		})
	}
}

func TestParseErrorQuotesTheSource(t *testing.T) {
	t.Parallel()

	_, err := workflow.Parse("bad.yml", []byte("jobs:\n  a:\n   - x\n  b: [\n"))
	if err == nil {
		t.Fatal("Parse succeeded, want an error")
	}

	if !strings.Contains(err.Error(), "bad.yml") {
		t.Errorf("error does not name the file: %v", err)
	}
}

func TestParseIsDeterministicAcrossRuns(t *testing.T) {
	t.Parallel()

	data, err := os.ReadFile(filepath.Join("testdata", "unsupported.yml"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	first, err := workflow.Parse("unsupported.yml", data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	for range 20 {
		next, err := workflow.Parse("unsupported.yml", data)
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		if diff := cmp.Diff(first.JobIDs, next.JobIDs); diff != "" {
			t.Fatalf("job order is not stable across parses (-first +next):\n%s", diff)
		}
	}
}

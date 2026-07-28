package workflowfile_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/elliot14A/abel/internal/core/errs"
	"github.com/elliot14A/abel/internal/infra/workflowfile"
)

func repoWith(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), workflowfile.DefaultDir)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return dir
}

const minimal = `
jobs:
  lint:
    runs-on: ubuntu-latest
    steps:
      - run: echo hi
`

func TestLoadReadsWorkflowsInFilenameOrder(t *testing.T) {
	t.Parallel()

	dir := repoWith(t, map[string]string{
		"zeta.yml":   minimal,
		"alpha.yaml": minimal,
		"README.md":  "not a workflow",
		"notes.txt":  "also not",
	})

	files, err := workflowfile.NewLoader(filepath.Dir(filepath.Dir(dir)), dir).Load(t.Context())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	got := []string{filepath.Base(files[0].Path), filepath.Base(files[1].Path)}
	if diff := cmp.Diff([]string{"alpha.yaml", "zeta.yml"}, got); diff != "" {
		t.Errorf("loaded files (-want +got):\n%s", diff)
	}
}

func TestLoadErrors(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		files map[string]string
		dir   func(t *testing.T) string
		want  errs.Kind
	}{
		"missing directory": {
			dir:  func(t *testing.T) string { return filepath.Join(t.TempDir(), "nope") },
			want: errs.KindNotFound,
		},
		"empty directory": {
			files: map[string]string{},
			want:  errs.KindNotFound,
		},
		"no yaml files": {
			files: map[string]string{"README.md": "hi"},
			want:  errs.KindNotFound,
		},
		"one broken document fails the load": {
			files: map[string]string{"good.yml": minimal, "bad.yml": "jobs: [\n"},
			want:  errs.KindValidation,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir := repoWith(t, tc.files)
			if tc.dir != nil {
				dir = tc.dir(t)
			}

			_, err := workflowfile.NewLoader(filepath.Dir(filepath.Dir(dir)), dir).Load(t.Context())
			if got := errs.KindOf(err); got != tc.want {
				t.Errorf("kind = %q, want %q (err: %v)", got, tc.want, err)
			}
		})
	}
}

func TestLoadNamesTheOffendingFile(t *testing.T) {
	t.Parallel()

	dir := repoWith(t, map[string]string{"broken.yml": "jobs: [\n"})

	_, err := workflowfile.NewLoader(filepath.Dir(filepath.Dir(dir)), dir).Load(t.Context())
	if err == nil {
		t.Fatal("Load succeeded, want an error")
	}
	if !contains(err.Error(), "broken.yml") {
		t.Errorf("error does not name the file: %v", err)
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}

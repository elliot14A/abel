package workflow_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/elliot14A/abel/internal/core/workflow"
)

func FuzzParse(f *testing.F) {
	for _, name := range []string{"ci.yml", "unsupported.yml"} {
		data, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			f.Fatalf("read seed %s: %v", name, err)
		}
		f.Add(data)
	}
	f.Add([]byte("jobs:\n  a:\n    runs-on: ubuntu-latest\n    steps:\n      - run: x\n"))
	f.Add([]byte("jobs: {a: {steps: [{run: x}]}}"))
	f.Add([]byte("&a [*a]"))

	f.Fuzz(func(t *testing.T, data []byte) {
		file, err := workflow.Parse("fuzz.yml", data)
		if err != nil {
			return
		}

		if len(file.JobIDs) != len(file.Jobs) {
			t.Fatalf("JobIDs has %d entries, Jobs has %d", len(file.JobIDs), len(file.Jobs))
		}
		for _, id := range file.JobIDs {
			if _, ok := file.Job(id); !ok {
				t.Fatalf("JobIDs lists %q but Jobs does not contain it", id)
			}

			_, _ = workflow.Resolve(file, id, workflow.Options{})
		}
	})
}

package workflow_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/elliot14A/abel/internal/core/workflow"
)

// FuzzParse asserts the invariant that matters for a tool pointed at other
// people's repositories: abel never panics on a workflow file, however
// malformed. Native fuzzing is coverage-guided, which makes it a far better
// fit for a parser than hand-written generators.
//
// Run longer with: go test ./internal/core/workflow -run=Fuzz -fuzz=FuzzParse.
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
		// A successful parse must be internally consistent: every declared job
		// ID resolves, and the ID list matches the map exactly.
		if len(file.JobIDs) != len(file.Jobs) {
			t.Fatalf("JobIDs has %d entries, Jobs has %d", len(file.JobIDs), len(file.Jobs))
		}
		for _, id := range file.JobIDs {
			if _, ok := file.Job(id); !ok {
				t.Fatalf("JobIDs lists %q but Jobs does not contain it", id)
			}
			// Resolve may reject the job, but it must never panic.
			_, _ = workflow.Resolve(file, id, workflow.Options{})
		}
	})
}

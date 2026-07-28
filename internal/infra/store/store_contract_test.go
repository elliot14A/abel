package store_test

import (
	"errors"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/elliot14A/abel/internal/app"
	"github.com/elliot14A/abel/internal/core/errs"
	"github.com/elliot14A/abel/internal/core/run"
	"github.com/elliot14A/abel/internal/infra/store"
)

func TestFailureStoreContract(t *testing.T) {
	t.Parallel()

	adapters := map[string]func(t *testing.T) app.FailureStore{
		"memory": func(*testing.T) app.FailureStore { return store.NewMemory() },
		"file":   func(t *testing.T) app.FailureStore { return store.NewFile(t.TempDir()) },
	}

	for name, newStore := range adapters {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s := newStore(t)
			ctx := t.Context()

			t.Run("get on an unknown job is NOT_FOUND", func(t *testing.T) {
				_, err := s.Get(ctx, "never-run")
				if got := errs.KindOf(err); got != errs.KindNotFound {
					t.Fatalf("kind = %q, want NOT_FOUND (err: %v)", got, err)
				}
			})

			t.Run("put then get round-trips every field", func(t *testing.T) {
				want := sampleFailure("lint")
				if err := s.Put(ctx, want); err != nil {
					t.Fatalf("Put: %v", err)
				}
				got, err := s.Get(ctx, "lint")
				if err != nil {
					t.Fatalf("Get: %v", err)
				}
				if diff := cmp.Diff(want, got); diff != "" {
					t.Errorf("round-trip lost data (-want +got):\n%s", diff)
				}
			})

			t.Run("put replaces the previous record", func(t *testing.T) {
				first := sampleFailure("replace-me")
				if err := s.Put(ctx, first); err != nil {
					t.Fatalf("Put: %v", err)
				}
				second := first
				second.ExitCode = 42
				second.LogTail = []string{"different"}
				if err := s.Put(ctx, second); err != nil {
					t.Fatalf("Put: %v", err)
				}
				got, err := s.Get(ctx, "replace-me")
				if err != nil {
					t.Fatalf("Get: %v", err)
				}
				if got.ExitCode != 42 || len(got.LogTail) != 1 {
					t.Errorf("got %+v, want the second record", got)
				}
			})

			t.Run("delete removes the record", func(t *testing.T) {
				if err := s.Put(ctx, sampleFailure("gone")); err != nil {
					t.Fatalf("Put: %v", err)
				}
				if err := s.Delete(ctx, "gone"); err != nil {
					t.Fatalf("Delete: %v", err)
				}
				if _, err := s.Get(ctx, "gone"); errs.KindOf(err) != errs.KindNotFound {
					t.Errorf("after Delete, Get returned %v", err)
				}
			})

			t.Run("deleting an absent record is not an error", func(t *testing.T) {
				if err := s.Delete(ctx, "never-existed"); err != nil {
					t.Errorf("Delete on an absent job = %v, want nil", err)
				}
			})

			t.Run("stored records do not alias the caller's buffers", func(t *testing.T) {
				f := sampleFailure("aliasing")
				if err := s.Put(ctx, f); err != nil {
					t.Fatalf("Put: %v", err)
				}
				f.LogTail[0] = "mutated"
				f.Env["CI"] = "mutated"

				got, err := s.Get(ctx, "aliasing")
				if err != nil {
					t.Fatalf("Get: %v", err)
				}
				if got.LogTail[0] == "mutated" || got.Env["CI"] == "mutated" {
					t.Error("the store handed back the caller's live buffers")
				}
			})

			t.Run("a job with no ID is rejected", func(t *testing.T) {
				err := s.Put(ctx, run.Failure{})
				if got := errs.KindOf(err); got != errs.KindValidation {
					t.Errorf("kind = %q, want VALIDATION (err: %v)", got, err)
				}
			})

			t.Run("a job name that escapes the store is rejected", func(t *testing.T) {
				for _, jobID := range []string{"../escape", "a/b"} {
					f := sampleFailure(jobID)
					if err := s.Put(ctx, f); err != nil && errs.KindOf(err) != errs.KindValidation {
						t.Errorf("Put(%q) = %v, want nil or a VALIDATION error", jobID, err)
					}
				}
			})
		})
	}
}

func TestFileStoreReportsACorruptRecord(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	s := store.NewFile(dir)
	if err := s.Put(t.Context(), sampleFailure("lint")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := writeFile(dir+"/lint.json", "{not json"); err != nil {
		t.Fatalf("corrupt the record: %v", err)
	}

	_, err := s.Get(t.Context(), "lint")
	if got := errs.KindOf(err); got != errs.KindValidation {
		t.Fatalf("kind = %q, want VALIDATION (err: %v)", got, err)
	}

	if !contains(err.Error(), "lint.json") {
		t.Errorf("error does not name the file to delete: %v", err)
	}
}

func TestFileStoreIsLazyAboutItsDirectory(t *testing.T) {
	t.Parallel()

	dir := t.TempDir() + "/nested/.abel"
	s := store.NewFile(dir)

	if _, err := s.Get(t.Context(), "lint"); !errors.Is(err, errs.OfKind(errs.KindNotFound)) {
		t.Fatalf("Get on a fresh store = %v, want NOT_FOUND", err)
	}
	if dirExists(dir) {
		t.Error("a read created the state directory")
	}

	if err := s.Put(t.Context(), sampleFailure("lint")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if !dirExists(dir) {
		t.Error("a write did not create the state directory")
	}
	if !fileExists(dir + "/.gitignore") {
		t.Error("the state directory was not made git-invisible")
	}
}

func sampleFailure(jobID string) run.Failure {
	return run.Failure{
		JobID:      jobID,
		JobName:    "Lint",
		Image:      "catthehacker/ubuntu:act-latest",
		StepIndex:  2,
		StepName:   "typecheck",
		Command:    "tsc --noEmit",
		ExitCode:   2,
		LogTail:    []string{"src/a.ts(3,1): error TS2304", "Found 1 error."},
		Env:        map[string]string{"CI": "true"},
		WorkDir:    "/abel/workspace",
		Source:     ".github/workflows/ci.yml",
		Line:       32,
		CapturedAt: time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC),
	}
}

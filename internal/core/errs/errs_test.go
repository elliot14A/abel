package errs_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/elliot14A/abel/internal/core/errs"
)

func TestKindOf(t *testing.T) {
	t.Parallel()

	base := errs.New(errs.KindNotFound, "store.Get", "no failure recorded for %q", "lint")

	tests := map[string]struct {
		err  error
		want errs.Kind
	}{
		"nil is unclassified":         {nil, ""},
		"classified error":            {base, errs.KindNotFound},
		"wrapped with fmt keeps kind": {fmt.Errorf("load: %w", base), errs.KindNotFound},
		"outermost classification wins": {
			errs.Wrap(base, errs.KindInternal, "app.GetFailure", "unexpected"),
			errs.KindInternal,
		},
		"context cancellation is recognised": {context.Canceled, errs.KindCancelled},
		"deadline is recognised":             {fmt.Errorf("dial: %w", context.DeadlineExceeded), errs.KindCancelled},
		"unclassified is internal":           {errors.New("boom"), errs.KindInternal},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := errs.KindOf(tc.err); got != tc.want {
				t.Errorf("KindOf() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestWrapNilIsNil(t *testing.T) {
	t.Parallel()

	if err := errs.Wrap(nil, errs.KindInternal, "op", "msg"); err != nil {
		t.Fatalf("Wrap(nil) = %v, want nil", err)
	}
}

func TestIsMatchesOnKindSentinel(t *testing.T) {
	t.Parallel()

	err := errs.Wrap(
		errs.New(errs.KindDependency, "docker.Ping", "daemon unreachable"),
		errs.KindDependency, "app.RunJob", "cannot start job",
	)

	if !errors.Is(err, errs.OfKind(errs.KindDependency)) {
		t.Error("errors.Is did not match the DEPENDENCY_UNAVAILABLE sentinel")
	}
	if errors.Is(err, errs.OfKind(errs.KindNotFound)) {
		t.Error("errors.Is matched an unrelated kind")
	}
}

func TestErrorMessageIsABreadcrumbTrail(t *testing.T) {
	t.Parallel()

	err := errs.Wrap(
		errs.New(errs.KindValidation, "workflow.Parse", "line 4: invalid runs-on"),
		errs.KindValidation, "app.RunJob", "workflow is not runnable",
	)

	want := "app.RunJob: workflow is not runnable: workflow.Parse: line 4: invalid runs-on"
	if got := err.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestWithDoesNotMutateReceiver(t *testing.T) {
	t.Parallel()

	base := errs.New(errs.KindStepFailed, "run.Exec", "step failed").With("job", "lint")
	derived := base.With("step", "2")

	if _, present := base.Meta["step"]; present {
		t.Error("With mutated the receiver")
	}
	if derived.Meta["job"] != "lint" || derived.Meta["step"] != "2" {
		t.Errorf("derived meta = %v, want job=lint step=2", derived.Meta)
	}
}

func TestMetaOfMergesChainOutermostFirst(t *testing.T) {
	t.Parallel()

	inner := errs.New(errs.KindStepFailed, "run.Exec", "exit 1").With("job", "inner").With("step", "3")
	outer := (&errs.Error{
		Kind: errs.KindStepFailed, Op: "app.RunJob", Msg: "job failed", Err: inner,
	}).With("job", "outer")

	got := errs.MetaOf(outer)
	if got["job"] != "outer" {
		t.Errorf("job = %q, want the outermost value %q", got["job"], "outer")
	}
	if got["step"] != "3" {
		t.Errorf("step = %q, want the inner value to survive", got["step"])
	}
}

package cli_test

import (
	"context"
	"errors"
	"testing"

	"github.com/elliot14A/abel/internal/cli"
	"github.com/elliot14A/abel/internal/core/errs"
)

func TestExitCode(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		err  error
		want int
	}{
		"success":      {nil, cli.ExitOK},
		"validation":   {errs.New(errs.KindValidation, "op", "bad"), cli.ExitUsage},
		"not found":    {errs.New(errs.KindNotFound, "op", "missing"), cli.ExitNotFound},
		"conflict":     {errs.New(errs.KindConflict, "op", "clash"), cli.ExitConflict},
		"unsupported":  {errs.New(errs.KindUnsupported, "op", "nope"), cli.ExitUnsupported},
		"dependency":   {errs.New(errs.KindDependency, "op", "no daemon"), cli.ExitDependency},
		"step failed":  {errs.New(errs.KindStepFailed, "op", "exit 1"), cli.ExitStepFailed},
		"cancelled":    {context.Canceled, cli.ExitCancelled},
		"internal":     {errs.New(errs.KindInternal, "op", "bug"), cli.ExitInternal},
		"unclassified": {errors.New("boom"), cli.ExitInternal},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := cli.ExitCode(tc.err); got != tc.want {
				t.Errorf("ExitCode() = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestEveryKindHasAnExitCode(t *testing.T) {
	t.Parallel()

	kinds := []errs.Kind{
		errs.KindValidation, errs.KindNotFound, errs.KindConflict, errs.KindUnsupported,
		errs.KindDependency, errs.KindStepFailed, errs.KindCancelled, errs.KindInternal,
	}
	seen := map[int]errs.Kind{}
	for _, kind := range kinds {
		code := cli.ExitCode(errs.New(kind, "op", "x"))
		if code == cli.ExitInternal && kind != errs.KindInternal {
			t.Errorf("kind %q falls through to the internal exit code", kind)
		}
		if other, clash := seen[code]; clash {
			t.Errorf("kinds %q and %q share exit code %d", other, kind, code)
		}
		seen[code] = kind
	}
}

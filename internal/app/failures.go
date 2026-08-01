package app

import (
	"context"

	"github.com/elliot14A/abel/internal/core/errs"
	"github.com/elliot14A/abel/internal/core/run"
)

const (
	opGetFailure = "app.GetFailure"
	opMarkFixed  = "app.MarkFixed"
)

type GetFailure struct {
	failures FailureStore
}

func NewGetFailure(failures FailureStore) *GetFailure {
	return &GetFailure{failures: failures}
}

func (u *GetFailure) Execute(ctx context.Context, jobID string) (run.Failure, error) {
	if jobID == "" {
		return run.Failure{}, errs.New(errs.KindValidation, opGetFailure, "a job name is required")
	}
	failure, err := u.failures.Get(ctx, jobID)
	if err != nil {
		return run.Failure{}, err
	}

	return failure.Redact(), nil
}

type MarkFixed struct {
	failures FailureStore
	clock    run.Clock
}

func NewMarkFixed(failures FailureStore, clock run.Clock) *MarkFixed {
	if clock == nil {
		clock = run.SystemClock
	}
	return &MarkFixed{failures: failures, clock: clock}
}

func (u *MarkFixed) Execute(ctx context.Context, jobID, note string) (run.Failure, error) {
	if jobID == "" {
		return run.Failure{}, errs.New(errs.KindValidation, opMarkFixed, "a job name is required")
	}

	failure, err := u.failures.Get(ctx, jobID)
	if err != nil {
		return run.Failure{}, err
	}
	if failure.Fixed {
		return failure, errs.New(errs.KindConflict, opMarkFixed,
			"job %q is already marked fixed; re-run it to verify", jobID).With("job", jobID)
	}

	failure.Fixed = true
	failure.FixNote = note
	failure.FixedAt = u.clock.Now()
	if err := u.failures.Put(ctx, failure); err != nil {
		return run.Failure{}, errs.New(errs.KindOf(err), opMarkFixed,
			"cannot record job %q as fixed", jobID).With("job", jobID).Wrapping(err)
	}
	return failure, nil
}

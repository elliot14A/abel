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

// GetFailure returns the failure context captured by the last run of a job.
// This is what an agent reads over MCP before attempting a fix.
type GetFailure struct {
	failures FailureStore
}

// NewGetFailure builds the use-case.
func NewGetFailure(failures FailureStore) *GetFailure {
	return &GetFailure{failures: failures}
}

// Execute returns the stored failure, or an error of kind NOT_FOUND if the job
// has not failed since it was last run.
func (u *GetFailure) Execute(ctx context.Context, jobID string) (run.Failure, error) {
	if jobID == "" {
		return run.Failure{}, errs.New(errs.KindValidation, opGetFailure, "a job name is required")
	}
	failure, err := u.failures.Get(ctx, jobID)
	if err != nil {
		return run.Failure{}, err
	}
	// Redaction is idempotent, and applying it on read as well as on write
	// means a store written by an older abel still cannot leak a credential.
	return failure.Redact(), nil
}

// MarkFixed records that an agent believes it has fixed a job's failure.
//
// It marks rather than deletes: the record is what a re-run is compared
// against, and deleting it would make `get_failure` say "nothing failed" when
// the truth is "something failed and was claimed fixed but not re-verified".
type MarkFixed struct {
	failures FailureStore
}

// NewMarkFixed builds the use-case.
func NewMarkFixed(failures FailureStore) *MarkFixed {
	return &MarkFixed{failures: failures}
}

// Execute flags the stored failure as fixed and returns the updated record.
// Marking an already-fixed failure is a conflict: it means the agent skipped
// the re-run that was supposed to clear it.
func (u *MarkFixed) Execute(ctx context.Context, jobID string) (run.Failure, error) {
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
	if err := u.failures.Put(ctx, failure); err != nil {
		return run.Failure{}, errs.New(errs.KindOf(err), opMarkFixed,
			"cannot record job %q as fixed", jobID).With("job", jobID).Wrapping(err)
	}
	return failure, nil
}

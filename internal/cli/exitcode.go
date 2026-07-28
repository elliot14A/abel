package cli

import (
	"github.com/elliot14A/abel/internal/core/errs"
)

// Exit codes. These are part of abel's contract: a pre-push hook, a Makefile
// and a CI job all branch on them, so they are documented in the README and
// must not be reshuffled.
const (
	// ExitOK means the job's steps all passed.
	ExitOK = 0
	// ExitStepFailed means abel worked and a workflow step failed. This is the
	// interesting one, and it is deliberately distinct from every abel-level
	// error below: a hook wants to block on it, a script wants to react to it.
	ExitStepFailed = 1
	// ExitUsage means the request was malformed: a bad flag, an invalid
	// workflow file, an unparseable job name.
	ExitUsage = 2
	// ExitNotFound means the job, workflow or failure record does not exist.
	ExitNotFound = 3
	// ExitConflict means the request contradicts the current state.
	ExitConflict = 4
	// ExitUnsupported means abel knows the feature and does not implement it.
	ExitUnsupported = 5
	// ExitDependency means something abel depends on is unavailable — most
	// often the Docker daemon.
	ExitDependency = 6
	// ExitCancelled matches the shell convention for SIGINT.
	ExitCancelled = 130
	// ExitInternal means a bug in abel.
	ExitInternal = 70
)

// ExitCode is the single place abel's error taxonomy meets the process exit
// status. Adding a Kind without adding a case here fails the exhaustive
// linter, which is the point.
func ExitCode(err error) int {
	if err == nil {
		return ExitOK
	}
	switch errs.KindOf(err) {
	case errs.KindValidation:
		return ExitUsage
	case errs.KindNotFound:
		return ExitNotFound
	case errs.KindConflict:
		return ExitConflict
	case errs.KindUnsupported:
		return ExitUnsupported
	case errs.KindDependency:
		return ExitDependency
	case errs.KindStepFailed:
		return ExitStepFailed
	case errs.KindCancelled:
		return ExitCancelled
	case errs.KindInternal:
		return ExitInternal
	default:
		return ExitInternal
	}
}

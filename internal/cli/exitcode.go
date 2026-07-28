package cli

import (
	"github.com/elliot14A/abel/internal/core/errs"
)

const (
	ExitOK          = 0
	ExitStepFailed  = 1
	ExitUsage       = 2
	ExitNotFound    = 3
	ExitConflict    = 4
	ExitUnsupported = 5
	ExitDependency  = 6
	ExitCancelled   = 130
	ExitInternal    = 70
)

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

// Package errs defines abel's error taxonomy.
//
// Every failure abel can produce is classified by exactly one [Kind]. The Kind is
// the discriminant that the transport mappers switch on — internal/cli.ExitCode,
// internal/mcpserver.ToToolError — so a new failure mode means one new Kind plus
// one line in each mapper, and the exhaustive linter fails the build until both
// are done.
//
// This package is pure: no I/O, no ambient state.
package errs

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"
)

// Kind classifies a failure. It is the only thing transports are allowed to
// branch on; never branch on an error's message.
type Kind string

const (
	// KindValidation covers malformed user input: a bad workflow file, an unknown
	// flag combination, an unparseable job name.
	KindValidation Kind = "VALIDATION"
	// KindNotFound covers a referenced thing that does not exist: an unknown job,
	// a missing workflow directory, a failure record that was never captured.
	KindNotFound Kind = "NOT_FOUND"
	// KindConflict covers a request that is valid but not applicable to the
	// current state, such as marking a job fixed while it is still running.
	KindConflict Kind = "CONFLICT"
	// KindUnsupported covers workflow features abel knowingly does not implement
	// (matrices, expressions, most `uses:` actions). It is a promise, not a bug:
	// the message must say what was unsupported and what abel did instead.
	KindUnsupported Kind = "UNSUPPORTED"
	// KindDependency covers an external system abel needs but cannot reach or
	// drive: the Docker daemon, a registry, the configured --fix agent.
	KindDependency Kind = "DEPENDENCY_UNAVAILABLE"
	// KindStepFailed covers the expected, load-bearing failure: a workflow step
	// exited non-zero. This is abel working correctly, so it must never be logged
	// as an internal error.
	KindStepFailed Kind = "STEP_FAILED"
	// KindCancelled covers context cancellation and deadline expiry, including
	// the user pressing Ctrl-C.
	KindCancelled Kind = "CANCELLED"
	// KindInternal is the fallback: a bug in abel. Any error reaching a transport
	// unclassified is reported as internal.
	KindInternal Kind = "INTERNAL"
)

// Error is abel's structured error. Construct it with [New] or [Wrap] rather
// than by literal, so Op is never accidentally empty.
type Error struct {
	// Kind is the discriminant. Required.
	Kind Kind
	// Op is the logical operation that failed, in "package.Function" form
	// ("workflow.Parse"). Wrapping accumulates a breadcrumb trail without
	// paying for stack traces.
	Op string
	// Msg is the human-facing sentence. It is shown to the user and sent over
	// MCP, so it must never contain secrets.
	Msg string
	// Meta carries structured context for logs and MCP payloads (job name, step
	// index, image). Values must be safe to display.
	Meta map[string]string
	// Err is the wrapped cause, or nil.
	Err error
}

// New builds a classified error with no cause.
func New(kind Kind, op, format string, args ...any) *Error {
	return &Error{Kind: kind, Op: op, Msg: fmt.Sprintf(format, args...)}
}

// Wrap classifies and annotates an existing error. A nil err yields nil, so it
// is safe to call unconditionally in a return.
func Wrap(err error, kind Kind, op, format string, args ...any) error {
	if err == nil {
		return nil
	}
	return &Error{Kind: kind, Op: op, Msg: fmt.Sprintf(format, args...), Err: err}
}

// Wrapping returns a copy of e with cause attached, for the common shape of
// building a classified error with metadata around a lower-level failure:
//
//	return errs.New(errs.KindDependency, op, "cannot reach the daemon").
//		With("host", host).Wrapping(err)
//
// Unlike [Wrap] it always returns a non-nil *Error, so only call it on a path
// that has already decided there is a failure.
func (e *Error) Wrapping(cause error) *Error {
	clone := *e
	clone.Err = cause
	return &clone
}

// With returns a copy of e carrying an additional Meta entry. The receiver is
// not mutated, so an *Error captured in a sentinel stays pristine.
func (e *Error) With(key, value string) *Error {
	clone := *e
	clone.Meta = make(map[string]string, len(e.Meta)+1)
	maps.Copy(clone.Meta, e.Meta)
	clone.Meta[key] = value
	return &clone
}

func (e *Error) Error() string {
	var b strings.Builder
	if e.Op != "" {
		b.WriteString(e.Op)
		b.WriteString(": ")
	}
	b.WriteString(e.Msg)
	if e.Err != nil {
		b.WriteString(": ")
		b.WriteString(e.Err.Error())
	}
	return b.String()
}

// Unwrap exposes the cause to errors.Is and errors.As.
func (e *Error) Unwrap() error { return e.Err }

// Is lets callers match on Kind alone via errors.Is(err, errs.OfKind(k)).
func (e *Error) Is(target error) bool {
	var t *Error
	if !errors.As(target, &t) {
		return false
	}
	// A Kind-only sentinel (no Op, no Msg) matches any error of that Kind.
	return t.Kind == e.Kind && t.Op == "" && t.Msg == ""
}

// OfKind returns a sentinel matching any error of the given Kind, for use with
// errors.Is:
//
//	if errors.Is(err, errs.OfKind(errs.KindNotFound)) { ... }
func OfKind(kind Kind) error { return &Error{Kind: kind} }

// KindOf reports the Kind of err by walking the wrap chain, returning the
// outermost classification. Unclassified and nil-adjacent errors are internal;
// context cancellation is recognised without the caller having to classify it.
func KindOf(err error) Kind {
	if err == nil {
		return ""
	}
	var e *Error
	if errors.As(err, &e) {
		return e.Kind
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return KindCancelled
	}
	return KindInternal
}

// MetaOf returns the merged Meta of every *Error in the chain, outermost
// winning. It never returns nil, so callers can range over it directly.
func MetaOf(err error) map[string]string {
	meta := map[string]string{}
	var e *Error
	for rest := err; errors.As(rest, &e); rest = e.Err {
		for k, v := range e.Meta {
			if _, seen := meta[k]; !seen {
				meta[k] = v
			}
		}
		if e.Err == nil {
			break
		}
	}
	return meta
}

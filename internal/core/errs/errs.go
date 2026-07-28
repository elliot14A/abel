package errs

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"
)

type Kind string

const (
	KindValidation  Kind = "VALIDATION"
	KindNotFound    Kind = "NOT_FOUND"
	KindConflict    Kind = "CONFLICT"
	KindUnsupported Kind = "UNSUPPORTED"
	KindDependency  Kind = "DEPENDENCY_UNAVAILABLE"
	KindStepFailed  Kind = "STEP_FAILED"
	KindCancelled   Kind = "CANCELLED"
	KindInternal    Kind = "INTERNAL"
)

type Error struct {
	Kind Kind
	Op   string
	Msg  string
	Meta map[string]string
	Err  error
}

func New(kind Kind, op, format string, args ...any) *Error {
	return &Error{Kind: kind, Op: op, Msg: fmt.Sprintf(format, args...)}
}

func Wrap(err error, kind Kind, op, format string, args ...any) error {
	if err == nil {
		return nil
	}
	return &Error{Kind: kind, Op: op, Msg: fmt.Sprintf(format, args...), Err: err}
}

func (e *Error) Wrapping(cause error) *Error {
	clone := *e
	clone.Err = cause
	return &clone
}

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

func (e *Error) Unwrap() error { return e.Err }

func (e *Error) Is(target error) bool {
	var t *Error
	if !errors.As(target, &t) {
		return false
	}

	return t.Kind == e.Kind && t.Op == "" && t.Msg == ""
}

func OfKind(kind Kind) error { return &Error{Kind: kind} }

func KindOf(err error) Kind {
	if err == nil {
		return ""
	}
	if e, ok := errors.AsType[*Error](err); ok {
		return e.Kind
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return KindCancelled
	}
	return KindInternal
}

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

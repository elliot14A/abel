package run

import (
	"context"
	"io"
	"time"
)

type Runner interface {
	Start(ctx context.Context, plan Plan) (Session, error)
}

type Session interface {
	Exec(ctx context.Context, step Step, out io.Writer) (int, error)
	Attach(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer) error
	Close(ctx context.Context) error
}

type PullReporter interface {
	Pull(status PullStatus)
	PullDone()
}

type Clock interface {
	Now() time.Time
}

type ClockFunc func() time.Time

func (f ClockFunc) Now() time.Time { return f() }

var SystemClock = ClockFunc(time.Now)

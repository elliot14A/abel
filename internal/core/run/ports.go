package run

import (
	"context"
	"io"
	"time"
)

// Runner starts a container for a plan. It is implemented by
// internal/infra/docker in production and by a fake in tests; nothing in core
// or app knows which.
type Runner interface {
	// Start prepares the container for the plan — pulling the image if needed,
	// mounting the repository at plan.Workdir — and returns a Session that owns
	// it. The caller must Close the session.
	Start(ctx context.Context, plan Plan) (Session, error)
}

// Session is a live container that steps execute in. Steps share one session so
// that filesystem changes and installed packages carry across steps, exactly as
// they do in a real job.
type Session interface {
	// Exec runs one step, streaming combined stdout and stderr to out, and
	// returns the step's exit code. A non-zero exit code is a value, not an
	// error: an error means abel could not run the step at all.
	Exec(ctx context.Context, step Step, out io.Writer) (int, error)
	// Attach hands the container's shell to the user's terminal for --shell.
	Attach(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer) error
	// Close removes the container. It is called even when a step failed, so it
	// must tolerate a half-finished session.
	Close(ctx context.Context) error
}

// Clock supplies the current time. Injecting it is what keeps failure capture
// deterministic under test.
type Clock interface {
	Now() time.Time
}

// ClockFunc adapts a function to the Clock interface.
type ClockFunc func() time.Time

// Now implements Clock.
func (f ClockFunc) Now() time.Time { return f() }

// SystemClock is the real clock, for the composition root.
var SystemClock = ClockFunc(time.Now)

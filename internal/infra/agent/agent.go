package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"strings"
	"time"

	"github.com/elliot14A/abel/internal/core/errs"
	"github.com/elliot14A/abel/internal/core/run"
)

const opFix = "agent.Fix"

const DefaultTimeout = 10 * time.Minute

type Config struct {
	Command string
	Dir     string
	Timeout time.Duration
	Env     []string
}

type Runner struct {
	cfg Config
}

func New(cfg Config) (*Runner, error) {
	if strings.TrimSpace(cfg.Command) == "" {
		return nil, errs.New(errs.KindValidation, opFix, "--fix needs an agent command")
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}
	name := strings.Fields(cfg.Command)[0]
	if _, err := exec.LookPath(name); err != nil {
		return nil, errs.New(errs.KindNotFound, opFix,
			"agent command %q is not on your PATH", name).With("command", name).Wrapping(err)
	}
	return &Runner{cfg: cfg}, nil
}

func (r *Runner) Fix(ctx context.Context, failure run.Failure, out io.Writer) error {
	payload, err := json.MarshalIndent(promptFor(failure), "", "  ")
	if err != nil {
		return errs.New(errs.KindInternal, opFix, "cannot encode the failure context").Wrapping(err)
	}

	ctx, cancel := context.WithTimeout(ctx, r.cfg.Timeout)
	defer cancel()

	fields := strings.Fields(r.cfg.Command)
	cmd := exec.CommandContext(ctx, fields[0], fields[1:]...) //nolint:gosec // the command is the user's own --fix argument
	cmd.Dir = r.cfg.Dir
	cmd.Env = r.cfg.Env
	cmd.Stdin = bytes.NewReader(payload)
	if out != nil {
		cmd.Stdout, cmd.Stderr = out, out
	}

	switch err := cmd.Run(); {
	case err == nil:
		return nil
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return errs.New(errs.KindCancelled, opFix,
			"the agent did not finish within %s", r.cfg.Timeout).Wrapping(err)
	case errors.Is(ctx.Err(), context.Canceled):
		return errs.New(errs.KindCancelled, opFix, "the agent was interrupted").Wrapping(err)
	default:
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return errs.New(errs.KindDependency, opFix,
				"the agent exited %d without fixing anything", exitErr.ExitCode()).
				With("command", r.cfg.Command).Wrapping(err)
		}
		return errs.New(errs.KindDependency, opFix,
			"cannot run the agent %q", r.cfg.Command).With("command", r.cfg.Command).Wrapping(err)
	}
}

type prompt struct {
	Tool        string      `json:"tool"`
	Instruction string      `json:"instruction"`
	Failure     run.Failure `json:"failure"`
}

func promptFor(failure run.Failure) prompt {
	return prompt{
		Tool: "abel",
		Instruction: "A CI step failed when reproduced locally. Using the failure context below, " +
			"make the smallest change to the repository that makes this step pass. " +
			"Do not modify the workflow file to skip or weaken the check. " +
			"Do not commit; the developer will review your diff.",
		Failure: failure,
	}
}

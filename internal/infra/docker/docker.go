package docker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"github.com/elliot14A/abel/internal/core/errs"
	"github.com/elliot14A/abel/internal/core/run"
)

const (
	opNew    = "docker.New"
	opStart  = "docker.Start"
	opExec   = "docker.Exec"
	opAttach = "docker.Attach"
	opClose  = "docker.Close"
)

var keepAlive = []string{"sh", "-c", "while :; do sleep 3600; done"}

type Config struct {
	RepoRoot        string
	Progress        run.PullReporter
	Log             *slog.Logger
	ContainerPrefix string
	Pull            bool
}

func (c Config) log() *slog.Logger {
	if c.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return c.Log
}

type Runner struct {
	cli *client.Client
	cfg Config
}

func New(ctx context.Context, cfg Config) (*Runner, error) {
	if cfg.RepoRoot == "" {
		return nil, errs.New(errs.KindInternal, opNew, "RepoRoot is required")
	}
	root, err := filepath.Abs(cfg.RepoRoot)
	if err != nil {
		return nil, errs.New(errs.KindValidation, opNew,
			"cannot resolve the repository path %s", cfg.RepoRoot).Wrapping(err)
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		return nil, errs.New(errs.KindValidation, opNew,
			"%s is not a directory", root).With("path", root)
	}
	cfg.RepoRoot = root
	if cfg.ContainerPrefix == "" {
		cfg.ContainerPrefix = "abel"
	}

	cli, err := client.New(client.FromEnv)
	if err != nil {
		return nil, errs.New(errs.KindDependency, opNew,
			"cannot configure a Docker client").Wrapping(err)
	}
	if _, err := cli.Ping(ctx, client.PingOptions{}); err != nil {
		_ = cli.Close()
		return nil, errs.New(errs.KindDependency, opNew,
			"cannot reach the Docker daemon; is it running? (%s)", daemonHint()).Wrapping(err)
	}
	return &Runner{cli: cli, cfg: cfg}, nil
}

func (r *Runner) Close() error { return r.cli.Close() }

func daemonHint() string {
	if host := os.Getenv("DOCKER_HOST"); host != "" {
		return "DOCKER_HOST=" + host
	}
	return "no DOCKER_HOST set; using the default socket"
}

func (r *Runner) Start(ctx context.Context, plan run.Plan) (run.Session, error) {
	if err := r.ensureImage(ctx, plan.Image); err != nil {
		return nil, err
	}

	name, err := r.nameFor(plan.JobID)
	if err != nil {
		return nil, err
	}
	created, err := r.cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name: name,
		Config: &container.Config{
			Image:      plan.Image,
			Cmd:        keepAlive,
			WorkingDir: plan.Workdir,
			Tty:        false,

			Labels: map[string]string{
				"abel.job": plan.JobID,
				"abel.pid": strconv.Itoa(os.Getpid()),
			},
		},
		HostConfig: &container.HostConfig{
			Binds: []string{r.cfg.RepoRoot + ":" + plan.Workdir},

			AutoRemove: false,
		},
	})
	if err != nil {
		return nil, errs.New(kindOfDockerError(err), opStart,
			"cannot create a container from %s", plan.Image).
			With("image", plan.Image).Wrapping(err)
	}

	session := &Session{cli: r.cli, id: created.ID, plan: plan}
	if _, err := r.cli.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		_ = session.Close(context.WithoutCancel(ctx))
		return nil, errs.New(kindOfDockerError(err), opStart,
			"cannot start the container for job %q", plan.JobID).
			With("job", plan.JobID).With("image", plan.Image).Wrapping(err)
	}
	return session, nil
}

func (r *Runner) ensureImage(ctx context.Context, image string) error {
	log := r.cfg.log()

	if !r.cfg.Pull {
		if _, err := r.cli.ImageInspect(ctx, image); err == nil {
			log.Debug("image_cached", "image", image)
			return nil
		} else if !cerrdefs.IsNotFound(err) {
			return errs.New(kindOfDockerError(err), opStart,
				"cannot inspect image %s", image).With("image", image).Wrapping(err)
		}
	}

	log.Info("pull_start", "image", image)
	resp, err := r.cli.ImagePull(ctx, image, client.ImagePullOptions{})
	if err != nil {
		return errs.New(kindOfDockerError(err), opStart,
			"cannot pull %s", image).With("image", image).Wrapping(err)
	}
	defer func() { _ = resp.Close() }()

	var report func(run.PullStatus)
	var last run.PullStatus
	if r.cfg.Progress != nil {
		report = func(status run.PullStatus) {
			last = status
			r.cfg.Progress.Pull(status)
		}
	} else {
		report = func(status run.PullStatus) { last = status }
	}

	if err := drainPull(resp, image, report); err != nil {
		return err
	}
	if r.cfg.Progress != nil {
		r.cfg.Progress.PullDone()
	}

	_, total := last.Bytes()
	log.Info("pull_done", "image", image, "layers", len(last.Layers), "bytes", total)
	return nil
}

type Session struct {
	cli       *client.Client
	id        string
	plan      run.Plan
	closeOnce sync.Once
	closeErr  error
}

func (s *Session) Exec(ctx context.Context, step run.Step, out io.Writer) (int, error) {
	created, err := s.cli.ExecCreate(ctx, s.id, client.ExecCreateOptions{
		Cmd:          step.Command(),
		Env:          envSlice(step.Env),
		WorkingDir:   step.WorkingDir,
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		return 0, errs.New(kindOfDockerError(err), opExec,
			"cannot create an exec for step %q", step.Name).With("step", step.Name).Wrapping(err)
	}

	attached, err := s.cli.ExecAttach(ctx, created.ID, client.ExecAttachOptions{})
	if err != nil {
		return 0, errs.New(kindOfDockerError(err), opExec,
			"cannot attach to step %q", step.Name).With("step", step.Name).Wrapping(err)
	}
	defer attached.Close()

	if out == nil {
		out = io.Discard
	}

	if _, err := stdcopy.StdCopy(out, out, attached.Reader); err != nil && !isClosedStream(err) {
		return 0, errs.New(kindOfDockerError(err), opExec,
			"the output stream of step %q was interrupted", step.Name).
			With("step", step.Name).Wrapping(err)
	}

	inspected, err := s.cli.ExecInspect(ctx, created.ID, client.ExecInspectOptions{})
	if err != nil {
		return 0, errs.New(kindOfDockerError(err), opExec,
			"cannot read the exit status of step %q", step.Name).
			With("step", step.Name).Wrapping(err)
	}
	if inspected.Running {
		return 0, errs.New(errs.KindInternal, opExec,
			"step %q reported no exit status", step.Name).With("step", step.Name)
	}
	return inspected.ExitCode, nil
}

func (s *Session) Attach(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer) error {
	shell := s.shell(ctx)
	created, err := s.cli.ExecCreate(ctx, s.id, client.ExecCreateOptions{
		Cmd:          []string{shell},
		WorkingDir:   s.plan.Workdir,
		Env:          envSlice(sessionEnv(s.plan)),
		TTY:          true,
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		return errs.New(kindOfDockerError(err), opAttach, "cannot create a shell").Wrapping(err)
	}

	attached, err := s.cli.ExecAttach(ctx, created.ID, client.ExecAttachOptions{TTY: true})
	if err != nil {
		return errs.New(kindOfDockerError(err), opAttach, "cannot attach a shell").Wrapping(err)
	}
	defer attached.Close()

	go func() {
		if stdin != nil {
			_, _ = io.Copy(attached.Conn, stdin)
		}
		_ = attached.CloseWrite()
	}()

	if stdout == nil {
		stdout = io.Discard
	}
	if _, err := io.Copy(stdout, attached.Reader); err != nil && !isClosedStream(err) {
		return errs.New(kindOfDockerError(err), opAttach, "the shell session ended abruptly").Wrapping(err)
	}
	return nil
}

func (s *Session) shell(ctx context.Context) string {
	created, err := s.cli.ExecCreate(ctx, s.id, client.ExecCreateOptions{
		Cmd: []string{"sh", "-c", "command -v bash >/dev/null"},
	})
	if err != nil {
		return "sh"
	}
	if _, err := s.cli.ExecStart(ctx, created.ID, client.ExecStartOptions{}); err != nil {
		return "sh"
	}
	inspected, err := s.cli.ExecInspect(ctx, created.ID, client.ExecInspectOptions{})
	if err != nil || inspected.Running || inspected.ExitCode != 0 {
		return "sh"
	}
	return "bash"
}

func (s *Session) Close(ctx context.Context) error {
	s.closeOnce.Do(func() {
		_, err := s.cli.ContainerRemove(ctx, s.id, client.ContainerRemoveOptions{
			Force:         true,
			RemoveVolumes: true,
		})
		if err != nil && !cerrdefs.IsNotFound(err) {
			short := shortID(s.id)
			s.closeErr = errs.New(kindOfDockerError(err), opClose,
				"cannot remove container %s (remove it by hand with `docker rm -f %s`)",
				short, short).With("container", s.id).Wrapping(err)
		}
	})
	return s.closeErr
}

func sessionEnv(plan run.Plan) map[string]string {
	for _, step := range plan.Steps {
		if !step.Skip {
			return step.Env
		}
	}
	return nil
}

func envSlice(env map[string]string) []string {
	if len(env) == 0 {
		return nil
	}
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}

	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func (r *Runner) nameFor(jobID string) (string, error) {
	var suffix [4]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", errs.New(errs.KindInternal, opStart,
			"cannot generate a container name").Wrapping(err)
	}
	return fmt.Sprintf("%s-%s-%s", r.cfg.ContainerPrefix, sanitise(jobID), hex.EncodeToString(suffix[:])), nil
}

func sanitise(jobID string) string {
	var b strings.Builder
	for _, r := range jobID {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '.', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	if b.Len() == 0 {
		return "job"
	}
	return b.String()
}

func kindOfDockerError(err error) errs.Kind {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return errs.KindCancelled
	case cerrdefs.IsNotFound(err):
		return errs.KindNotFound
	case cerrdefs.IsInvalidArgument(err):
		return errs.KindValidation
	case cerrdefs.IsConflict(err):
		return errs.KindConflict
	case cerrdefs.IsPermissionDenied(err), cerrdefs.IsUnauthorized(err):
		return errs.KindDependency
	case cerrdefs.IsUnavailable(err), cerrdefs.IsNotImplemented(err):
		return errs.KindDependency
	case client.IsErrConnectionFailed(err):
		return errs.KindDependency
	default:
		return errs.KindDependency
	}
}

func isClosedStream(err error) bool {
	return errors.Is(err, io.EOF) ||
		errors.Is(err, net.ErrClosed) ||
		strings.Contains(err.Error(), "use of closed network connection")
}

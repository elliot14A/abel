// Package docker implements [run.Runner] against a Docker daemon.
//
// It is the one place in abel that knows containers exist. Everything above it
// — the resolver, the use-cases, both transports — is exercised in tests
// through runfake, so this adapter is the only code that needs a daemon to
// verify, and it is covered by the build-tagged integration test beside it.
package docker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
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

// keepAlive is the container's entrypoint. abel runs each step as an exec in a
// long-lived container so that steps share a filesystem and installed packages,
// exactly as they do in a real job. `sh` is the one binary every image abel can
// run bash steps in is guaranteed to have.
var keepAlive = []string{"sh", "-c", "while :; do sleep 3600; done"}

// Config configures the runner.
type Config struct {
	// RepoRoot is the host directory mounted into the container. Required.
	RepoRoot string
	// Progress receives image-pull progress. Nil discards it.
	Progress io.Writer
	// ContainerPrefix names created containers, so a leaked one is
	// recognisable in `docker ps`.
	ContainerPrefix string
	// Pull forces an image pull even when the image is present locally.
	Pull bool
}

// Runner starts containers on a Docker daemon.
type Runner struct {
	cli *client.Client
	cfg Config
}

// New connects to the daemon described by the environment (DOCKER_HOST and
// friends) and verifies it is reachable, so that "is Docker running?" is
// answered once, at wiring time, rather than as a confusing failure three steps
// into a run.
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

	// API-version negotiation is on by default in the v29+ client.
	cli, err := client.New(client.FromEnv)
	if err != nil {
		return nil, errs.New(errs.KindDependency, opNew,
			"cannot configure a Docker client").Wrapping(err)
	}
	if _, err := cli.Ping(ctx, client.PingOptions{}); err != nil {
		_ = cli.Close()
		return nil, errs.New(errs.KindDependency, opNew,
			"cannot reach the Docker daemon — is it running? (%s)", daemonHint()).Wrapping(err)
	}
	return &Runner{cli: cli, cfg: cfg}, nil
}

// Close releases the daemon connection.
func (r *Runner) Close() error { return r.cli.Close() }

func daemonHint() string {
	if host := os.Getenv("DOCKER_HOST"); host != "" {
		return "DOCKER_HOST=" + host
	}
	return "no DOCKER_HOST set; using the default socket"
}

// Start implements [run.Runner].
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
			// Labels make a leaked container findable and bulk-removable:
			//   docker rm -f $(docker ps -aq --filter label=abel.job)
			Labels: map[string]string{
				"abel.job": plan.JobID,
				"abel.pid": strconv.Itoa(os.Getpid()),
			},
		},
		HostConfig: &container.HostConfig{
			Binds: []string{r.cfg.RepoRoot + ":" + plan.Workdir},
			// The repository is bind-mounted read-write on purpose: steps that
			// build, install or generate must behave as they do in CI. That is
			// also why `abel run` is documented as operating on your working
			// tree.
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
		// Remove the container we just created rather than leaking it.
		_ = session.Close(context.WithoutCancel(ctx))
		return nil, errs.New(kindOfDockerError(err), opStart,
			"cannot start the container for job %q", plan.JobID).
			With("job", plan.JobID).With("image", plan.Image).Wrapping(err)
	}
	return session, nil
}

// ensureImage pulls the image unless it is already present. Pull progress goes
// to Config.Progress: pulling a CI image is slow enough that silence reads as
// a hang.
func (r *Runner) ensureImage(ctx context.Context, image string) error {
	if !r.cfg.Pull {
		if _, err := r.cli.ImageInspect(ctx, image); err == nil {
			return nil
		} else if !cerrdefs.IsNotFound(err) {
			return errs.New(kindOfDockerError(err), opStart,
				"cannot inspect image %s", image).With("image", image).Wrapping(err)
		}
	}

	resp, err := r.cli.ImagePull(ctx, image, client.ImagePullOptions{})
	if err != nil {
		return errs.New(kindOfDockerError(err), opStart,
			"cannot pull %s", image).With("image", image).Wrapping(err)
	}
	defer func() { _ = resp.Close() }()

	// The pull only completes once its response body is drained.
	sink := r.cfg.Progress
	if sink == nil {
		sink = io.Discard
	}
	if _, err := io.Copy(sink, resp); err != nil {
		return errs.New(kindOfDockerError(err), opStart,
			"the pull of %s was interrupted", image).With("image", image).Wrapping(err)
	}
	return nil
}

// Session is a running container that steps execute in.
type Session struct {
	cli  *client.Client
	id   string
	plan run.Plan

	closeOnce sync.Once
	closeErr  error
}

// ID returns the container ID, for diagnostics and `docker exec` by hand.
func (s *Session) ID() string { return s.id }

// Exec implements [run.Session]. A non-zero exit code is returned as a value;
// the error is reserved for abel being unable to run the step at all.
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
	// Without a TTY the daemon multiplexes stdout and stderr; abel interleaves
	// them into one stream because that is what the user sees in CI logs.
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

// Attach implements [run.Session], handing the container's shell to the user.
//
// Putting the terminal into raw mode is the caller's job: this adapter moves
// bytes, the CLI owns the user's terminal.
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

	// Copy the user's input into the container until it closes; the output copy
	// below is what actually ends the session.
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

// shell picks bash when the image has it, falling back to sh. A CI image
// almost always has bash, and dropping the user into sh when bash exists is a
// worse debugging experience than one extra exec.
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

// Close removes the container. It is idempotent: the use-case closes on every
// path, including the one where Start already cleaned up.
func (s *Session) Close(ctx context.Context) error {
	s.closeOnce.Do(func() {
		_, err := s.cli.ContainerRemove(ctx, s.id, client.ContainerRemoveOptions{
			Force:         true,
			RemoveVolumes: true,
		})
		if err != nil && !cerrdefs.IsNotFound(err) {
			s.closeErr = errs.New(kindOfDockerError(err), opClose,
				"cannot remove container %s (remove it by hand with `docker rm -f %s`)",
				s.id[:12], s.id[:12]).With("container", s.id).Wrapping(err)
		}
	})
	return s.closeErr
}

// sessionEnv is the environment an interactive shell starts with: the first
// step's, which is the closest thing to "what the job saw".
func sessionEnv(plan run.Plan) map[string]string {
	for _, step := range plan.Steps {
		if !step.Skip {
			return step.Env
		}
	}
	return nil
}

// envSlice renders an environment map as Docker's KEY=VALUE slice, sorted so
// that two runs of the same plan produce identical exec requests.
func envSlice(env map[string]string) []string {
	if len(env) == 0 {
		return nil
	}
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	// sort.Strings without the import; the slice is small and this keeps the
	// adapter's dependency surface at the driver plus stdlib.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// nameFor builds a unique, human-recognisable container name.
//
// The random suffix is not decoration: without it, two runs of the same job —
// a re-run after `--fix`, or an agent driving the MCP server while the user
// runs the CLI — collide on the name and the second one fails to start.
func (r *Runner) nameFor(jobID string) (string, error) {
	var suffix [4]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", errs.New(errs.KindInternal, opStart,
			"cannot generate a container name").Wrapping(err)
	}
	return fmt.Sprintf("%s-%s-%s", r.cfg.ContainerPrefix, sanitise(jobID), hex.EncodeToString(suffix[:])), nil
}

// sanitise turns a job ID into something Docker accepts as a container name.
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

// kindOfDockerError maps the driver's error taxonomy onto abel's, so that the
// CLI's exit codes and the MCP server's error payloads stay meaningful for
// failures that originate in the daemon.
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

// isClosedStream recognises the ordinary end of a hijacked connection, which
// the daemon signals as an error on some platforms.
func isClosedStream(err error) bool {
	return errors.Is(err, io.EOF) ||
		errors.Is(err, net.ErrClosed) ||
		strings.Contains(err.Error(), "use of closed network connection")
}

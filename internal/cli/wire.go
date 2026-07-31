package cli

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/charmbracelet/x/term"

	"github.com/elliot14A/abel/internal/app"
	"github.com/elliot14A/abel/internal/core/errs"
	"github.com/elliot14A/abel/internal/core/run"
	"github.com/elliot14A/abel/internal/infra/docker"
	"github.com/elliot14A/abel/internal/infra/logging"
	"github.com/elliot14A/abel/internal/infra/store"
	"github.com/elliot14A/abel/internal/infra/workflowfile"
	"github.com/elliot14A/abel/internal/ui"
)

const opWire = "cli.wire"

type abel struct {
	globals   Globals
	stdio     IO
	ui        *ui.Renderer
	progress  *ui.Renderer
	log       *slog.Logger
	repoRoot  string
	workflows app.Workflows
	failures  app.FailureStore
	clock     run.Clock
	exitCode  int
	closers   []func()
}

func newAbel(g Globals, stdio IO) (*abel, error) {
	a := &abel{
		globals:  g,
		stdio:    stdio,
		ui:       ui.New(useColor(g.Color, stdio.Out)),
		progress: ui.New(useColor(g.Color, stdio.Err)),
		log:      logging.New(stdio.Err, logging.Level(g.LogLevel), nil),
		clock:    run.SystemClock,
	}

	root, err := filepath.Abs(g.Repo)
	if err != nil {
		return a, errs.New(errs.KindValidation, opWire,
			"cannot resolve --repo %q", g.Repo).Wrapping(err)
	}
	if info, statErr := os.Stat(root); statErr != nil || !info.IsDir() {
		return a, errs.New(errs.KindNotFound, opWire,
			"--repo %s is not a directory", root).With("path", root)
	}
	a.repoRoot = root
	a.workflows = workflowfile.NewLoader(root, resolveUnder(root, g.Workflows))
	a.failures = store.NewFile(resolveUnder(root, g.State))
	a.openLog(resolveUnder(root, g.State))
	return a, nil
}

func (a *abel) openLog(stateDir string) {
	level := logging.Level(a.globals.LogLevel)

	sink, err := logging.Open(filepath.Join(stateDir, logging.DefaultLogDir))
	if err != nil {
		a.log.Warn("file logging is unavailable", "error", err.Error())
		return
	}
	a.closers = append(a.closers, func() { _ = sink.Close() })
	a.log = logging.New(a.stdio.Err, level, sink).With("run", logging.RunID(), "pid", os.Getpid())
}

func resolveUnder(root, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(root, path)
}

func (a *abel) close() {
	for i := len(a.closers) - 1; i >= 0; i-- {
		a.closers[i]()
	}
}

func (a *abel) listJobs() *app.ListJobs     { return app.NewListJobs(a.workflows) }
func (a *abel) getFailure() *app.GetFailure { return app.NewGetFailure(a.failures) }
func (a *abel) markFixed() *app.MarkFixed   { return app.NewMarkFixed(a.failures, a.clock) }

func (a *abel) pullProgress() *pullPrinter {
	return newPullPrinter(a.stdio.Err, a.progress, a.clock, terminalSize(a.stdio.Err))
}

func (a *abel) runner(ctx context.Context, pull bool, progress *pullPrinter) (run.Runner, error) {
	cfg := docker.Config{RepoRoot: a.repoRoot, Pull: pull, Log: a.log}
	if progress != nil {
		cfg.Progress = progress
		a.closers = append(a.closers, progress.close)
	}

	r, err := docker.New(ctx, cfg)
	if err != nil {
		return nil, err
	}
	a.closers = append(a.closers, func() { _ = r.Close() })
	return r, nil
}

func (a *abel) runJob(ctx context.Context, pull bool, progress *pullPrinter) (*app.RunJob, error) {
	runner, err := a.runner(ctx, pull, progress)
	if err != nil {
		return nil, err
	}
	return app.NewRunJob(a.workflows, runner, a.failures, a.clock, a.log), nil
}

func (a *abel) exit(code int, err error) int {
	switch {
	case err != nil:
		a.log.Error("exit", "code", code, "kind", string(errs.KindOf(err)), "error", err.Error())
	case code != 0:
		a.log.Warn("exit", "code", code)
	default:
		a.log.Info("exit", "code", code)
	}
	return code
}

func (a *abel) errorText(err error) string {
	if a == nil || a.ui == nil {
		return "abel: " + err.Error() + "\n"
	}
	return a.ui.Error(err)
}

func useColor(mode string, out io.Writer) bool {
	switch mode {
	case "always":
		return true
	case "never":
		return false
	}
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return false
	}
	return isTerminal(out)
}

func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	return ok && term.IsTerminal(f.Fd())
}

func terminalSize(w io.Writer) termSize {
	f, ok := w.(*os.File)
	if !ok || !term.IsTerminal(f.Fd()) {
		return nil
	}
	return func() (int, int) {
		width, height, err := term.GetSize(f.Fd())
		if err != nil {
			return 0, 0
		}
		return width, height
	}
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return errs.New(errs.KindInternal, opWire, "cannot encode the response").Wrapping(err)
	}
	return nil
}

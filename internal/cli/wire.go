package cli

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"

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

// deps is the composition root: every concrete adapter, built once, injected
// into use-cases on demand.
//
// The Docker client is built lazily because most commands do not need a
// daemon, and `abel jobs` failing because Docker is not running would be
// absurd.
type deps struct {
	globals Globals
	stdio   IO
	ui      *ui.Renderer
	log     *slog.Logger

	repoRoot  string
	workflows app.Workflows
	failures  app.FailureStore
	clock     run.Clock

	// exitCode lets a command report a non-zero status without it being an
	// error — a failing workflow step is abel working correctly.
	exitCode int

	closers []func()
}

func newDeps(g Globals, stdio IO) (*deps, error) {
	d := &deps{
		globals: g,
		stdio:   stdio,
		ui:      ui.New(useColor(g.Color, stdio.Out)),
		log:     logging.New(stdio.Err, logging.Level(g.LogLevel)),
		clock:   run.SystemClock,
	}

	root, err := filepath.Abs(g.Repo)
	if err != nil {
		return d, errs.New(errs.KindValidation, opWire,
			"cannot resolve --repo %q", g.Repo).Wrapping(err)
	}
	if info, statErr := os.Stat(root); statErr != nil || !info.IsDir() {
		return d, errs.New(errs.KindNotFound, opWire,
			"--repo %s is not a directory", root).With("path", root)
	}
	d.repoRoot = root
	d.workflows = workflowfile.NewLoader(root, resolveUnder(root, g.Workflows))
	d.failures = store.NewFile(resolveUnder(root, g.State))
	return d, nil
}

// resolveUnder interprets a configured path as relative to the repository root
// unless the user gave an absolute one.
func resolveUnder(root, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(root, path)
}

func (d *deps) close() {
	for i := len(d.closers) - 1; i >= 0; i-- {
		d.closers[i]()
	}
}

func (d *deps) listJobs() *app.ListJobs     { return app.NewListJobs(d.workflows) }
func (d *deps) getFailure() *app.GetFailure { return app.NewGetFailure(d.failures) }
func (d *deps) markFixed() *app.MarkFixed   { return app.NewMarkFixed(d.failures) }

// runner builds the Docker adapter on first use and registers its cleanup.
func (d *deps) runner(ctx context.Context, pull bool, progress io.Writer) (run.Runner, error) {
	r, err := docker.New(ctx, docker.Config{
		RepoRoot: d.repoRoot,
		Progress: progress,
		Pull:     pull,
	})
	if err != nil {
		return nil, err
	}
	d.closers = append(d.closers, func() { _ = r.Close() })
	return r, nil
}

func (d *deps) runJob(ctx context.Context, pull bool, progress io.Writer) (*app.RunJob, error) {
	runner, err := d.runner(ctx, pull, progress)
	if err != nil {
		return nil, err
	}
	return app.NewRunJob(d.workflows, runner, d.failures, d.clock), nil
}

// errorText renders an error for stderr. A nil renderer (a failure during
// wiring, before the renderer exists) still produces something readable.
func (d *deps) errorText(err error) string {
	if d == nil || d.ui == nil {
		return "abel: " + err.Error() + "\n"
	}
	return d.ui.Error(err)
}

// useColor decides whether to colourise. "auto" means: only when writing to a
// terminal, and never when NO_COLOR is set — the convention every CLI should
// honour and most do not.
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
	f, ok := out.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return errs.New(errs.KindInternal, opWire, "cannot encode the response").Wrapping(err)
	}
	return nil
}

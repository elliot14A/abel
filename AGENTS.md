# AGENTS.md: abel

Instructions for any agent (or human) writing code in this repository.

---

## 0. Before you write code

**Think first.** State your assumptions. If the request admits more than one
reasonable interpretation, ask instead of guessing.

**Then read, in this order:**
1. `internal/core/errs/errs.go`, the error taxonomy everything is classified by.
2. `internal/core/run/ports.go`, the seam between the pure core and Docker.
3. `internal/app/runjob.go`, the central use-case; every transport calls it.

Three rules that override any instinct you bring with you:

- **A failing workflow step is not an error.** It is `Result.Failure` with a nil
  `error`. `error` means *abel* could not do its job. Getting this backwards
  breaks the exit codes, the MCP payloads and the entire product story.
- **Honesty over emulation.** When abel cannot reproduce something (a matrix,
  an `if:`, a `uses:` action, a macOS runner) it says so with a `run.Warning` or
  an `UNSUPPORTED` error. It never silently pretends.
- **Never leak a secret.** Every failure is redacted before it is stored,
  printed or served. See `run.Failure.Redact`.

---

## 1. Architecture: three rings, dependencies point inward

```
cmd/abel/main.go            process only: signals, streams, exit code (~20 lines)
internal/
  core/                     PURE. no I/O, no clock, no SDKs, no os
    errs/                     the Kind taxonomy + *errs.Error
    workflow/                 parse YAML -> model -> resolve into a run.Plan
    run/                      Plan, Result, Failure, LogTail + the ports
      runfake/                the in-memory Runner every test above core uses
  app/                      use-cases. framework-free. declares its own ports
  infra/                    everything impure, one package per external system
    docker/                   run.Runner over the Docker daemon
    workflowfile/             reads .github/workflows
    store/                    persists failures (File + Memory)
    agent/                    --fix shell-out
    logging/                  slog to a rotating file, and to stderr
  cli/                      transport 1: kong grammar + the composition root
  mcpserver/                transport 2: MCP tools over the same use-cases
  ui/                       human-facing rendering (pure string builders)
```

**`infra → app → core`, never the other way.** This is enforced by `depguard`
in `.golangci.yml`, so a violation is a failed build, not a review comment. If
you find yourself wanting `os`, `os/exec` or an SDK inside `core/` or `app/`,
you want **a port** instead.

**Two transports, one business path.** `cli` and `mcpserver` are both thin.
Any logic you are tempted to write in either belongs in `app/`. The test for
"is this in the right place?" is: *would the MCP server need this too?*

Keep the MCP surface level with the CLI. `plan_job` and `--dry-run` are the same
`app.RunJob.Plan` call; `run_job`'s `tail` and `timeout` are the same
`app.RunJobInput.LogTailLines` and context deadline as `--tail` and `--timeout`.
When you add a flag that changes what a run does, add it to the tool too, or say
why not.

An agent has no terminal and cannot press Ctrl-C, so anything a human gets from
the terminal has to be reachable through the tool: step output via
`output: "all"`, liveness via progress notifications, and an escape from a
hanging step via `timeout`.

---

## 2. Errors

One taxonomy, in `internal/core/errs`:

```
VALIDATION   NOT_FOUND   CONFLICT   UNSUPPORTED
DEPENDENCY_UNAVAILABLE   STEP_FAILED   CANCELLED   INTERNAL
```

- Classify at the point of failure: `errs.New(kind, op, format, args...)`.
- Attach a cause with `.Wrapping(err)`, context with `.With(key, value)`.
- `op` is `"package.Function"`. It builds a breadcrumb trail for free.
- Inspect with `errs.KindOf(err)` or `errors.Is/As`. **Never** match on message text.
- Messages are lowercase, no trailing period, no "failed to", and say what the
  user should do next.

**Adding a `Kind` means updating both mappers**, `cli.ExitCode` and
`mcpserver.agentMessage`, or the `exhaustive` linter fails the build. That is
the mechanism, not a suggestion.

Exit codes are a public contract (`internal/cli/exitcode.go`); people put abel
in pre-push hooks. Do not renumber them.

---

## 3. Testing

`go test` + hand-written fakes + `go-cmp` + native fuzzing. **No testify, no
gomock, no mockery.**

- **Unit tests are the default.** Inject `runfake.Runner` and
  `store.NewMemory()`; they run in milliseconds and need no daemon.
- **The fake is kept honest by contract tests.** `store_contract_test.go` runs
  one suite against both store adapters. `docker_integration_test.go`
  (`//go:build integration`) is the other half for `run.Runner`. If you add a
  port, add its contract suite.
- **Table-driven subtests**, `t.Parallel()`, names that are behaviour
  statements: `"returns NOT_FOUND when the job is absent"`.
- **Assert with `cmp.Diff(want, got)`**, never `reflect.DeepEqual`.
- **Assert errors structurally**: `errs.KindOf(err) == errs.KindNotFound`.
  Test **both** branches of every use-case.
- **Any new parser gets a fuzz target.** The existing one already found an
  upstream panic; its crasher is committed under `testdata/fuzz/`.
- Use `t.Context()`, `t.TempDir()`, `t.Setenv()`, `t.Cleanup()`.
- Coverage target: **~85% on `core` and `app`**. Adapters are covered by
  integration tests, and that is fine.

Never call `t.Errorf` from a goroutine that may outlive the test.

---

## 4. Dependencies

The current set is deliberate and small. **Ask before adding one.**

| Concern | Choice | Note |
|---|---|---|
| CLI | `alecthomas/kong` | the grammar *is* the typed config object |
| YAML | `goccy/go-yaml` | `gopkg.in/yaml.v3` is archived; goccy gives source positions |
| Containers | `moby/moby/client` + `moby/moby/api` | `github.com/docker/docker` is deprecated (v29 moved it) |
| MCP | `modelcontextprotocol/go-sdk` | official, v1-stable, Google co-maintained |
| Rendering | `charm.land/lipgloss/v2` | note the module path moved off `github.com/charmbracelet` for v2 |
| Terminal size | `github.com/charmbracelet/x/term` | lipgloss v2 already links it; `GetSize` bounds the pull redraw |
| Logging | stdlib `log/slog` | JSON; a rotating file under `.abel/logs`, plus stderr at `--log-level` |
| Testing | stdlib + `google/go-cmp` | |

`goccy/go-yaml` inside `core/workflow` is the **one** allowed exception to
"core imports no libraries": it is a pure decoder, and it is what makes
`workflow.Parse` fuzzable and unit-testable from bytes.

Dev tools are pinned as `tool` directives in `go.mod`. Always `go tool
golangci-lint`, never a globally installed one.

---

## 5. Rules specific to this codebase

- **stdout is sacred in `abel mcp`.** It is the JSON-RPC stream. Logs, progress
  and diagnostics go to **stderr**. One stray `fmt.Println` ends the session.
- **`--json` means stdout carries one JSON document and nothing else.**
- **Adapters report progress, they do not render it.** The Docker adapter folds
  the daemon's pull stream into `run.PullStatus` and hands it to a
  `run.PullReporter`; the glyphs live in `internal/ui` and the cursor games in
  `internal/cli`. Nothing in `infra/` may write to a terminal. Anything that
  redraws must degrade to a few plain lines when the stream is not a TTY.
  `abel mcp` and CI both land there.
- **A redraw must fit the terminal, or it corrupts the screen.** Cursor-up
  arithmetic assumes one line is one row, so a painted block is clipped to the
  live `term.GetSize` width *and* height. Truncate in `internal/ui`, on the
  plain text, before styling: cutting a rendered string would split an ANSI
  escape.
- **Every container abel creates must be removed**, including on Ctrl-C.
  Cleanup runs on `context.WithoutCancel` with its own timeout, and containers
  carry `abel.job` / `abel.pid` labels so a leak is findable.
- **Container names need their random suffix.** Without it, two runs of the
  same job collide. The integration test caught exactly this.
- **`Redact` before `Put`, and again on read.** Defence in depth: a record
  written by an older abel still must not leak.
- **Captured step output is raw command output, so redact it.** `run.RedactLines`
  is the same secret set `Failure.Redact` uses. Anything that leaves the process
  carrying what a step printed goes through it.
- **A step's name is its command, so it is never safe to record raw.** An
  unnamed `run:` step takes its label from the script's first line, which may
  contain an inline secret. Pass it through `run.RedactText(name, step.Env)`
  before it is logged, stored or served. `Failure.Redact` does the same for
  `StepName`, `Command` and `LogTail`.
- **Logging must never fail a run.** The rotating sink swallows its own write
  and rotation errors; if the log directory cannot be opened, abel warns once
  and carries on. A broken log is not a broken build.
- **Skipped steps keep their index.** "step 3" must mean the same thing in the
  CLI, the logs, the MCP payload and the workflow file.
- **The one `recover` in the codebase** is in `workflow.Parse`, guarding against
  a known upstream decoder panic on malformed YAML. Do not copy the pattern
  inward.
- `abel run` mutates the working tree, exactly as CI would. Say so wherever a
  user or an agent could be surprised.

---

## 6. Before you say you are done

```
make check          # vet + format + lint (incl. the depguard ring rules) + test + vuln
make test-integration   # needs a running Docker daemon
```

Both must be green. If you changed the parser, also run `make fuzz`.

Then check yourself against the three rules in §0.

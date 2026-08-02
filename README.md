# abel

> Reproduce a failing CI job locally and hand it to your coding agent, over MCP.

`abel` runs a GitHub Actions job's `run:` steps in the container the job
declares, streams the logs, and captures the failure context when a step fails:
the failing step, its command, the exit code, the tail of its output, and the
line in the workflow file.

That context exists to be handed to an agent. `abel mcp` serves it over MCP, so
an agent can list the jobs, plan one without touching your tree, run it, read
why it failed, fix it, and run it again to check. It sees structured JSON rather
than scraped terminal output, gets progress while a job runs, and can bound a
job that might not terminate. Secrets are redacted before any of it leaves the
process.

The CLI is the same tool for when you want to drive it yourself. Both go through
one code path, so they cannot disagree about what your CI does.

```console
$ abel run lint
abel lint  catthehacker/ubuntu:act-latest
3 step(s) to run, 1 skipped, from .github/workflows/ci.yml
! line 9: skipped `actions/checkout`: your working tree is already mounted
✓ pulled catthehacker/ubuntu:act-latest 540 MB in 41.2s
- step 1 skipped `actions/checkout`: your working tree is already mounted

▸ step 2 install
✓ install 4.2s

▸ step 3 typecheck
src/app.ts(3,1): error TS2304: Cannot find name 'foo'.
✗ typecheck exit 2 in 1.8s

FAIL lint failed at step 3 (typecheck) in 6.1s, 2 step(s) run, 1 skipped

failure context
  job       lint
  step      3: typecheck
  command   tsc --noEmit
  exit      2
  image     catthehacker/ubuntu:act-latest
  source    .github/workflows/ci.yml:12

  last 2 line(s):
  │ src/app.ts(3,1): error TS2304: Cannot find name 'foo'.
  │ Found 1 error.
```

## Install

```sh
go install github.com/elliot14A/abel/cmd/abel@latest
```

Or grab a binary from [Releases](https://github.com/elliot14A/abel/releases).
abel needs a running Docker daemon and reads `DOCKER_HOST` like every other
Docker tool.

## Use

```sh
abel jobs                        # what can abel reproduce?
abel run lint                    # reproduce a job, stream the logs
abel run lint --dry-run          # resolve and print the plan; no daemon needed
abel run lint --shell            # drop into the container when it finishes
abel run lint --image alpine:3   # override the image
abel failure lint                # re-read the last captured failure
abel failure lint --json         # the exact payload agents receive
abel mcp                         # serve failures to an agent over stdio
abel run lint --fix claude-code  # hand the failure to an agent, then re-run
```

### Driving abel from a coding agent

`abel mcp` speaks MCP over stdio. Register it with your agent:

```json
{
  "mcpServers": {
    "abel": { "command": "abel", "args": ["mcp", "--repo", "/path/to/repo"] }
  }
}
```

Five tools:

| tool | what it does |
|---|---|
| `list_jobs` | the jobs abel can reproduce, which workflow each is in, and whether abel can run it |
| `plan_job` | what a job would run: image, steps, skip reasons, warnings. Starts no container |
| `run_job` | reproduce a job; returns the result and, on failure, the context |
| `get_failure` | the last captured failure: step, command, exit code, log tail, env |
| `mark_fixed` | record a claimed fix and what changed; `run_job` is what verifies it |

`run_job` executes your workflow's commands against the working tree, so an
agent that wants to know what a job does first should call `plan_job`, which is
read-only. `run_job` also takes:

- `output: "all"` to return what every step printed, not just the failing one
- `tail` to raise the 200-line default
- `timeout` in seconds, which you want for any job that might not terminate,
  since a hanging step otherwise blocks the call

It reports progress per step when the client sends a progress token.

The loop is: detect, serve, the agent fixes, re-verify, you review the diff.
abel never commits anything, and never runs the agent beyond the single
invocation you asked for.

### Logs

Every run appends NDJSON to a rotating log. Read it with
`tail -f .abel/logs/abel.jsonl`. The file records everything; `--log-level`
(`ABEL_LOG_LEVEL`, default `warn`) controls what is also mirrored to stderr.
Secrets are redacted, as they are in the failure context.

### Exit codes

These are a contract. Put abel in a pre-push hook and branch on them.

| code | meaning |
|---|---|
| `0` | every step passed |
| `1` | a workflow step failed (abel worked) |
| `2` | usage: bad flags, or an invalid workflow file |
| `3` | not found: unknown job, no workflows, no captured failure |
| `4` | conflict |
| `5` | unsupported: abel knowingly does not implement this |
| `6` | dependency unavailable, usually the Docker daemon |
| `70` | a bug in abel |
| `130` | interrupted |

## What abel does and does not do

Supported:

- `run:` steps
- `runs-on:` mapped to a local image, and `container:`
- all three layers of `env:`
- `defaults.run` and per-step `working-directory`
- `bash` and `sh`
- the real container, with state carried between steps
- secret redaction and the failure context

Unsupported, and reported every time:

- `uses:` actions. `checkout` is skipped because your tree is already mounted;
  `setup-*` and `cache` are skipped with a reason
- matrices, `if:` conditions, and `needs:` ordering
- `${{ }}` expressions
- macOS and Windows runners
- services and artifacts

Each of those produces a warning on the plan or an `UNSUPPORTED` error. abel
tells you it cannot reproduce something instead of reproducing it wrongly.

> `abel run` executes your workflow's commands against your working tree,
> read-write, exactly as CI would. That is the point, and worth knowing before
> you point it at a step that runs `rm -rf`.

## How it is built

Hexagonal, three rings, dependencies pointing inward:

```
cmd/abel/           process entry: signals, streams, exit code
internal/core/      pure: the workflow model, resolution, the failure model
internal/app/       use-cases: RunJob, GetFailure, MarkFixed, ListJobs
internal/infra/     Docker, YAML files, the failure store, logging, the --fix agent
internal/cli/       transport 1, and the composition root
internal/mcpserver/ transport 2, the same use-cases with no logic of its own
```

Two transports over one business path is the reason for the structure: the CLI
and the MCP server cannot drift, because there is only one implementation. The
rings are enforced by `depguard` in CI, so importing `os` into `core/` fails
the build.

The core is pure, so it is tested with injected fakes and no daemon. The Docker
adapter is tested against a real daemon by a build-tagged integration suite. One
contract suite runs against both the fake and the real store to keep the fake
honest. The workflow parser is fuzzed, which is how the panic it now guards
against was found.

See [AGENTS.md](./AGENTS.md) for the conventions.

## Development

```sh
make help              # list targets
make build             # ./bin/abel
make check             # vet + format + lint + test + govulncheck, what CI runs
make test-integration  # the Docker adapter, against a real daemon
make fuzz              # the workflow parser, 60s
```

Dev tools are pinned in `go.mod` as `tool` directives, so `make lint` runs the
same linter version CI does. Go 1.26+.

## Licence

MIT.

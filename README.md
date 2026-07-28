# abel

*Named after Abel (The Weeknd) — soundcheck before the show.*

> Run your CI locally before you push — soundcheck the pipeline before you go live.

`abel` reproduces a GitHub Actions job's `run:` steps **locally, in the real
container**, streams the logs, and — when a step fails — captures the failure
context and serves it to your coding agent over MCP. Then you re-run.

It kills the push-wait-fail loop. It is not an `act` clone: it is a **fast
CI-debug loop** aimed at the part that actually breaks.

```console
$ abel run lint
abel lint  catthehacker/ubuntu:act-latest
3 step(s) to run, 1 skipped, from .github/workflows/ci.yml
! line 9: skipped `actions/checkout`: your working tree is already mounted
- step 1 skipped `actions/checkout`: your working tree is already mounted

▸ step 2 install
✓ install 4.2s

▸ step 3 typecheck
src/app.ts(3,1): error TS2304: Cannot find name 'foo'.
✗ typecheck exit 2 in 1.8s

FAIL lint failed at step 3 (typecheck) in 6.1s — 2 step(s) run, 1 skipped

failure context
  job       lint
  step      3 — typecheck
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
abel needs a running Docker daemon; it reads `DOCKER_HOST` like every other
Docker tool.

## Use

```sh
abel jobs                        # what can abel reproduce?
abel run lint                    # reproduce a job, stream the logs
abel run lint --dry-run          # resolve and print the plan; no daemon needed
abel run lint --shell            # drop into the container when it finishes
abel run lint --image alpine:3   # override the image
abel failure lint                # re-read the last captured failure
abel failure lint --json         # …as the exact payload agents receive
abel mcp                         # serve failures to an agent over stdio
abel run lint --fix claude-code  # hand the failure to an agent, then re-run
```

### With a coding agent (the point of the tool)

`abel mcp` speaks MCP over stdio. Register it with your agent:

```json
{
  "mcpServers": {
    "abel": { "command": "abel", "args": ["mcp", "--repo", "/path/to/repo"] }
  }
}
```

Four tools, the shared **`agentfix`** contract abel implements alongside
[`mob`](https://github.com/elliot14A/mob):

| tool | what it does |
|---|---|
| `list_jobs` | the jobs abel can reproduce, and which workflow each is in |
| `run_job` | reproduce a job; returns the result and, on failure, the context |
| `get_failure` | the last captured failure: step, command, exit code, log tail, env |
| `mark_fixed` | record a claimed fix — `run_job` is what verifies it |

The loop is **detect → serve → the agent fixes → re-verify, you review the
diff.** abel never commits anything and never runs the agent unattended beyond
the single invocation you asked for.

### Exit codes

They are a contract — put abel in a pre-push hook and branch on them.

| code | meaning |
|---|---|
| `0` | every step passed |
| `1` | a workflow step failed (abel worked) |
| `2` | usage: bad flags, or an invalid workflow file |
| `3` | not found: unknown job, no workflows, no captured failure |
| `4` | conflict |
| `5` | unsupported: abel knowingly does not implement this |
| `6` | dependency unavailable — usually the Docker daemon |
| `70` | a bug in abel |
| `130` | interrupted |

## What abel does and does not do

**Does:** `run:` steps · `runs-on:` → a local image · `container:` · the three
layers of `env:` · `defaults.run` and per-step `working-directory` · `bash`
and `sh` · the real container, with state carried between steps · secret
redaction · the failure context.

**Does not — and says so, every time:** `uses:` actions (`checkout` is skipped
because your tree is already mounted; `setup-*` and `cache` are skipped with a
reason) · matrices · `if:` conditions · `needs:` ordering · `${{ }}`
expressions · macOS and Windows runners · services and artifacts.

Every one of those produces a warning on the plan or an `UNSUPPORTED` error.
abel would rather tell you it cannot reproduce something than reproduce it
wrongly.

> `abel run` executes your workflow's commands against your **working tree**,
> read-write, exactly as CI would. That is the point — and worth knowing before
> you point it at a step that runs `rm -rf`.

## How it is built

Hexagonal, three rings, dependencies pointing inward:

```
cmd/abel/          process entry: signals, streams, exit code
internal/core/     pure: the workflow model, resolution, the failure model
internal/app/      use-cases: RunJob, GetFailure, MarkFixed, ListJobs
internal/infra/    Docker, YAML files, the failure store, the --fix agent
internal/cli/      transport 1 — and the composition root
internal/mcpserver/ transport 2 — the same use-cases, no logic of its own
```

Two transports over one business path is the reason for the structure: the CLI
and the MCP server cannot drift, because there is only one implementation. The
rings are enforced by `depguard` in CI, so importing `os` into `core/` fails
the build.

The core is pure, so it is tested with injected fakes and no daemon; the Docker
adapter is tested against a real daemon by a build-tagged integration suite;
and one contract suite runs against both the fake and the real store to keep
the fake honest. The workflow parser is fuzzed — which is how the panic it now
guards against was found.

See [AGENTS.md](./AGENTS.md) for the conventions, and
[GO-STANDARDS.md](https://github.com/elliot14A/standards) for the house standard
this instantiates.

## Development

```sh
make help              # list targets
make build             # ./bin/abel
make check             # vet + format + lint + test + govulncheck — what CI runs
make test-integration  # the Docker adapter, against a real daemon
make fuzz              # the workflow parser, 60s
```

Dev tools are pinned in `go.mod` as `tool` directives, so `make lint` runs the
same linter version CI does. Go 1.26+.

## Licence

MIT.

<div align="center">
  <img src="assets/hamster-logo.png" alt="Hamster sprinting furiously in a wheel" width="220" />

  <h1>Hamster</h1>

  <p>A small language and runtime for enforceable, resource-bounded Codex loops.</p>

  <p>
    <a href="https://github.com/kavishsathia/hamster/actions/workflows/ci.yml"><img src="https://github.com/kavishsathia/hamster/actions/workflows/ci.yml/badge.svg" alt="CI status" /></a>
    <img src="https://img.shields.io/badge/status-experimental-C56A32?style=flat-square" alt="Status: experimental" />
    <img src="https://img.shields.io/badge/Go-1.25-00ADD8?style=flat-square&logo=go&logoColor=white" alt="Go 1.25" />
    <img src="https://img.shields.io/badge/runtime-Codex-22272E?style=flat-square" alt="Runtime: Codex" />
    <img src="https://img.shields.io/badge/docs-Starlight-5B45D6?style=flat-square&logo=astro&logoColor=white" alt="Documentation: Starlight" />
    <a href="LICENSE"><img src="https://img.shields.io/badge/license-Apache--2.0-D22128?style=flat-square&logo=apache" alt="License: Apache 2.0" /></a>
  </p>
</div>

The project is bootstrapping its trusted kernel. Design decisions and live
Codex hook findings are recorded in [`discussions`](discussions/).

Contributions are welcome. Read [CONTRIBUTING.md](CONTRIBUTING.md), the
[Code of Conduct](CODE_OF_CONDUCT.md), and the [Security policy](SECURITY.md)
before opening an issue or pull request.

## Development

```sh
go test ./...
go run ./cmd/hamster version
```

## Bootstrap CLI

Build the native binary and initialize a trusted repository:

```sh
go build -o bin/hamster ./cmd/hamster
bin/hamster init
```

`init` creates `.hamster/scripts`, preserves and extends any existing
`.codex/hooks.json`, and prints the Codex hook-trust step. Inside Codex, the
agent starts a repository loop with:

```sh
hamster run build-provider
hamster status
hamster signal goal-achieved
```

Declarative work advances only through the explicit completion signal. Ordinary
tool calls leave the goal parked. Hamster then runs its independent verifiers;
success completes the loop, while failure reports the named verifier and starts
the next iteration with remediation for the agent.

If a loop is preventing work needed to repair Hamster itself, use the
high-priority emergency brake:

```sh
hamster kill --reason "repair Hamster runtime"
```

The pre-tool hook handles `kill` before normal loop policy. It persistently
marks the bound run as killed and removes the current agent binding, so later
tool calls are no longer governed by that run. Outside Codex, identify the
binding explicitly with `hamster kill --session <session-id>`.

The pre-tool hook owns registration because it already has the Codex session
and agent identity. For local testing outside Codex, bind explicitly:

```sh
hamster run --session test-session .hamster/scripts/build-provider.hmstr
hamster status --session test-session
```

Runtime state is owned by one per-user daemon in `~/.hamster/hamster.db`. The
current bootstrap supports persistent `goal`, `flow`, nested loops, `foreach`,
prospective time and attempt limits, `allow`/`disallow`, exact `bash.run`
steps, independent `bash.check` verifiers, and the first process kernel.

When a bound Codex agent spawns a subagent, Hamster snapshots the complete run
at the spawn boundary and binds the new `agent_id` on `SubagentStart`. A child
can replace its inherited machine without changing process identity, and its
parent can wait durably for the structured result:

```sh
hamster exec child-workflow
hamster wait <child-run-id>
```

Normal loop `return` completes the child, clears its binding, and wakes a
waiting parent. `exec` clears the old machine stack and machine-local limits;
`wait` currently accepts direct children. Simultaneous pending subagents use
observed start order until the fork-token correlation protocol is implemented.

## Runtime observability

The daemon hosts a local operational console on a loopback-only HTTP listener.
Print its current address with:

```sh
hamster ui
```

The console shows every known Codex session and its agent process tree. A run
detail view exposes the current machine and state, iteration, nested stack,
activated limits, recent hook decisions, paired tool durations, and persisted
verification evidence. It polls the local daemon directly; no runtime data is
sent to an external service and no separate frontend process is required.

The default listener is `127.0.0.1:7331`. Use
`hamster daemon serve --http 127.0.0.1:0` when an ephemeral port is preferable;
the resolved URL is still available through `hamster ui`.

## Language and integration reference

Hamster ships an offline reference catalog. It records syntax, preconditions,
postconditions, guarantee levels, verification evidence, stable failures, and
tested examples for every current language and process primitive:

```sh
hamster man fork
hamster man --search "child process"
hamster man --list
```

The separately hosted documentation website is generated from that same
catalog; it is not part of the runtime daemon or observability console. Source
and the Astro + Starlight adapter live under `docs/`. A deployment supplies its
public origin through `SITE_URL` and builds the static site with
`pnpm --dir docs/site run build`.

## Codex trace fixtures

Hamster can record the hook stream directly from Codex. Inside a Codex session:

```sh
hamster trace record --name parallel-provider-build
# exercise the workflow
hamster trace stop
hamster trace export --id <trace-id> --output testdata/traces
```

The pre-tool hook supplies the session identity and detects the exact installed
Codex version. Exports are sanitized and automatically organized as:

```text
testdata/traces/<codex-distribution>-<exact-version>/<scenario>/
```

Replay a fixture without launching Codex:

```sh
hamster trace replay testdata/traces/codex-cli-0.144.5/parallel-subagents
```

The committed `codex-cli 0.144.5` corpus covers parallel subagents, a nested
subagent, native web search, shell calls, spawn/wait operations, lifecycle
events, and stable parent/child identities. Runtime replay uses an injectable
clock and queued verifier results, so tests do not sleep or execute captured
commands.

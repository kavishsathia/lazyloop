# Hamster

Hamster is a small language and runtime for enforceable, resource-bounded
Codex loops.

The project is bootstrapping its trusted kernel. Design decisions and live
Codex hook findings are recorded in [`discussions`](discussions/).

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
```

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

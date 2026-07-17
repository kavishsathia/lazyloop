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

The pre-tool hook owns registration because it already has the Codex session
and agent identity. For local testing outside Codex, bind explicitly:

```sh
hamster run --session test-session .hamster/scripts/build-provider.hmstr
hamster status --session test-session
```

Runtime state is owned by one per-user daemon in `~/.hamster/hamster.db`. The
current bootstrap supports persistent `goal`, `flow`, nested loops, `foreach`,
prospective time and attempt limits, `allow`/`disallow`, exact `bash.run`
steps, and independent `bash.check` verifiers. Native provider integrations
and fork/exec/wait are intentionally left for Hamster-supervised follow-up
work.

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

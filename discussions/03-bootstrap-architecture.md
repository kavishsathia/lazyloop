# Hamster Bootstrap Architecture

Date: 17 July 2026

## Objective

Build the smallest trusted Hamster kernel that can supervise Codex while Codex
uses Hamster loops to build the remaining providers and language features.

The kernel must be useful rather than demonstrative: state transitions,
verification, identity bindings, resource limits, and recovery are runtime
facts rather than agent assertions.

## Current language model

Hamster has three loop forms:

- `goal` is declarative. Codex chooses its actions until every verification
  succeeds or a limit terminates the loop.
- `flow` is imperative. Codex performs ordered `step` declarations and the
  flow repeats until every verification succeeds or a limit terminates it.
- `foreach` applies one `goal` or `flow` to every value in a stable input
  sequence. Each element must complete independently. The inner loop's return
  value becomes that element's result.

Only a `flow` contains `step` declarations. A step may invoke a primitive or a
nested `goal`, `flow`, or `foreach` loop.

`verify` is executed by Hamster, not Codex. `return` declares the value emitted
after all required verifications succeed; it does not bypass verification.

Limits are prospective: a limit begins accounting when execution first
crosses its declaration. Earlier work is not charged to it.

## Bootstrap stages

### H0: Foundation

- Go module and native `hamster` binary.
- Versioned packages and test harness.
- Written language, runtime, and daemon boundaries.

### H1: Language front end

- Lexer with source spans.
- Parser and AST for the bootstrap grammar.
- Actionable diagnostics.
- Formatter and richer type checking are deferred.

### H2: State-machine compiler

- Compile source into a versioned, serializable IR.
- Distinguish agent-executed steps from runtime-executed verifications.
- Preserve nested loop structure and prospective limit activation points.

### H3: Central runtime service

- One per-user daemon owns `~/.hamster/hamster.db`.
- All CLI and hook processes communicate with it over local IPC.
- SQLite WAL mode with atomic, idempotent event processing.
- Lock transitions per run rather than globally.

### H4: Codex hooks and identity

- Install project hook configuration.
- Normalize root identity to `ROOT`.
- Bind `(session_id, agent_id | ROOT)` to a stable Hamster process.
- Ingest tool and subagent lifecycle events.
- Return allow/deny decisions with machine-readable remediation.

### H5: Loop runtime

- Execute and resume goal and flow machines.
- Run independent verifiers.
- Track prospective time and attempt limits.
- Persist evidence, transitions, results, and terminal status.
- Fail closed for active runs when an event cannot be authorized.

### H6: Dogfooding surface

- `hamster init`, `run`, `status`, and `inspect`.
- Repository, personal, and ephemeral script resolution.
- A bootstrap workflow that builds providers using raw command verifiers.
- Native fork/exec/wait and provider integrations follow once the kernel can
  supervise their construction.

## Process and storage model

The daemon owns a stable process identity independently of the machine it
currently executes:

```text
(session_id, agent_id | ROOT) -> process_id
process_id                     -> current_run_id
run_id                         -> machine + stack + limits + state
```

This separation lets `fork` create a process and lets `exec` replace its
machine without changing its identity.

Repository files contain shareable definitions only:

```text
.hamster/
  config.toml
  scripts/*.hmstr
```

The central runtime directory contains private definitions and operational
state:

```text
~/.hamster/
  hamster.db
  run/hamster.sock
  scripts/*.hmstr
```

## Daemon boundary

The daemon is the only database writer. CLI and hook invocations are clients.
Every request has a unique identifier, and consuming a hook event, changing a
machine, and recording the resulting decision occur in one transaction.

The initial IPC protocol is versioned, length-delimited JSON over a Unix
socket. A named-pipe transport can implement the same interface on Windows.

Hooks attempt to start or reconnect to the daemon. A hook associated with an
active Hamster run fails closed if the daemon cannot make an authorization
decision.

## Bootstrap trust boundary

New providers cannot verify themselves. Until a provider passes an external
conformance workflow, Hamster verifies it with the trusted command primitive.

```text
trusted kernel
  parser -> compiler -> store -> daemon -> hook -> verifier

Hamster-built surface
  git -> docker -> codex -> lsp -> parallel orchestration -> richer tooling
```

The initial command primitive keeps agent actions and runtime checks distinct:

```hmstr
step tests = bash.run("go test ./...");
verify tested = bash.check("go test ./...");
```

The first call is an observable action requested from Codex. The second is an
independent check executed and recorded by Hamster.

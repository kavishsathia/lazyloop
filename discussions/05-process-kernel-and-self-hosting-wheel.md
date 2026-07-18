# Process kernel and the self-hosting wheel

## Why this came before integrations

Hamster needs one trustworthy single-agent wheel before it can ask several
Hamster processes to build Git, Docker, Codex, and LSP integrations in
parallel. The bootstrap workflow is stored at:

```text
.hamster/scripts/build-process-kernel.hmstr
```

It creates `hamster/process-kernel`, requires changed tests that fail before
implementation, then loops until the named process contracts, the full Go test
suite, `go vet`, and a clean branch commit all succeed.

The workflow also depends on a higher-priority escape hatch:

```sh
hamster kill --reason "repair Hamster runtime"
```

Kill is evaluated before ordinary machine policy. It marks the run `killed`,
records the reason, and atomically removes the process binding.

## Fork

A native Codex `collaborationspawn_agent` pre-tool event creates the fork
snapshot. The child receives a new process and run while cloning:

- The current machine and state pointer.
- Nested loop stack.
- Variables and activated limits.
- Current iteration.
- Evidence available through the persisted run state.
- Parent ancestry.

The spawn tool does not expose the future `agent_id`, so the process initially
uses a private pending identity. Hamster uses the documented Codex
`PreToolUse.updatedInput` response to append a one-time `hamster attach` command
to the child message. `SubagentStart` supplies context but never guesses a
binding. Until the child presents its token, Hamster quarantines its other tool
calls. The atomic token claim replaces the pending identity with the stable
child `agent_id`.

This removes the ordering assumption from the Codex 0.144.5 fixture corpus.
Concurrent children may attach in any order, and replaying either spawn or
attach remains idempotent.

## Exec

`hamster exec SCRIPT` is a hook-owned control operation. It preserves the
process ID, run ID, ancestry, and Codex binding while replacing:

- The root machine.
- Current state pointer.
- Nested stack.
- Variables passed to the replacement program.
- Machine-local iteration and verification state.

The runtime pumps control and verification states before returning control to
Codex. The implementation currently clears all persisted run-limit rows on
exec. A later budget model must distinguish process-wide cost/time allocation
from machine-local retry limits so the former survives exec.

## Wait and return

`hamster wait CHILD_RUN_ID` accepts only a direct child. If the child is still
running, the parent becomes `waiting` and its Codex binding remains intact.
Ordinary policy denies further agent work while that status is active, but the
kill switch remains available.

A loop's existing `return` is the structured process return. Normal machine
completion persists the value, unbinds the terminal child, completes matching
wait records, and makes the parent runnable. An explicit process return is
idempotent when replayed with the same JSON value and rejects a conflicting
second value.

Wait state is stored in SQLite rather than in a blocked goroutine, so daemon
and computer restarts do not lose the relationship.

## Kernel boundary

This milestone deliberately exposes a small kernel:

```text
Codex spawn -> automatic fork -> child exec -> parent wait -> child return
```

The daemon also exposes versioned `process.fork`, `process.exec`,
`process.wait`, and `process.return` requests for deterministic tests and
future language lowering. Higher-level `join`, `race`, cancellation,
supervision, resource allocation, and simultaneous-spawn tokens remain outside
this kernel.

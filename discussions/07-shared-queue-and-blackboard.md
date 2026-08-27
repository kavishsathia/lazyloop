# Shared queue and blackboard

> Design proposal. Nothing described here is implemented yet.

Date: 27 August 2026

## Problem

Hamster coordinates several agents today, but only along lines of descent.
`fork` snapshots a parent, `exec` replaces the child's machine, and `wait`
suspends the parent until a direct child returns. Coordination is by
lineage: the parent must exist, must have spawned the worker, and must still be
alive to receive the result.

That is the right primitive for a parent that needs an answer, and the wrong
one for a team. A worker that finishes one task and wants another has no way
to find it. Two agents that both discover something about the workspace have
no place to leave it. A root agent that wants to hand off work and stop has
to stay alive as a `wait`ing parent. Every one of these is a coordination
through shared, persistent state, and Hamster has no such state that a loop
can read or write.

The daemon already owns a durable store (`~/.hamster/hamster.db`) and already
repairs orphaned forks, waits, and traps on every open. What is missing is not
persistence. It is two structures whose semantics the kernel can enforce.

## Two structures, deliberately only two

- A **queue** hands work between agents. An item is pushed by one run and
  claimed by another, possibly in a different Codex session, possibly hours
  later.
- A **board** shares state between agents. A blackboard: any run can read
  every key, and writes are governed.

Both are workspace-scoped and outlive any run. Neither is a general-purpose
persistent data structure. A queue whose consumption is not governed is a todo
list; a map without an audit trail is a Redis hash. What makes each of these a
Hamster feature is that the existing `verify`, `allow`/`disallow`, `limit`,
`trap`, and `judge` vocabulary applies to it unchanged.

The general principle: add the structure whose use Hamster can enforce, and
resist the ones it can only store.

## Queue

### Language surface

`foreach` already exists. A queue is a `foreach` over a shared, growing,
concurrently consumed collection. Iterating it means claiming an item; the
inner loop governs the work; the inner loop's `return` completes the item.

```hmstr
foreach task in queue("review") {
  goal {
    limit attempts 3;
    verify tests_pass = bash.check("go test ./...");
    judge "the change does what task.payload asked for";
    return tests_pass;
  }
}
```

The grammar does not change. `queue(...)` is an ordinary call expression; only
its iteration semantics differ from a literal list.

The producer side is a builtin namespace beside `bash.*`:

```hmstr
step queued = queue.push("review", { branch: "feature/x", request: "..." });
```

A push is a hook-visible tool call. It is recorded as an event attributed to the
run that made it, so the queue's history is auditable through the existing
observability console without new machinery.

### The loop lives with the consumer

The item carries a payload. It does not carry a loop. The worker's own script
decides what governs the work it claims.

The alternative — a queue item that says "run me under this loop" — makes the
loop untrusted input. The loop is the trust kernel. A producer that could ship
an arbitrary loop through the queue could ship a loop with no verifiers, and
the worker would enforce nothing while appearing to enforce something. That
failure mode is exactly the one the playground was built to expose, and it
should not be reintroduced through a side door.

### Leases, not pops

A claim does not remove an item. It leases the item to the claiming run:

```text
queue_items
  id            text primary key
  workspace_id  text not null
  queue         text not null
  payload       json not null
  status        text not null   -- ready | leased | done | failed
  leased_by     text            -- run id
  leased_at     text
  attempts      integer not null default 0
  result        json
  created_at    text not null
```

- Inner loop `return` marks the item `done` and stores the return value as its
  result. An explicit return is idempotent under replay, as process returns are
  today.
- A run that ends any other way — killed, failed, limit exhausted, or simply
  gone — releases the lease. The item returns to `ready` with `attempts`
  incremented. This is the same repair `Recover()` already performs for forks
  and waits; the queue is one more table it walks on open.
- Items are delivered oldest first, but delivery is at-least-once. This is the
  honest guarantee. Hamster already relies on idempotent replay for hook
  control decisions and fork keys; a queue built on the same assumption is
  consistent with the rest of the kernel.

### Bouncing items

An item that keeps coming back is a signal, not noise. The consumer's script
should be able to bound it:

```hmstr
foreach task in queue("review") {
  goal {
    limit attempts 3;
    ...
  }
}
```

`limit attempts` on the inner goal already bounds one run's retries. A second,
queue-level limit bounds how many runs may try the same item before it is
parked as `failed` and a trap fires:

```text
"review" item 3f9c has failed 3 workers; drop it, retry it, or hand it to me?
```

This reuses trap suspension and `limit traps` exactly as they exist.

### Cross-session identity

This is the genuinely new capability, and the part with the most design work.

Today a hook is bound to a run because the run was started in that Codex
session: identity is `(codex_session_id, agent_id)` and registration happens in
the pre-tool hook. A worker consuming a queue may be in a different session
entirely. Its binding must come from the claim, not from a parent.

Proposed: a worker binding mode.

```sh
hamster work review
```

binds the calling agent to a `foreach`-over-queue machine whose inner loop is
taken from the workspace's script. Each successful claim creates a run under
that binding; each item's completion unbinds it and the machine pumps to the
next claim, exactly as `foreach` advances today. If the queue is empty the
worker's status is `waiting`, with the same policy consequence as a parent
waiting on a fork: ordinary agent work is denied, kill remains available.

`fork` is unchanged. A parent that needs an answer from a specific child still
forks and waits. The queue is for work nobody in particular is waiting on.

## Board

### Language surface

```hmstr
step _ = board.set("status.worker-3", "reviewing 3f9c");
verify all_done = board.get("workers.remaining") == 0;
```

Reads are expressions; writes are steps. Both are builtins in a `board.*`
namespace. Reads are not tool calls and are not recorded; writes are tool
calls and are.

### What the existing vocabulary does with it

**Writes are events.** Every `board.set` lands in `events` with the run,
machine, and state that produced it. The board is fully auditable. "Who wrote
`decision.approach`, and under what loop?" is answered by the console without
new tables.

**Policies apply to keys.** `allow` and `disallow` today name tools. Extending
them to name a namespace with a key pattern lets a loop say what its agent may
touch:

```hmstr
allow board.set("status.*");
disallow board.set("decision.*");
```

A worker can report. It cannot decide. This is the single feature that makes a
shared board safe with many agents on it, and it is a small extension of a
policy mechanism that already exists.

**Verifiers can read it.** A goal on one agent can gate on state written by
others. This is the multi-agent equivalent of `wait` without lineage: a
coordinator that says `verify all_done = board.get("workers.remaining") == 0`
completes when the team is finished, regardless of who spawned whom.

**Traps can be keyed on it.** A contested key — two runs writing different
values to `decision.approach` inside one lease window — is a conflict Hamster
cannot resolve. It is precisely the kind of decision `trap` exists for.

### Versioning

A board with concurrent writers wants compare-and-swap, or last-writer-wins
silently eats a peer's work in exactly the situation the board is for.

```text
board_entries
  workspace_id  text not null
  key           text not null
  value         json not null
  version       integer not null
  written_by    text not null   -- run id
  written_at    text not null
  primary key (workspace_id, key)
```

```hmstr
step claimed = board.set("decision.approach", "sentinel", if_version: 4);
```

A write with `if_version` fails if the stored version differs. The failure is a
normal step outcome the loop can branch on, not an evaluator error.
Unconditional writes remain available for keys where overwriting is the point
(`status.*`).

## Scope and lifetime

Both structures hang off the existing `workspaces` table. A user-scoped queue or
board — one shared across repositories — is a reasonable later addition but is
not the default. Cross-workspace coordination is a bigger trust question than
cross-agent coordination and should be argued for separately.

Unlike forks, waits, and traps, queue items and board entries are meant to
outlive runs. `Recover()` releases dead leases; it does not delete items or
entries. Explicit clearing is an administrative CLI action:

```sh
hamster queue drain review
hamster board clear "status.*"
```

## What this does not add

- No maps, sets, or lists beyond the two above. Each additional structure
  should arrive with the loop that needed it.
- No loop-in-the-queue. Items are data.
- No exactly-once delivery. Idempotency is the consumer's job, as it is
  everywhere else in Hamster.
- No change to `fork`/`exec`/`wait`. Lineage coordination and shared-state
  coordination are different tools for different shapes of work.

## Open questions

1. Should `queue(...)` in a `foreach` require the worker binding, or may an
   ordinary run's flow iterate a queue as one step among others? The former is
   simpler to make correct; the latter is more expressive.
2. Key-pattern policies: glob or prefix? Prefix is enough for every example
   here and is trivially indexable.
3. When a worker is `waiting` on an empty queue, should it hold its Codex
   binding indefinitely, or should `limit time` apply and release it? Holding
   an idle agent has a cost.
4. Does a board read inside a `verify` need to be snapshot-consistent with the
   other verifiers in the same ladder, or is per-read consistency enough?

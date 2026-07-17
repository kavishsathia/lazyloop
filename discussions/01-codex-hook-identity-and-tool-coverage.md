# Codex Hook Identity and Tool Coverage

Date: 17 July 2026  
Tested with: `codex-cli 0.144.5`

## Context

Hamster is intended to compile declarative and imperative loops into state
machines that Codex agents traverse. For this to work, Hamster must be able to:

1. Bind each Codex agent to the correct state-machine instance.
2. Distinguish a root agent from parallel and nested subagents.
3. Observe imperative tool calls in their actual execution order.
4. Fork the current state machine when Codex creates a subagent.
5. Reject or remediate tool calls that are invalid in the current state.

The public Codex hook documentation leaves two important questions open:

- Ordinary tool hooks document `session_id`, `turn_id`, and `tool_use_id`, but
  do not document `agent_id` as a field.
- The documentation describes `PreToolUse` as incomplete and specifically says
  that web search is not intercepted.

We ran live Codex CLI experiments to determine the behavior of the installed
runtime rather than relying only on the documented contract.

## Experimental setup

The experiments ran in an isolated temporary Git repository. A project-level
`.codex/hooks.json` registered capture-only command hooks for:

- `SessionStart`
- `UserPromptSubmit`
- `PreToolUse`
- `PostToolUse`
- `SubagentStart`
- `SubagentStop`
- `Stop`

Each hook wrote its complete JSON input to a JSONL event log. The hook returned
an empty successful JSON response and did not modify or block the agent's
behavior.

Three scenarios were exercised:

1. A root agent with two parallel subagents, each making multiple Bash calls.
2. A root agent with a coordinator subagent that spawned a grandchild.
3. A root agent using native web search followed by a Bash call.

The nested experiment used `agents.max_depth=2`.

## Finding 1: tool hooks include `agent_id` for subagents

On `codex-cli 0.144.5`, every observed `PreToolUse` and `PostToolUse` event made
by a subagent included both `agent_id` and `agent_type`.

A representative child tool event contained:

```json
{
  "session_id": "019f6e92-c5c7-79e1-a76d-8621e1f7916d",
  "turn_id": "019f6e93-0d65-7a93-978b-0e9dc3760724",
  "agent_id": "019f6e93-0af8-7d92-9724-1dbf73d30940",
  "agent_type": "default",
  "hook_event_name": "PreToolUse",
  "tool_name": "Bash",
  "tool_input": {
    "command": "printf 'CHILD_A_STEP_1\\n'"
  }
}
```

The root agent's tool events did not include `agent_id`. This gives Hamster a
simple process identity model:

```text
root process:     (session_id, ROOT)
subagent process: (session_id, agent_id)
```

`turn_id` remains valuable for grouping events into turns, but Hamster does not
need to use it as the process identity.

## Finding 2: parallel subagents are directly distinguishable

The parallel experiment produced one shared `session_id` and two distinct,
stable child identities:

```text
Root
  agent_id: absent
  turn_id: 019f6e92-c866-7fb1-a80f-4810f5a97e04

Child A
  agent_id: 019f6e93-0af8-7d92-9724-1dbf73d30940
  turn_id:  019f6e93-0d65-7a93-978b-0e9dc3760724

Child B
  agent_id: 019f6e93-1adf-74e3-b5b1-4f57c2882094
  turn_id:  019f6e93-1d98-7c41-a69c-48a90a7e3195
```

The child calls were interleaved in time without losing identity. This is
sufficient for Hamster to maintain a separate run, state pointer, stack,
budget, and event history for every parallel subagent.

## Finding 3: nested subagents retain distinct identities

The nested experiment used this process tree:

```text
root
└── coordinator
    └── grandchild
```

The coordinator and grandchild had different stable `agent_id` values on all
of their observed tool calls. The coordinator's call to spawn the grandchild
also included the coordinator's own `agent_id`.

This means the spawn request identifies the parent process:

```text
PreToolUse(collaborationspawn_agent)
├── session_id
├── parent agent_id, absent only for root
├── parent turn_id
├── tool_use_id
└── tool_input
```

Later, `SubagentStart` supplies the new child's `agent_id` and `turn_id`.

Hamster can therefore create a pending fork when the spawn call is observed,
then assign a new run ID and binding when the child starts:

```text
PreToolUse(spawn_agent):
    parent_run = lookup(session_id, caller_agent_id || ROOT)
    pending_fork = clone(parent_run)

SubagentStart:
    child_run = assign_new_run_id(pending_fork)
    bind(session_id, child_agent_id, child_run)
```

## Finding 4: collaboration operations are hook-visible

The installed CLI emitted `PreToolUse` and `PostToolUse` for native subagent
operations. The observed canonical names were:

```text
collaborationspawn_agent
collaborationwait_agent
```

When a subagent invoked `collaborationspawn_agent`, the spawn hook contained
that subagent's `agent_id`. This lets Hamster identify the parent before the
child exists.

The `PostToolUse` response for spawning contained the canonical task path, for
example:

```json
{
  "task_name": "/root/coordinator/grandchild"
}
```

It did not contain the new child's `agent_id`; that arrived separately in
`SubagentStart`.

## Finding 5: native web search is hook-visible on this CLI

Contrary to the current public documentation, native web search produced both
`PreToolUse` and `PostToolUse` events. Its canonical tool name was `webrun`.

The pre-tool event contained structured search arguments:

```json
{
  "tool_name": "webrun",
  "tool_input": {
    "search_query": [
      {
        "q": "site:openai.com Codex documentation official"
      }
    ],
    "response_length": "short"
  }
}
```

The post-tool event included the search result in `tool_response`.

Therefore, web search can be considered an observable imperative operation on
this tested CLI version. Hamster should still treat this as a version-specific
capability rather than a permanent guarantee.

## Registry design

Hamster should own `run_id`. Codex identifiers are bindings into Hamster's
runtime registry:

```text
(session_id, agent_id | ROOT) -> run_id -> state-machine instance
```

A state-machine instance should contain at least:

```text
run_id
parent_run_id
root_run_id
session_id
agent_id | ROOT
machine definition
current state pointer
nested loop stack
variables
evidence
resource budget
terminal status
```

On `SubagentStart`, Hamster creates a new `run_id`, clones the parent's machine
and stack, and binds the child's `agent_id` to that new run.

## Implications for imperative enforcement

Hamster should only expose a tool as an imperative language primitive when the
installed Codex runtime produces a sufficiently structured hook event for that
tool.

For supported tools, Hamster can:

1. Resolve the calling process from `session_id` and `agent_id`.
2. Check the tool call against the process's current state.
3. Deny invalid calls and provide remediation.
4. Record valid calls as evidence.
5. Evaluate the next transition after `PostToolUse`.

For unobservable tools, Hamster cannot enforce occurrence or order. Such tools
must either be rejected by the compiler as imperative steps or placed inside a
declarative or explicitly opaque interval.

The precise guarantee is:

> Hamster enforces the sequence of observable operations and makes no claim
> about unobservable agent activity between those operations.

## Versioned capability discovery

The experiment exposed behavior that is newer or broader than the public hook
documentation. Hamster should not rely on a single hard-coded global tool list.

A safer design is a versioned capability matrix:

```text
codex-cli 0.144.5
├── Bash                         observed
├── webrun                       observed
├── collaborationspawn_agent    observed
├── collaborationwait_agent     observed
├── SubagentStart               observed
└── SubagentStop                observed
```

`hamster init` or `hamster doctor` should detect the installed Codex version
and validate required hook behavior. Unknown or untested tools should fail
closed when used as imperative steps.

## Remaining uncertainty: simultaneous nested spawns

Routing an already-started child is solved because its ordinary tool hooks
contain `agent_id`. One association edge remains.

`PreToolUse(collaborationspawn_agent)` identifies the parent and the spawn
call. `SubagentStart` identifies the new child, but does not include the
parent's `agent_id`, the spawning `tool_use_id`, or the spawn task name.

The tested event order made association straightforward:

```text
parent spawn PreToolUse
parent spawn PostToolUse
child SubagentStart
child tool calls
```

If two different parent agents spawn children concurrently, their pending
forks may overlap. A stress test is required to determine whether Codex
serializes these lifecycle events sufficiently for unambiguous matching.

If ordering is not sufficient, Hamster will need a fork-token handshake:

1. Create a unique pending fork token for the parent run.
2. Include that token in the child's initial instructions.
3. Require the child to attach using the token before other work.
4. Bind the observed child `agent_id` to the corresponding forked run.

This handshake remains agent-driven and requires no human intervention.

## Conclusion

The core Hamster identity and execution model is viable on the tested Codex
CLI:

- Root and subagent tool calls can be routed to different runs.
- Parallel subagents retain stable identities.
- Nested subagents retain stable identities.
- The spawning parent is visible when it requests a child.
- Native collaboration operations are observable.
- Native web search is observable as `webrun` on this version.
- A state-machine fork can be created automatically when a subagent starts.

The next focused experiment should stress simultaneous nested spawns and settle
whether pending forks can be associated by event ordering or require explicit
fork tokens.

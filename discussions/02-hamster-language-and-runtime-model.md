# Hamster Language and Runtime Model

Date: 17 July 2026

## Thesis

Hamster is a small language for defining enforceable agent loops. A Hamster
program compiles into a state machine that an agent traverses from beginning to
end.

The model is not the authority on whether work is complete. The agent can
signal that it believes a goal or step has been completed, but the Hamster
runtime evaluates the corresponding machine-observable conditions before it
accepts a transition.

The central principle is:

> A loop is an executable, resource-bounded control structure whose progress
> and completion are determined by observable system state.

Codex is the primary author and primary user of Hamster programs. A human
supervises initial adoption and handles escalations, but normal execution
should proceed without the human manually prompting each transition.

## Two loop semantics

Hamster distinguishes two kinds of loops that are commonly conflated.

### Declarative loops

A declarative loop specifies:

- A goal or desired end state.
- Constraints that must remain true.
- Conditions that prove success.
- Resource and safety limits.

The agent chooses the steps required to reach the goal.

Conceptually:

```hmstr
goal fix_bug {
  achieve {
    issue.reproduces == false
    tests.regression.passed == true
  }

  constrain {
    git.changed_files within issue.scope
    budget.cost <= 2.00
    budget.elapsed <= 30m
  }
}
```

The exact syntax is not yet settled. The important semantic property is that
the agent controls the path while Hamster controls acceptance of the terminal
state.

### Imperative loops

An imperative loop specifies an ordered workflow. The agent must follow the
declared sequence, and Hamster observes each enforceable operation before
advancing the state pointer.

Conceptually:

```hmstr
workflow fix_issue {
  step create_worktree
  step select_issue
  step fix_bug
  step run_tests
  step push_branch
}
```

An imperative program compiles into explicit states and transitions. A tool
call that does not match the current state can be denied with model-visible
remediation.

Declarative and imperative loops share the same runtime but have different
contracts:

```text
declarative: optimize toward a verified state
imperative:  execute a verified state machine
```

## Verified transitions

The agent does not directly mutate the state pointer. It requests or triggers a
transition, after which Hamster evaluates the transition predicate.

```text
agent performs work
        ↓
agent signals completion
        ↓
Hamster runs verifiers
        ↓
predicate passes?
    ├── yes: commit transition
    └── no:  stay in state and return remediation
```

Verification should use independently observed evidence such as:

- Command exit codes.
- Structured test reports.
- Git status and diffs.
- LSP diagnostics.
- Docker or service health.
- Artifact existence and hashes.
- MCP tool results.
- Independent evaluator results.

An agent statement such as "the tests passed" is not evidence by itself.

Transitions should be transactional. The runtime should record the triggering
event, verification evidence, previous state, next state, and resource usage in
one durable operation.

## Observable imperative operations

An imperative step can only be enforced when Codex emits a sufficiently
structured hook event for it.

For an observable operation, Hamster can:

1. Identify the calling process.
2. Check whether the operation is valid in the current state.
3. Deny an invalid operation before execution when supported.
4. Return precise remediation to the agent.
5. Record successful completion after execution.
6. Evaluate and commit the next transition.

An operation that is not hook-visible cannot be a strictly enforced imperative
step. If such an operation were placed in the machine, Hamster would never see
it complete and the state pointer would remain blocked.

Unsupported operations must therefore be handled in one of three ways:

```text
1. Reject them at compile time as imperative steps.
2. Place them inside a declarative goal.
3. Place them inside an explicitly opaque agent-work interval.
```

For an opaque interval, Hamster can enforce the observable operations before
and after the interval but cannot make claims about activity inside it.

The precise guarantee is:

> Hamster enforces the sequence of observable operations and makes no claim
> about unobservable agent activity between those operations.

Tool observability can vary by Codex version. Hamster should use a versioned
capability matrix rather than assume one permanent set of imperative
primitives.

## Nesting and composition

A loop is itself a step. An imperative workflow can therefore invoke another
imperative or declarative loop.

For example:

```text
fix GitHub issue
├── create worktree       imperative
├── select issue          imperative
├── fix bug               declarative loop
├── run tests             imperative
└── push branch           imperative
```

The parent process suspends at the composite step while the nested machine
runs. When the child machine reaches a terminal state, the parent transition is
evaluated using the child's structured result.

Nesting does not necessarily create a new agent process. It creates a new frame
on the current process's loop stack:

```text
process
└── root machine
    └── nested machine
        └── nested machine
```

Each frame should retain:

- Machine definition.
- Current state pointer.
- Local variables.
- Input arguments.
- Accumulated evidence.
- Return value.
- Local resource limits.

## Process model

Hamster adopts an OS-inspired process model for parallel agents.

Each agent process owns:

- A unique `run_id`.
- A complete state-machine instance.
- Its own current state pointer.
- A stack of nested loop frames.
- Variables and structured outputs.
- Evidence and event history.
- Resource accounting.
- Parent and child relationships.

A Codex session may contain many agent processes, but every process has exactly
one active root machine and one active loop stack.

### Process identity

Hamster creates and owns `run_id`. Codex identifiers bind incoming hook events
to Hamster runs:

```text
(session_id, agent_id | ROOT) -> run_id -> state-machine instance
```

The root agent uses a synthetic `ROOT` agent identity because its ordinary tool
hooks do not include `agent_id`.

### `fork`

`fork` creates a new process by cloning the caller's complete execution state:

- Machine definition.
- Current state pointer.
- Nested loop stack.
- Variables.
- Evidence available at the fork boundary.
- Process metadata.
- An explicitly allocated resource budget.

The child receives a new `run_id` and Codex agent binding. It retains
`parent_run_id` and `root_run_id`.

Conceptually:

```hmstr
let child = fork agent {
  role: explorer
}

if process.is_parent {
  wait child
}

if process.is_child {
  // delegated work
}
```

Like an OS fork, both processes resume from the fork boundary but observe
different fork results:

```text
parent: fork_result = child.run_id
child:  fork_result = null
```

Every native Codex subagent creation should automatically create the
corresponding Hamster fork when the parent is running a Hamster machine.

### `exec`

`exec` replaces the machine running inside the current process:

- Preserve the current `run_id`.
- Preserve process ancestry.
- Preserve resource accounting unless explicitly replaced.
- Replace the root machine definition.
- Replace the current state pointer.
- Clear and replace the nested loop stack.
- Pass only declared arguments or preserved variables.

Conceptually:

```hmstr
exec repo:review_pr {
  pr = output.pull_request
}
```

The distinction is:

```text
fork: create a new process
exec: replace the program inside a process
call: push a nested machine onto the same process stack
```

### `wait`, `join`, and cancellation

`wait` blocks the parent state until a child reaches a terminal state.

Additional process operations are expected:

- `join`: collect structured outputs from multiple children.
- `race`: accept the first child satisfying a predicate.
- `cancel`: request child termination.
- `supervise`: restart, retry, or escalate failed children.

Parent transitions should operate on structured child results, not free-form
assistant summaries.

### Process intrinsics

The language should expose process-aware values such as:

```hmstr
process.run_id
process.parent_run_id
process.root_run_id
process.session_id
process.agent_id
process.is_root
process.is_parent
process.is_child
process.depth
process.children
process.fork_result
```

These allow one machine definition to branch deliberately between parent and
child behavior.

## Resource-aware termination

Success is only one possible terminal state. Every loop must account for
external workflow constraints such as cost and time.

Terminal results should include at least:

```text
succeeded
failed
blocked
stalled
timed_out
budget_exhausted
cancelled
needs_human
```

Termination policies may use:

- Wall-clock deadline.
- Token budget.
- Monetary budget.
- Attempt limit.
- Lack of measurable progress.
- Repeated identical failure.
- Permission or safety boundary.
- External dependency failure.
- Parent cancellation.
- Required human approval.

Budgets must compose across nested loops and forked processes. A parent should
allocate a defined portion of its remaining budget to a child rather than
allowing unbounded fan-out.

## Developer tools as first-class primitives

Hamster should model developer tools as typed state and operations, not only as
opaque shell strings.

Target first-class integrations include:

- Git.
- Docker.
- Codex.
- LSP.
- Test runners and structured reports.
- MCP tools.

Example predicates might eventually look like:

```hmstr
git.diff.empty == false
git.mergeable == true
docker.service("api").healthy == true
lsp.diagnostics.errors == 0
tests.junit.failed == 0
codex.process.children.all(status == succeeded)
```

Typed operations improve validation, portability, diagnostics, and the
runtime's ability to prove completion.

## CLI as a control protocol

The agent interacts with Hamster through an `npx` CLI:

```bash
npx hamster init
npx hamster run .hamster/scripts/fix-issue.hmstr
npx hamster run personal:review-pr
npx hamster run --text '...'
npx hamster signal goal-achieved
npx hamster status
npx hamster cancel
```

From the agent's perspective, these commands behave like system calls. A Codex
`PreToolUse` hook recognizes and authorizes the command, associates it with the
calling session and agent, and routes it to the correct Hamster process.

The CLI may perform little conventional command work, but it should not be a
literal unprotected no-op. A fail-closed handshake is preferable:

```text
agent command
    ↓
PreToolUse trap
    ↓
identify process and authorize operation
    ↓
inject one-time registration token
    ↓
CLI transports the operation to the runtime
    ↓
transactional state-machine update
```

If the hook does not run, the CLI receives no valid token and refuses the
operation. This prevents a missed hook from silently creating an unbound or
incorrect run.

## Repository initialization

`npx hamster init` should configure a repository for Codex-driven execution.

Expected responsibilities:

1. Detect the repository and Git common directory.
2. Create `.hamster/scripts/`.
3. Create or merge `.codex/hooks.json` safely.
4. Install or pin the Hamster runtime locally.
5. Configure hook commands to use the pinned runtime.
6. Add private runtime artifacts to `.gitignore`.
7. Validate the installed Codex version and hook capabilities.
8. Explain that Codex must reload the project and trust the hook definition.

Hook trust cannot safely be bypassed during normal initialization. Codex
requires project hooks to be reviewed when their definition changes.

## Definition scopes

Hamster supports three definition scopes.

### Repository loops

Shared workflows live under:

```text
<repo>/.hamster/scripts/*.hmstr
```

These definitions are committed and shared with collaborators.

### Personal loops

Private reusable workflows live under:

```text
~/.hamster/scripts/*.hmstr
```

They remain outside the repository and can be referenced by a qualified name
such as `personal:review-pr`.

### Ephemeral loops

One-off definitions can be registered directly as text:

```bash
npx hamster run --text '...'
```

The runtime assigns a content hash and `run_id`, compiles the definition, and
stores only the runtime record. The user does not need to create a `.hmstr`
file.

## Runtime state storage

Shareable definitions and private execution state must remain separate.

For Git repositories, a useful runtime location is:

```text
$(git rev-parse --git-common-dir)/hamster/
```

This location is:

- Not committed.
- Shared across the repository's worktrees.
- Naturally scoped to one repository.

A SQLite database in WAL mode is a strong candidate for concurrent process
bindings, state transitions, pending forks, event history, evidence, and
budgets. Non-Git workflows can fall back to user-level storage under
`~/.hamster/`.

## Compiler and runtime responsibilities

The compiler should:

- Parse and type-check `.hmstr` definitions.
- Distinguish declarative, imperative, composite, and process operations.
- Reject unsupported imperative tools.
- Resolve nested definitions.
- Validate transition reachability.
- Require explicit terminal and budget policies.
- Produce a deterministic state-machine representation.

The runtime should:

- Register runs and Codex bindings.
- Route hook events to processes.
- Enforce allowed operations.
- Execute independent verifiers.
- Commit transitions transactionally.
- Manage loop stacks.
- Fork and exec machines.
- Account for time, cost, tokens, and attempts.
- Persist evidence and event history.
- Produce structured terminal results and remediation.

## Safety and guarantee boundaries

Hamster should distinguish its guarantees explicitly.

### Strong guarantees

- Hamster owns the state pointer.
- A transition is not accepted until its predicates pass.
- Observable imperative operations are checked against the current state.
- Every process has an independent machine and stack.
- Resource and terminal policies are evaluated by the runtime.

### Version-dependent guarantees

- Which Codex tools emit hook events.
- Which hook-visible calls can be denied or rewritten.
- How subagent spawn lifecycle events are correlated under concurrency.

### Non-guarantees

- Absence of unobservable agent actions between observable events.
- Correctness of a model's unverified natural-language claim.
- Complete security isolation using Codex hooks alone.

## Open design questions

The following areas still require design or experimentation:

1. The concrete `.hmstr` syntax and type system.
2. Whether declarative goals and imperative states share one surface syntax.
3. The verifier interface and typed evidence model.
4. Budget allocation rules for nested loops and forks.
5. Structured child return values and join semantics.
6. Recovery and compensation for a failed imperative step with side effects.
7. Capability discovery across Codex versions.
8. Simultaneous nested subagent spawns and pending-fork correlation.
9. Migration and versioning of persisted state-machine instances.
10. How much runtime functionality lives in hooks, the CLI, or a daemon.

## Current direction

The current architecture can be summarized as:

```text
.hmstr definition
       ↓ compile
typed nested state machine
       ↓ register
Hamster process bound to Codex session and agent
       ↓ execute
Codex hooks observe and constrain operations
       ↓ signal
runtime evaluates independent evidence
       ↓
transition, remediation, escalation, or terminal result
```

Hamster is therefore not a prompt loop and not merely a task runner. It is a
small process-oriented language and runtime for making Codex workflows
observable, composable, resource-bounded, and mechanically verifiable.

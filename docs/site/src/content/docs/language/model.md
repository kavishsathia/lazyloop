---
title: Language model
description: The execution and enforcement model behind goal, flow, and foreach.
---

# Language model

Hamster has two loop forms: `goal` and `foreach`. A goal may attach a `flow` as
its imperative execution policy. Normal completion requires every declared
verifier to pass; an active limit can terminate the loop first.

## Agent work and runtime work

`step` is agent-owned. The Codex agent performs it, while hooks observe and constrain calls where Codex exposes them. `verify` is runtime-owned: Hamster executes the verifier and persists its result as transition evidence. Process control follows the same boundary: `fork` is a step because Codex must spawn the subagent, while `if`, `exec`, and `wait` are executed by Hamster.

This distinction is deliberate. Hamster never treats the agent's claim that tests passed as proof when it can run the check independently.

## Process control

`step child = fork;` snapshots the complete process. The parent receives a
handle with `parent == true`; after token attachment, the child receives the
same handle with `child == true`. Hamster evaluates child/parent branches,
automatically executes `exec script;` by replacing the complete child machine,
and implements `wait child;` durably. Codex never issues exec or wait commands
for language-level process control.

## Loop forms and verification cadence

- [`goal`](/reference/loop-goal/) repeats open-ended agent work and probes its
  ordered verifiers after every hook-visible tool call.
- [`with flow`](/reference/loop-flow/) constrains a goal to an ordered set of
  named steps. Verification happens once per complete flow cycle, never between
  its steps; failure begins the next cycle at the first step.
- [`foreach`](/reference/loop-foreach/) runs an inner goal or foreach to
  completion for each element and collects its return values. A goal inside it
  may carry an attached flow.

Verifier evaluation is cheap-to-expensive in source order and short-circuits
on the first false or errored result. Passing the entire ladder completes the
goal automatically. The optional `hamster signal goal-achieved` command merely
requests an immediate probe.

## Guarantees

The reference uses three explicit guarantee levels:

| Level | Meaning |
| --- | --- |
| **Enforced** | Hamster independently checks the condition before allowing the transition. |
| **Observed** | Hamster sees the relevant event, but cannot prove its complete semantic effect. |
| **Declared** | The contract records the condition, but current hooks or integrations cannot enforce it. |

This vocabulary prevents a hook-visible shell invocation from being confused with proof that an arbitrary external effect occurred.

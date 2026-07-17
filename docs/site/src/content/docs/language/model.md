---
title: Language model
description: The execution and enforcement model behind goal, flow, and foreach.
---

# Language model

Hamster has three loop forms. They differ in how work is organized, but share one termination rule: normal completion requires every declared verifier to pass. An active limit can terminate the loop first.

## Agent work and runtime work

`step` is agent-owned. The Codex agent performs it, while hooks observe and constrain calls where Codex exposes them. `verify` is runtime-owned: Hamster executes the verifier and persists its result as transition evidence.

This distinction is deliberate. Hamster never treats the agent's claim that tests passed as proof when it can run the check independently.

## Loop forms

- [`goal`](/reference/loop-goal/) repeats open-ended agent work until its verifiers pass.
- [`flow`](/reference/loop-flow/) repeats an ordered set of named steps until its verifiers pass.
- [`foreach`](/reference/loop-foreach/) runs an inner goal, flow, or foreach to completion for each element and collects its return values.

## Guarantees

The reference uses three explicit guarantee levels:

| Level | Meaning |
| --- | --- |
| **Enforced** | Hamster independently checks the condition before allowing the transition. |
| **Observed** | Hamster sees the relevant event, but cannot prove its complete semantic effect. |
| **Declared** | The contract records the condition, but current hooks or integrations cannot enforce it. |

This vocabulary prevents a hook-visible shell invocation from being confused with proof that an arbitrary external effect occurred.

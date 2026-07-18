---
title: Hamster documentation
description: A tiny language and runtime for enforceable, resource-bounded Codex loops.
template: splash
hero:
  tagline: Give Codex a goal. Keep the exit conditions outside the agent.
  actions:
    - text: Read the language model
      link: /language/model/
      icon: right-arrow
      variant: primary
    - text: Open the reference
      link: /reference/
      icon: open-book
      variant: secondary
---

## Loops with an independent boundary

Hamster compiles compact workflow declarations into durable state machines. Codex performs agent-owned work; Hamster owns verification, limits, transition policy, and process state.

:::note[One source of truth]
Every page under **Reference** is generated from the same structured catalog embedded in the CLI. For a fast, offline answer, use `hamster man fork` or `hamster man bash.check`.
:::

## The small trusted core

| Concern | Owner | What is independently checked |
| --- | --- | --- |
| Work | Codex agent | Hook-visible step occurrence |
| Verification | Hamster runtime | Verifier command result and evidence |
| Termination | State machine | All verifiers or an active limit |
| Parallelism | Process kernel | Forked state, bindings, wait, and return |

```text
goal {
  limit time "30m";
  verify clean = bash.check("git diff --check");
  verify tests = bash.check("go test ./...");
  return fix;
} with flow {
  step branch = bash.run("git switch -c repair");
  step fix = goal {
    limit attempts 8;
    verify tests = bash.check("go test ./...");
    return "fixed";
  };
}
```

## OS-like process control

```text
goal {
  verify complete = alpha.result == "alpha" && beta.result == "beta";
  return [alpha.result, beta.result];
} with flow {
  step alpha = fork;
  if alpha.child { exec alpha-work; }

  step beta = fork;
  if beta.child { exec beta-work; }

  wait [alpha, beta];
}
```

Only `fork` is agent-owned because Codex must create the subagent. Hamster
selects the parent/child branch, executes process-image replacement, and waits
durably without asking an agent to issue `exec` or `wait` commands.

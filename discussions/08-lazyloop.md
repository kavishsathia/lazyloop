# lazyloop

Date: 27 August 2026

## Why a front door

Everything Hamster enforces is reachable today, and almost none of it is
reachable without knowing it is there. Before the first governed tool call a
person builds a binary, runs `init` in a repository, trusts seven hooks in
Codex, writes a `.hmstr` file, and asks the agent to run it — because a person
cannot start a loop themselves; only a hook can. Each step is defensible on
its own. Together they mean the trusted kernel goes unused.

lazygit's promise is that nothing about git's command surface has to be
remembered: the state is on screen and the actions are one key away.
lazyloop makes the same promise for loop engineering. `hamster` stays the
language and the kernel; `lazyloop` is what people type.

## Control the experience, not the utility

The agent keeps its own renderer. lazyloop runs the stock CLI on a
pseudo-terminal, decodes its output with an in-process terminal emulator
(`vito/midterm`), and paints it to the screen. The
emulator answers the agent's own terminal queries; the two mode changes that
are really about the person's terminal — bracketed paste and focus reporting
— are forwarded and reset on exit. This is tmux's architecture at small
scale, and it inherits the edge cases the agent's authors have already
handled instead of re-earning them.

The alternative — a client of the Codex app-server that renders the
transcript itself — was considered and set aside. It would give Hamster the
whole experience, but every streaming-markdown and diff-rendering edge case
would be Hamster's to own. The app-server remains the right *control*
channel for later (launching, interrupting, steering); it is not the right
place to redraw a conversation.

## How a screen finds its session

The console knows the directory it opened in and the agent it spawned. It
does not know the Codex session that agent will create; that identity exists
only inside hooks. So the console mints a token, sets it in the agent's
environment as `HAMSTER_CONSOLE`, and every hook process — a child of the
agent — carries it to the daemon alongside the session identity. The daemon
binds the two; the console reads its session back through the token.

This is the pending-fork shape reused for a different reason. A fork cannot
know its child's identity until the child's first hook; a console cannot know
its agent's.

## Starting a loop without a script

Most loops are "make this command pass". The console accepts that as one
line and writes the goal itself:

```hmstr
goal {
  limit time 1h;
  limit attempts 10;
  verify passes = bash.check("go test ./...");
  return passes;
}
```

A `.hmstr` path or a literal `goal { … }` compiles as written.

What the console does with the compiled machine is *arm* it, not run it.
A launch waits in the daemon under the console token until the next
`UserPromptSubmit` or `PreToolUse` from an unbound root agent carrying that
token; then it becomes a run, bound the ordinary way, and the hook's
additional context tells the agent a loop has started. The claim happens as
work begins rather than as a turn ends, so the agent reads about the loop
before doing anything under it. A console holds one armed loop, not a queue.

This closes the "a person cannot start a loop" gap without a new trust path:
the run is still created by the hook, under the identity the hook proves.

## What the pane shows and does

Top-down, in the order a person needs it: the trap that is waiting (answered
with `a`, `r`, or an option number; a rejection takes a note, because the
agent re-derives the work from it); the loop — machine, state, iteration,
limits counting down, the last verifier result, tool-call and denial counts;
the armed launch, if any; the last action's outcome. `n` arms a loop. `k`
twice kills the run. `q` or escape returns to the agent.

Every action is a daemon call. The pane never touches run state and redraws
from what `console.observe` reports, twice a second and immediately after
any action.

## Hooks, once

`lazyloop setup` writes the hook into `~/.codex/hooks.json`. A hook bound to
no run allows everything, which is what makes a user-level hook safe in every
repository. The console notices when neither the user-level nor the project
hooks route to a Hamster binary and offers `s` to install them. Any build of
`hamster` or `lazyloop` counts as installed; both serve the same daemon.

## Two full screens, not a split

The agent and the dashboard each own the whole terminal; `ctrl-]` shows the
dashboard, `Tab` returns to the agent. The agent is sized to the full
terminal and keeps that size while the dashboard is up, so a flip never
resizes it and never makes it reflow — the flip is instant, and the agent's
screen survives it untouched in the emulator. This is the background model
made concrete: the dashboard is home, the agent is one key away, and a trap
rings the terminal bell so an escalation reaches a person who is watching
the agent rather than the dashboard.

It started as a left/right split. Two full screens read better: the agent
gets the whole width it was built for, and the dashboard is a real
dashboard rather than a sidebar.

## Not done here

- The probe cadence for plain goals is unchanged: every hook-visible tool
  call still runs the verifier ladder, so `limit attempts` counts tool calls.
  The shorthand goal inherits that. Moving to claim-or-stop is a kernel
  decision recorded separately and deliberately left alone.
- One agent per screen. The multi-agent panes, and the queue and board in
  `07-shared-queue-and-blackboard.md`, want this shell to exist first.
- Mouse events are not forwarded. Codex does not enable mouse tracking at
  startup; when an agent does, its events need translating into pane
  coordinates before they can be passed on.
- Typing into Codex was verified by hand, not by the test harness: Codex
  ignores keyboard input under a Python-driven pty even with no Hamster in
  between. The forwarding path is exercised with a `cat` agent instead.

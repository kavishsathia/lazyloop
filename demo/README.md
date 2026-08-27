# Demo: a night on the wheel

A tiny Go module with two bugs and a loop to fix them under. Nothing here is
used by Hamster itself; it exists so `lazyloop` has something to do on first
run.

## Try it

```sh
go install ./cmd/lazyloop     # from the repository root, once
lazyloop setup                # once
cd demo
lazyloop
```

Press `ctrl-]`, then `n`, type `go test ./...`, press enter. The console
shows the loop as **armed**. Now say something to Codex — *"make the tests
pass"* is enough. Its next hook claims the loop, the agent is told a run has
started, and the left pane follows it: state, iteration, attempts used, the
last verifier result.

The tests fail for two reasons. `Turns` rounds where it should truncate, and
`Marathons` divides as if it were given metres while its comment, its caller,
and its test all speak centimetres. Both fixes are one line; the second
needs the agent to decide whether the comment or the code is lying.
`limit attempts 5` means it gets five tries at green before Hamster stops
it.

## With a trap

`loops/green.hmstr` is the same loop with a question at the end:

```hmstr
trap "The tests pass. Does the fix read like the comment above Turns says it should?";
```

Arm it from the console by typing `loops/green.hmstr` instead of a command.
When the machine is satisfied the run suspends and the console shows the
question at the top of the pane: `a` approves and completes the loop, `r`
asks for a note and hands the work back. Traps are always asked after every
verifier, so you are never asked about work the machine has not accepted.

## Reset

```sh
git checkout -- demo
```

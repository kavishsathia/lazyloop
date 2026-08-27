# Demo: a night on the wheel

A tiny Go module with three bugs and a `lazyloop` demo that fixes them under a
governed loop — **no Codex, no API key required**.

## Run it

```sh
./run.sh
```

It builds `lazyloop` and a small scripted agent, resets the bugs, and opens
lazyloop in this directory. Compose a loop and start it:

```
n   m   go test ./...   ⏎   ⏎
```

`n` opens the builder, `m` adds a machine check, you type the command, the
first `⏎` adds the clause, the second starts the loop. Now watch the list:

- the loop's tool count climbs and its timer ticks as the agent works;
- press `ctrl-]` on the indented **root agent** row to watch it — it explores,
  runs the tests, and patches `wheel/` one bug at a time — then `ctrl-]` back;
- in about forty seconds the loop turns green, because `go test ./...` finally
  passes.

`x` removes a loop from the list; `q` quits.

## The bugs

Three one-line defects across `wheel/`, each with a failing test:

- `Turns` rounds to the nearest turn; the comment says only full turns count.
- `Ration` divides by `hamsters + 1` — an off-by-one that shorts every hamster.
- `TopSpeed` carries a stray `/ 100`; speed is just distance over time.

The agent fixes the code, not the tests. Try composing a richer loop — add a
time limit with `l`, a review with `j`, or a question for yourself with `t` —
and watch how the loop changes.

## What's real here

Everything except the model. The agent is scripted, but it fires the same
hooks Codex does and really edits the files; Hamster's verifier independently
runs `go test ./...` on each tool call and completes the loop only when it
truly passes. Swap the scripted agent for real Codex (`unset HAMSTER_AGENT`,
`lazyloop`) and the same loop governs it.

## Reset

```sh
git checkout -- demo/wheel
```

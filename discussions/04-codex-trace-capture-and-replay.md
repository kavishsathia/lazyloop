# Codex Trace Capture and Replay

Date: 17 July 2026

## Purpose

Hamster should not require a live Codex process for ordinary parser, runtime,
hook, or compatibility testing. Live Codex runs are retained as occasional
conformance tests; routine development replays sanitized hook traces through
the same normalization and state-machine boundaries used in production.

The hook stream is the capture boundary. Codex transcripts are deliberately
excluded because their format is not a stable hook interface and because they
contain substantially more user and model content than Hamster needs.

## Fixture organization

Fixtures are grouped first by exact Codex distribution and version, then by
scenario:

```text
testdata/traces/
  codex-cli-0.144.5/
    parallel-subagents/
      manifest.json
      events.jsonl
    nested-subagent/
      manifest.json
      events.jsonl
    native-web-search/
      manifest.json
      events.jsonl
```

No fixture directory named `latest` is allowed. A fixture always states the
exact runtime that produced it, so changes in hook coverage are visible rather
than silently absorbed into a moving baseline.

## Trace schema

Every manifest identifies:

- Trace schema version.
- Codex distribution and exact version.
- Scenario and source (`live` or `synthetic`).
- Capture time and event count.
- Whether decisions and state snapshots are present.

Each JSONL event contains:

```json
{
  "sequence": 1,
  "elapsed_ms": 0,
  "input": {},
  "decision": {},
  "state_before": {},
  "state_after": {}
}
```

`input` is the sanitized raw hook object. The remaining fields are optional so
older capture-only experiments can become compatibility fixtures even though
they predate Hamster runtime decisions.

## Recording path

The centralized daemon records before and after every hook request:

```text
raw Codex event
  -> state snapshot before
  -> normalize and authorize/transition
  -> decision
  -> state snapshot after
  -> append trace record
```

An active recorder can filter by Codex session. Recording is persisted in the
central database, so restarting the CLI does not lose the trace.

## Deterministic runtime replay

The runtime must not directly own wall-clock time or shell verification.
Instead it consumes injected interfaces:

```go
type Clock interface {
    Now() time.Time
}

type Verifier interface {
    Check(context.Context, string, string) (bool, error)
}
```

Production uses the real clock and command verifier. Tests and replay use a
fixed clock and queued verifier outcomes. This makes limits, retries, nested
loops, and failure paths reproducible without executing captured commands.

## Redaction

Exported fixtures are sanitized by default:

- Session, turn, agent, tool-use, process, and run IDs become stable aliases.
- Workspace and transcript paths become symbolic paths.
- prompts and assistant messages are removed.
- wall-clock timestamps become relative elapsed milliseconds.
- tool names, structured inputs, command text, ordering, and parent/child
  relationships are retained because they are compatibility evidence.

The original private capture remains in the central database. Only redacted
exports belong in Git.

## Replay levels

1. Schema replay validates that current Hamster can parse every recorded wire
   event and preserve process identity and tool pairing.
2. Decision replay compares recorded allow/deny results when present.
3. State replay compares before/after machine snapshots and injected verifier
   results when present.
4. Live conformance reruns a small scenario only when Codex is upgraded or a
   hook capability changes.

The first imported `codex-cli 0.144.5` fixtures are schema-level live traces.
New Hamster-owned recordings include decisions and machine snapshots.

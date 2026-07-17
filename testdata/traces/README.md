# Codex trace fixtures

Trace fixtures are organized by exact Codex distribution and version. Never
place captures in an unversioned `latest` directory.

Each scenario contains a `manifest.json` and newline-delimited
`events.jsonl`. Fixtures committed here must already be sanitized; raw captures
belong in Hamster's private central database.

Run all compatibility fixtures with:

```sh
go test ./internal/trace/...
```

Inspect or replay one fixture with:

```sh
hamster trace replay testdata/traces/codex-cli-0.144.5/parallel-subagents
```

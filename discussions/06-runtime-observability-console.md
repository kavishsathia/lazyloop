# Runtime observability console

## Purpose

Before Hamster coordinates several integration-building agents, a developer
needs to answer four questions without querying SQLite or reading raw hooks:

1. Which Codex sessions does the daemon know about?
2. Which root and subagent processes belong to a session?
3. Which machine, nested loop, state, and iteration is each process on?
4. What tool decisions and independent verification evidence led there?

The observability console is an operational interface over the daemon's owned
state, not a second runtime or source of truth.

## Deployment model

The Go binary embeds `index.html`, `app.css`, and `app.js`. The daemon serves
them from a loopback-only HTTP listener alongside its Unix control socket.

```text
Codex hooks / CLI
        |
        v
Unix socket -> Hamster daemon -> SQLite WAL
                   |
                   v
          127.0.0.1 dashboard
```

`hamster ui` ensures the daemon is available and prints the URL recorded by the
running daemon. A custom or ephemeral loopback port can be selected with the
daemon's `--http` option. Public and wildcard bind addresses are rejected.

The HTTP surface is read-only and applies a restrictive content security
policy. Runtime commands and verification evidence can contain sensitive local
information, so remote access is deliberately outside this milestone.

## Data model

The snapshot endpoint groups durable process records by Codex session. Each
process includes its most recent run even after a terminal child has been
unbound, preserving the visible process tree after completion.

The run-detail endpoint returns:

- Process, agent, session, and parent identity.
- Current machine and state pointer.
- All states in machine order with executor ownership.
- Nested stack frames and resume positions.
- Activated prospective limits.
- Recent pre/post hook events and allow/deny decisions.
- Tool-call durations derived from paired hook timestamps.
- Independent verification observations persisted on the triggering event.

Verification observations were previously retained only by an active trace
recording. They are now attached to ordinary run events as well, so evidence is
available for normal debugging without enabling a profiler ahead of time.

## Interface direction

The design is intentionally closer to Linear and Notion than to an animated
agent visualization:

- Neutral off-white canvas and white working surfaces.
- Near-black type, thin borders, compact spacing, and system fonts.
- A session rail, process table, and evidence detail panel.
- Color limited to status semantics: running, waiting, failed, and killed.
- No gradients, mascots, decorative illustrations, or gamified activity.
- Responsive layout without a frontend framework or external asset requests.

The default polling interval is 1.8 seconds. Polling keeps this first version
simple and recoverable; server-sent events can replace it later if database
load or update latency becomes material.

## API

```text
GET /api/v1/health
GET /api/v1/snapshot
GET /api/v1/runs/{run_id}
```

All responses are non-cacheable JSON. Static assets are embedded and briefly
cacheable. Mutation, cancellation, and interactive process control are not part
of this read-only console.

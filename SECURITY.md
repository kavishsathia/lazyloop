# Security policy

Hamster sits between Codex tool calls and developer systems. Reports involving
hook bypasses, command-policy escapes, unsafe verifier execution, identity or
run-binding confusion, trace redaction failures, daemon exposure, or state
corruption are treated as security-sensitive.

## Supported versions

Hamster has not published a stable release. Security fixes currently target the
latest commit on `main` only.

| Version | Supported |
| --- | --- |
| `main` | Yes |
| Older commits and forks | No |

## Reporting a vulnerability

Do not open a public issue for a suspected vulnerability. Use
[GitHub private vulnerability reporting](https://github.com/kavishsathia/hamster/security/advisories/new)
to send the affected component, reproduction steps, impact, and any proposed
mitigation privately.

Please avoid accessing data that is not yours, degrading services, or
publishing details before a fix is available. The maintainer will acknowledge
the report when practical, investigate it, and coordinate disclosure and credit
with the reporter.

Security support does not cover modified forks, unsupported Codex versions, or
deployments that expose Hamster's loopback-only services to untrusted networks.

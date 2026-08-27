#!/bin/sh
# A self-contained lazyloop demo — no Codex, no API key. A scripted agent fires
# the same hooks Codex would and really fixes three bugs in ./wheel, so Hamster
# governs a genuine loop to completion in about forty seconds.
set -e
here="$(cd "$(dirname "$0")" && pwd)"
root="$(cd "$here/.." && pwd)"

echo "Building lazyloop and the demo agent…"
go build -o /tmp/lazyloop "$root/cmd/lazyloop"
(cd "$here" && go build -o /tmp/lazyloop-demo-agent ./agent)

# The agent edits ./wheel; start every run from the bugs.
git -C "$root" checkout -- demo/wheel 2>/dev/null || true

HAMSTER_HOME=/tmp/lazyloop-demo-home
rm -rf "$HAMSTER_HOME"; mkdir -p "$HAMSTER_HOME"
export HAMSTER_HOME
export HAMSTER_BIN=/tmp/lazyloop
export HAMSTER_AGENT=/tmp/lazyloop-demo-agent

cat <<'MSG'

  lazyloop demo — no API key needed.

  When it opens, compose a loop and start it:
      n   m   go test ./...   ⏎   ⏎

  Then watch the list: the agent fixes three bugs, the tool count climbs,
  and the loop goes green in about forty seconds.

      ctrl-]  on the "root agent" row to watch the agent work; ctrl-] back
      x       remove a loop     ·     q  quit

MSG
printf "  starting… "
sleep 2
cd "$here"
exec /tmp/lazyloop

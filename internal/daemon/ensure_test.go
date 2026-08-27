package daemon

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"hamster/internal/protocol"
	"hamster/internal/store"
)

func TestOnlyAStrictlyNewerCallerReplacesTheDaemon(t *testing.T) {
	now := time.Now()
	running := Identity{Executable: "/bin/hamster", Modified: now, PID: 4242}
	if shouldReplace(Identity{Modified: now}, running) {
		t.Fatal("same build replaced the daemon")
	}
	if shouldReplace(Identity{Modified: now.Add(500 * time.Millisecond)}, running) {
		t.Fatal("timestamp jitter replaced the daemon")
	}
	if shouldReplace(Identity{Modified: now.Add(-time.Hour)}, running) {
		t.Fatal("an older caller replaced a newer daemon")
	}
	if !shouldReplace(Identity{Modified: now.Add(time.Minute)}, running) {
		t.Fatal("a newer build did not replace the daemon")
	}
	// A daemon from before identities were reported cannot be asked to leave.
	if shouldReplace(Identity{Modified: now.Add(time.Hour)}, Identity{}) {
		t.Fatal("an unidentified daemon was marked for replacement")
	}
}

func TestPingReportsTheDaemonsIdentity(t *testing.T) {
	state, err := store.Open(filepath.Join(t.TempDir(), "hamster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	response := New(state).Handle(context.Background(), protocol.Request{Version: protocol.Version, RequestID: "ping", Method: "ping"})
	if response.Error != nil {
		t.Fatal(response.Error)
	}
	var result struct {
		OK       bool     `json:"ok"`
		Identity Identity `json:"identity"`
	}
	if err := json.Unmarshal(response.Result, &result); err != nil {
		t.Fatal(err)
	}
	if !result.OK || result.Identity.PID == 0 || result.Identity.Executable == "" || result.Identity.Modified.IsZero() {
		t.Fatalf("ping = %+v", result)
	}
}

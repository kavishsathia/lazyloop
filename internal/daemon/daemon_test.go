package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"hamster/internal/ir"
	"hamster/internal/protocol"
	"hamster/internal/store"
)

func TestHandlePingAndCreateRun(t *testing.T) {
	state, err := store.Open(filepath.Join(t.TempDir(), "hamster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	server := New(state)
	response := server.Handle(context.Background(), protocol.Request{Version: protocol.Version, RequestID: "ping-1", Method: "ping"})
	if response.Error != nil {
		t.Fatalf("ping error = %v", response.Error)
	}

	machine := ir.Machine{ID: "machine_0", Name: "test", Kind: ir.LoopFlow, InitialState: "state_0", States: []ir.State{{ID: "state_0", Kind: ir.StateComplete}}}
	params, _ := json.Marshal(CreateRunRequest{WorkspaceRoot: "/repo", CodexSessionID: "session", Machine: machine})
	response = server.Handle(context.Background(), protocol.Request{Version: protocol.Version, RequestID: "create-1", Method: "run.create", Params: params})
	if response.Error != nil {
		t.Fatalf("run.create error = %v", response.Error)
	}
	var run store.Run
	if err := json.Unmarshal(response.Result, &run); err != nil {
		t.Fatal(err)
	}
	if run.Machine.Name != "test" || run.StateID != "state_0" {
		t.Fatalf("run = %#v", run)
	}
}

func TestRemoveStaleSocketRefusesRegularFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hamster.sock")
	if err := os.WriteFile(path, []byte("not a socket"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := removeStaleSocket(path); err == nil {
		t.Fatal("removeStaleSocket() error = nil, want refusal")
	}
}

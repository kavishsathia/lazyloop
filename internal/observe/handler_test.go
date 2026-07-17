package observe

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"hamster/internal/ir"
	"hamster/internal/store"
)

func TestHTTPDashboardServesEmbeddedUIAndSnapshot(t *testing.T) {
	state, err := store.Open(filepath.Join(t.TempDir(), "hamster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	machine := ir.Machine{ID: "machine_http", Name: "dashboard", Kind: ir.LoopGoal, InitialState: "state_work", States: []ir.State{{ID: "state_work", Kind: ir.StateAgentWork, Name: "work"}}}
	run, err := state.CreateRun(context.Background(), store.CreateRunParams{WorkspaceRoot: "/repo", CodexSessionID: "session-http", Machine: machine})
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(state)

	page := httptest.NewRecorder()
	handler.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/", nil))
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "Hamster Runtime") {
		t.Fatalf("page = %d %q", page.Code, page.Body.String())
	}
	if policy := page.Header().Get("Content-Security-Policy"); !strings.Contains(policy, "default-src 'self'") {
		t.Fatalf("content security policy = %q", policy)
	}

	snapshot := httptest.NewRecorder()
	handler.ServeHTTP(snapshot, httptest.NewRequest(http.MethodGet, "/api/v1/snapshot", nil))
	if snapshot.Code != http.StatusOK {
		t.Fatalf("snapshot = %d %q", snapshot.Code, snapshot.Body.String())
	}
	var body struct {
		Sessions []store.SessionObservation `json:"sessions"`
	}
	if err := json.Unmarshal(snapshot.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Sessions) != 1 || body.Sessions[0].Processes[0].Run.ID != run.ID {
		t.Fatalf("snapshot body = %#v", body)
	}

	detail := httptest.NewRecorder()
	handler.ServeHTTP(detail, httptest.NewRequest(http.MethodGet, "/api/v1/runs/"+run.ID, nil))
	if detail.Code != http.StatusOK || !strings.Contains(detail.Body.String(), run.ID) {
		t.Fatalf("detail = %d %q", detail.Code, detail.Body.String())
	}
}

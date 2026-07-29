package playground

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hamster/internal/store"
)

func testHandler(t *testing.T) (http.Handler, *store.Store, string) {
	t.Helper()
	home := t.TempDir()
	state, err := store.Open(filepath.Join(home, "hamster.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { state.Close() })
	root := t.TempDir()
	supervisor := NewSupervisor(state, filepath.Join(home, "hamster"), home, root)
	// Nothing in these tests should reach a real agent.
	supervisor.CodexBin = "/nonexistent-codex"
	return NewHandler(state, supervisor), state, root
}

func post(t *testing.T, handler http.Handler, path string, body any) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	payload, _ := json.Marshal(body)
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(payload)))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	var decoded map[string]any
	_ = json.Unmarshal(recorder.Body.Bytes(), &decoded)
	return recorder, decoded
}

// A loop that cannot compile is a mistake to report next to the editor, not one
// to discover from agent transcript noise.
func TestCompileReportsDiagnosticsWithoutRunningAnything(t *testing.T) {
	handler, _, _ := testHandler(t)

	_, ok := post(t, handler, "/api/compile", map[string]string{
		"script": `goal { verify passed = bash.check("true"); return passed; }`,
	})
	if ok["ok"] != true {
		t.Fatalf("valid loop reported as broken: %v", ok)
	}

	_, broken := post(t, handler, "/api/compile", map[string]string{"script": `goal { return "x"; }`})
	if broken["ok"] != false {
		t.Fatalf("a gateless goal compiled: %v", broken)
	}
	if !strings.Contains(broken["error"].(string), "gate") {
		t.Fatalf("diagnostic = %v", broken["error"])
	}
}

func TestLaunchRefusesALoopThatCannotCompile(t *testing.T) {
	handler, _, _ := testHandler(t)
	recorder, body := post(t, handler, "/api/runs", LaunchParams{
		Script: `goal { this is not hamster }`, Prompt: "do something",
	})
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", recorder.Code)
	}
	if !strings.Contains(body["error"].(string), "does not compile") {
		t.Fatalf("error = %v", body["error"])
	}
}

// Every experiment gets its own workspace so two runs of the same task start
// from identical state, and so nothing an experiment does reaches real work.
func TestLaunchBuildsAnIsolatedWorkspace(t *testing.T) {
	handler, state, root := testHandler(t)
	script := `goal {
  verify done = bash.check("test -f NOTES.md");
  return done;
}`
	recorder, body := post(t, handler, "/api/runs", LaunchParams{
		Script: script, Prompt: "write notes",
		Taste: []store.PlaygroundTaste{{Name: "tone", Clause: "Plain sentences, no marketing."}},
	})
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d: %v", recorder.Code, body)
	}
	workspace, _ := body["workspace_root"].(string)
	if workspace == "" || !strings.HasPrefix(workspace, mustEval(t, root)) {
		t.Fatalf("workspace %q is not inside the playground root %q", workspace, root)
	}

	// The loop, the hooks, and a repository are what make the workspace a
	// faithful stand-in for a real project.
	for _, name := range []string{
		filepath.Join(".hamster", "scripts", ScriptName+".hmstr"),
		filepath.Join(".codex", "hooks.json"),
		".git",
	} {
		if _, err := os.Stat(filepath.Join(workspace, name)); err != nil {
			t.Fatalf("workspace is missing %s: %v", name, err)
		}
	}
	written, err := os.ReadFile(filepath.Join(workspace, ".hamster", "scripts", ScriptName+".hmstr"))
	if err != nil || string(written) != script {
		t.Fatalf("script was not written verbatim: %v", err)
	}

	// Taste typed into the playground is adopted, because the person writing the
	// standard is the supervisor and there is nobody else to ratify it.
	clauses, err := state.ListTaste(t.Context(), workspace, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(clauses) != 1 || clauses[0].Name != "tone" {
		t.Fatalf("ratified clauses = %#v", clauses)
	}
}

func TestPlaygroundRefusesNonLoopbackAddresses(t *testing.T) {
	if err := ValidateAddress("0.0.0.0:7332"); err == nil {
		t.Fatal("the playground launches agents and must not be exposed off the machine")
	}
	if err := ValidateAddress("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
}

// The start instruction is part of the experiment record rather than hidden in
// the runtime, so what the agent was told is always visible.
func TestStartPromptNamesTheLoopAndKeepsTheTask(t *testing.T) {
	prompt := StartPrompt("write the notes")
	if !strings.Contains(prompt, "hamster run "+ScriptName) {
		t.Fatalf("prompt does not start the loop: %s", prompt)
	}
	if !strings.Contains(prompt, "write the notes") {
		t.Fatalf("prompt lost the task: %s", prompt)
	}
}

func mustEval(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return path
	}
	return resolved
}

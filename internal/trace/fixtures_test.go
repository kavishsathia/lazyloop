package trace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommittedCodexFixturesReplay(t *testing.T) {
	root := filepath.Join("..", "..", "testdata", "traces", "codex-cli-0.144.5")
	expected := map[string]struct {
		events int
		agents int
		calls  int
	}{
		"parallel-subagents": {events: 27, agents: 3, calls: 10},
		"nested-subagent":    {events: 25, agents: 3, calls: 9},
		"native-web-search":  {events: 7, agents: 1, calls: 2},
	}
	for name, want := range expected {
		t.Run(name, func(t *testing.T) {
			fixture, err := Load(filepath.Join(root, name))
			if err != nil {
				t.Fatal(err)
			}
			if fixture.Manifest.CodexDistribution != "codex-cli" || fixture.Manifest.CodexVersion != "0.144.5" || !fixture.Manifest.Sanitized {
				t.Fatalf("manifest = %#v", fixture.Manifest)
			}
			report, err := Replay(fixture)
			if err != nil {
				t.Fatal(err)
			}
			if report.Events != want.events || report.Agents != want.agents || report.ToolCalls != want.calls || report.PairedCalls != want.calls {
				t.Fatalf("report = %#v", report)
			}
		})
	}
}

func TestCommittedFixturesContainNoRawCaptureIdentity(t *testing.T) {
	root := filepath.Join("..", "..", "testdata", "traces")
	forbidden := []string{"/Users/", "019f6e", "gAAAAA", "This is an isolated"}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		payload, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for _, value := range forbidden {
			if strings.Contains(string(payload), value) {
				t.Errorf("%s contains forbidden raw capture value %q", path, value)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

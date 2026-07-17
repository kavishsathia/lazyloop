package project

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestInitCreatesAndMergesHookConfiguration(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	existing := `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"existing"}]}]},"custom":true}`
	if err := os.WriteFile(filepath.Join(root, ".codex", "hooks.json"), []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Init(root, "/path with spaces/hamster"); err != nil {
		t.Fatal(err)
	}
	if err := Init(root, "/path with spaces/hamster"); err != nil {
		t.Fatal(err)
	}
	contents, _ := os.ReadFile(filepath.Join(root, ".codex", "hooks.json"))
	var configuration map[string]any
	if err := json.Unmarshal(contents, &configuration); err != nil {
		t.Fatal(err)
	}
	if configuration["custom"] != true {
		t.Fatal("custom configuration was not preserved")
	}
	hooks := configuration["hooks"].(map[string]any)
	if len(hooks["PreToolUse"].([]any)) != 1 || len(hooks["PostToolUse"].([]any)) != 1 || hooks["Stop"] == nil {
		t.Fatalf("hooks = %#v", hooks)
	}
}

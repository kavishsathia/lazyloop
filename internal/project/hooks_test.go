package project

import (
	"os"
	"path/filepath"
	"testing"
)

func TestUserHooksInstallOnceAndAreRecognisedFromEitherBinary(t *testing.T) {
	home := t.TempDir()
	if HooksInstalled(UserHooksPath(home)) {
		t.Fatal("empty home reports hooks installed")
	}
	if err := InstallUserHooks(home, "/opt/bin/lazyloop"); err != nil {
		t.Fatal(err)
	}
	if !HooksInstalled(UserHooksPath(home)) {
		t.Fatal("installed hooks not recognised")
	}
	before, _ := os.ReadFile(UserHooksPath(home))
	if err := InstallUserHooks(home, "/opt/bin/lazyloop"); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(UserHooksPath(home))
	if string(before) != string(after) {
		t.Fatal("second install changed hooks.json")
	}

	project := t.TempDir()
	if err := Init(project, "/somewhere/else/bin/hamster"); err != nil {
		t.Fatal(err)
	}
	if !HooksInstalled(filepath.Join(project, ".codex", "hooks.json")) {
		t.Fatal("project hooks written by hamster not recognised")
	}
	other := filepath.Join(t.TempDir(), "hooks.json")
	if err := os.WriteFile(other, []byte(`{"hooks":{"PreToolUse":[{"hooks":[{"type":"command","command":"\"/usr/bin/other\" hook"}]}]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if HooksInstalled(other) {
		t.Fatal("a foreign hook command counted as Hamster's")
	}
}

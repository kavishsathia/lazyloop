package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestShorthandCompilesWithQuotesEscaped(t *testing.T) {
	machine, source, err := CompileRequest(`go test ./... && grep -q "ok" out.txt`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(source, `bash.check("go test ./... && grep -q \"ok\" out.txt")`) {
		t.Fatalf("source = %s", source)
	}
	if machine.Kind != "goal" || len(machine.States) == 0 {
		t.Fatalf("machine = %+v", machine)
	}
	if _, _, err := CompileRequest("   "); err == nil {
		t.Fatal("empty request compiled")
	}
}

func TestLiteralGoalsAndScriptsPassThrough(t *testing.T) {
	literal := `goal { verify ok = bash.check("true"); return ok; }`
	if _, source, err := CompileRequest(literal); err != nil || source != literal {
		t.Fatalf("literal goal: %v, source %q", err, source)
	}
	path := filepath.Join(t.TempDir(), "loop.hmstr")
	if err := os.WriteFile(path, []byte(literal), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, source, err := CompileRequest(path); err != nil || source != path {
		t.Fatalf("script: %v, source %q", err, source)
	}
	if _, _, err := CompileRequest(filepath.Join(t.TempDir(), "missing.hmstr")); err == nil {
		t.Fatal("missing script compiled")
	}
}

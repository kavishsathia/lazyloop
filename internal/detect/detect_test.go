package detect

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	full := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func commands(checks []Check) string {
	var b strings.Builder
	for _, c := range checks {
		b.WriteString(c.Command + "\n")
	}
	return b.String()
}

func TestConventionAndManifestProviders(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "go.mod", "module x\n")
	write(t, dir, "package.json", `{"scripts":{"test":"vitest","lint":"eslint ."}}`)
	write(t, dir, "yarn.lock", "")
	write(t, dir, "Makefile", ".PHONY: test\ntest:\n\tgo test ./...\ndeploy:\n\techo go\n")
	write(t, dir, ".github/workflows/ci.yml", "jobs:\n  x:\n    steps:\n      - run: go vet ./...\n      - run: |\n          multi\n")

	got := commands(Detect(dir))
	for _, want := range []string{"go test ./...", "yarn test", "yarn lint", "make test", "make deploy", "go vet ./..."} {
		if !strings.Contains(got, want+"\n") {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	// The block scalar's body is not a command.
	if strings.Contains(got, "multi") || strings.Contains(got, "|\n") {
		t.Errorf("a block scalar leaked into commands:\n%s", got)
	}
	// yarn, not npm, because of the lockfile.
	if strings.Contains(got, "npm run") {
		t.Errorf("used npm despite a yarn.lock:\n%s", got)
	}
}

func TestRankingPutsTestsFirst(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "go.mod", "module x\n")
	checks := Detect(dir)
	if len(checks) == 0 || !strings.Contains(checks[0].Command, "test") {
		t.Fatalf("tests are not ranked first: %+v", checks)
	}
}

func TestEmptyDirHasNoChecks(t *testing.T) {
	if got := Detect(t.TempDir()); len(got) != 0 {
		t.Fatalf("empty dir produced checks: %+v", got)
	}
}

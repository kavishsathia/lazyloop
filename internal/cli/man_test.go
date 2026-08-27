package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRenderMan(t *testing.T) {
	output, err := renderMan([]string{"fork"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, "process.fork") || !strings.Contains(output, "GUARANTEE: ENFORCED") {
		t.Fatalf("unexpected manual output:\n%s", output)
	}
}

func TestRenderManJSONAndGrammar(t *testing.T) {
	output, err := renderMan([]string{"--json", "bash.check"})
	if err != nil {
		t.Fatal(err)
	}
	var entry map[string]any
	if err := json.Unmarshal([]byte(output), &entry); err != nil {
		t.Fatal(err)
	}
	if entry["id"] != "primitive.bash-check" {
		t.Fatalf("id = %v", entry["id"])
	}
	grammar, err := renderMan([]string{"grammar"})
	if err != nil || !strings.Contains(grammar, "program") {
		t.Fatalf("grammar = %q, err = %v", grammar, err)
	}
}

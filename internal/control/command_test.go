package control

import "testing"

func TestParseHamsterAndNpxCommands(t *testing.T) {
	for _, input := range []string{
		`hamster run build-provider`,
		`/usr/local/bin/hamster run --text 'goal { verify yes = true; return yes; }'`,
		`npx --yes hamster status`,
	} {
		command, ok, err := Parse(input)
		if err != nil || !ok || command.Name == "" {
			t.Fatalf("Parse(%q) = %#v, %v, %v", input, command, ok, err)
		}
	}
}

func TestIgnoreUnrelatedCommand(t *testing.T) {
	if _, ok, err := Parse("go test ./..."); err != nil || ok {
		t.Fatalf("Parse() = %v, %v", ok, err)
	}
}

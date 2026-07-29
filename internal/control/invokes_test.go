package control

import "testing"

// Parse only recognizes a Hamster command that is the whole tool call, so every
// other way of reaching the CLI has to be caught separately. Otherwise one word
// in front of `hamster resolve` lets an agent answer its own trap.
func TestInvokesFindsTheCLIInAnyCommandPosition(t *testing.T) {
	disguised := []string{
		"cd . && hamster taste ratify",
		"cd /repo; hamster resolve trap_x --approve",
		"true || hamster audit j --agree",
		"HAMSTER_HOME=/tmp hamster taste ratify",
		"sh -c 'hamster resolve trap_x --approve'",
		"bash -c \"hamster taste ratify\"",
		"/usr/local/bin/hamster resolve trap_x --approve",
		"echo start && npx hamster taste ratify",
		"ls | hamster taste ratify",
	}
	for _, command := range disguised {
		t.Run(command, func(t *testing.T) {
			if _, isControl, _ := Parse(command); isControl {
				return // Parse already owns it.
			}
			if !Invokes(command) {
				t.Fatalf("%q reaches the Hamster CLI without passing the authority check", command)
			}
		})
	}
}

// Matching on command position rather than content matters in a repository
// called hamster, where the word turns up in ordinary work constantly.
func TestInvokesIgnoresTheWordInOrdinaryCommands(t *testing.T) {
	innocent := []string{
		"echo hamster is cute",
		`git commit -m "add hamster docs"`,
		`rg "hamster" internal/`,
		"go test ./... # hamster",
		`printf '%s\n' "hamster taste ratify"`,
		"cat hamster.md",
		"./hamsterctl status",
	}
	for _, command := range innocent {
		t.Run(command, func(t *testing.T) {
			if Invokes(command) {
				t.Fatalf("%q is not a Hamster invocation but was treated as one", command)
			}
		})
	}
}

func TestParseStillOwnsAWholeCommandInvocation(t *testing.T) {
	command, isControl, err := Parse("hamster taste ratify --name naming")
	if err != nil || !isControl {
		t.Fatalf("isControl = %v, err = %v", isControl, err)
	}
	if command.Name != "taste" || len(command.Args) != 3 {
		t.Fatalf("command = %#v", command)
	}
}

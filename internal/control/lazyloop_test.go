package control

import "testing"

// lazyloop is the same binary under the name people type. An agent reaching
// for it must meet the same authority checks as one typing hamster, or
// `lazyloop resolve` would answer a trap that `hamster resolve` is denied.
func TestLazyloopIsTheCLIToo(t *testing.T) {
	command, isControl, err := Parse("lazyloop resolve trap_x --approve")
	if err != nil || !isControl || command.Name != "resolve" {
		t.Fatalf("Parse(lazyloop resolve) = %+v, %v, %v", command, isControl, err)
	}
	if _, isControl, _ := Parse("/Users/x/go/bin/lazyloop status"); !isControl {
		t.Fatal("a full path to lazyloop is not recognised")
	}
	for _, disguised := range []string{
		"cd . && lazyloop resolve trap_x --approve",
		"sh -c 'lazyloop taste ratify'",
		"npx lazyloop audit j --agree",
	} {
		if !Invokes(disguised) {
			t.Fatalf("%q reaches the CLI without the authority check", disguised)
		}
	}
	if Invokes("echo lazyloop is what you type") {
		t.Fatal("the word lazyloop in ordinary output counted as an invocation")
	}
}

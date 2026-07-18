package control

import (
	"reflect"
	"testing"
)

func TestParseRunArgumentsIsOrderIndependent(t *testing.T) {
	want := RunArguments{
		Script: "workflow.hmstr", Session: "session", Agent: "agent", Home: "/tmp/hamster-home",
	}
	for _, arguments := range [][]string{
		{"--session", "session", "--agent", "agent", "--home", "/tmp/hamster-home", "workflow.hmstr"},
		{"workflow.hmstr", "--home=/tmp/hamster-home", "--agent=agent", "--session=session"},
	} {
		got, err := ParseRunArguments(arguments)
		if err != nil {
			t.Fatalf("ParseRunArguments(%q): %v", arguments, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("ParseRunArguments(%q) = %#v, want %#v", arguments, got, want)
		}
	}
}

func TestParseRunArgumentsRejectsAmbiguity(t *testing.T) {
	for _, arguments := range [][]string{
		{"one.hmstr", "two.hmstr"},
		{"one.hmstr", "--text", "goal { verify yes = true; return true; }"},
		{"--agent", "child", "one.hmstr"},
		{"--home", "/tmp/home", "one.hmstr"},
		{"--unknown", "value", "one.hmstr"},
		{"--entry", "old-name", "one.hmstr"},
	} {
		if _, err := ParseRunArguments(arguments); err == nil {
			t.Fatalf("ParseRunArguments(%q) error = nil", arguments)
		}
	}
}

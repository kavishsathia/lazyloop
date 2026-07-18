package control

import (
	"fmt"
	"strings"

	"hamster/internal/ir"
)

// RunArguments is the canonical interpretation of a hamster run invocation.
// Both the CLI and the Codex hook must use this parser so tool interception
// cannot select a different machine from direct execution.
type RunArguments struct {
	Script  string
	Text    string
	Session string
	Agent   string
	Home    string
}

func ParseRunArguments(arguments []string) (RunArguments, error) {
	var result RunArguments
	seen := map[string]bool{}
	options := true
	for index := 0; index < len(arguments); index++ {
		argument := arguments[index]
		if options && argument == "--" {
			options = false
			continue
		}
		if options && strings.HasPrefix(argument, "--") {
			name, value, inline := strings.Cut(strings.TrimPrefix(argument, "--"), "=")
			switch name {
			case "text", "session", "agent", "home":
			default:
				return RunArguments{}, fmt.Errorf("unknown run option --%s", name)
			}
			if seen[name] {
				return RunArguments{}, fmt.Errorf("run option --%s was provided more than once", name)
			}
			seen[name] = true
			if !inline {
				index++
				if index >= len(arguments) {
					return RunArguments{}, fmt.Errorf("--%s requires a value", name)
				}
				value = arguments[index]
			}
			if value == "" {
				return RunArguments{}, fmt.Errorf("--%s requires a non-empty value", name)
			}
			switch name {
			case "text":
				result.Text = value
			case "session":
				result.Session = value
			case "agent":
				result.Agent = value
			case "home":
				result.Home = value
			}
			continue
		}
		if result.Script != "" {
			return RunArguments{}, fmt.Errorf("run accepts one script, got %q and %q", result.Script, argument)
		}
		result.Script = argument
	}
	if result.Script != "" && result.Text != "" {
		return RunArguments{}, fmt.Errorf("run accepts either a script or --text, not both")
	}
	if result.Script == "" && result.Text == "" {
		return RunArguments{}, fmt.Errorf("run requires a script or --text")
	}
	if result.Agent != "" && result.Session == "" {
		return RunArguments{}, fmt.Errorf("--agent requires --session")
	}
	if result.Home != "" && result.Session == "" {
		return RunArguments{}, fmt.Errorf("--home requires --session")
	}
	return result, nil
}

func ResolveRunMachine(arguments RunArguments, cwd string) (ir.Machine, error) {
	source, err := resolveRunSource(arguments, cwd)
	if err != nil {
		return ir.Machine{}, err
	}
	return compileMachine(source)
}

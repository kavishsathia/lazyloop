package tui

import (
	"fmt"
	"os"
	"strings"

	"hamster/internal/control"
	"hamster/internal/ir"
)

// Most loops are "make this command pass". The console accepts that as one
// line and writes the goal itself; a .hmstr path or a literal goal is passed
// to the compiler untouched.
const (
	defaultTimeLimit    = "1h"
	defaultAttemptLimit = 10
)

// CompileRequest turns what a person typed into a machine, and returns the
// source that was compiled so the console can show what it armed.
func CompileRequest(text string) (ir.Machine, string, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return ir.Machine{}, "", fmt.Errorf("type a command to make pass, a .hmstr path, or a goal")
	}
	if strings.HasPrefix(text, "goal") || strings.HasPrefix(text, "foreach") {
		machine, err := control.CompileSource(text)
		return machine, text, err
	}
	if strings.HasSuffix(text, ".hmstr") {
		if _, err := os.Stat(text); err != nil {
			return ir.Machine{}, "", fmt.Errorf("%s: %w", text, err)
		}
		machine, err := control.CompileFile(text)
		return machine, text, err
	}
	source := ShorthandSource(text)
	machine, err := control.CompileSource(source)
	return machine, source, err
}

// ShorthandSource is the goal behind a bare command: make it pass, within
// the default limits.
func ShorthandSource(command string) string {
	return fmt.Sprintf(`goal {
  limit time %s;
  limit attempts %d;
  verify passes = bash.check(%s);
  return passes;
}
`, defaultTimeLimit, defaultAttemptLimit, quote(command))
}

func quote(text string) string {
	var builder strings.Builder
	builder.WriteByte('"')
	for _, r := range text {
		switch r {
		case '"', '\\':
			builder.WriteByte('\\')
			builder.WriteRune(r)
		case '\n':
			builder.WriteString("\\n")
		default:
			builder.WriteRune(r)
		}
	}
	builder.WriteByte('"')
	return builder.String()
}

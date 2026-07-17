package reference

import (
	"fmt"
	"strings"
)

func Render(entry Entry, full bool) string {
	var out strings.Builder
	fmt.Fprintf(&out, "%s — %s\n%s · %s · since %s\n\n%s\n", entry.ID, entry.Title, entry.Kind, entry.Stability, entry.Since, entry.Summary)
	section(&out, "SYNTAX", entry.Syntax)
	section(&out, "PRECONDITIONS", conditions(entry.Preconditions, false))
	section(&out, "POSTCONDITIONS / GUARANTEE", conditions(entry.Postconditions, true))
	if len(entry.Examples) > 0 {
		out.WriteString("\nEXAMPLE\n")
		out.WriteString(indent(entry.Examples[0].Source, "  "))
		if !strings.HasSuffix(entry.Examples[0].Source, "\n") {
			out.WriteByte('\n')
		}
	}
	if full {
		section(&out, "SEMANTICS", entry.Semantics)
		var failures []string
		for _, failure := range entry.Failures {
			failures = append(failures, fmt.Sprintf("%s: %s Remediation: %s", failure.Code, failure.When, failure.Remediation))
		}
		section(&out, "FAILURES", failures)
		section(&out, "SEE ALSO", entry.Related)
	}
	return out.String()
}

func section(out *strings.Builder, title string, rows []string) {
	if len(rows) == 0 {
		return
	}
	fmt.Fprintf(out, "\n%s\n", title)
	for _, row := range rows {
		fmt.Fprintf(out, "  %s\n", row)
	}
}

func conditions(items []Condition, guarantee bool) []string {
	rows := make([]string, 0, len(items))
	for _, item := range items {
		row := item.Predicate + " — " + item.Description
		if guarantee {
			row += " [GUARANTEE: " + strings.ToUpper(item.Guarantee) + "]"
		}
		if item.Evidence != "" {
			row += " Evidence: " + item.Evidence
		}
		rows = append(rows, row)
	}
	return rows
}

func indent(value, prefix string) string {
	return prefix + strings.ReplaceAll(strings.TrimRight(value, "\n"), "\n", "\n"+prefix)
}

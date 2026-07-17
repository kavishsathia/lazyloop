package language

import "fmt"

type Diagnostic struct {
	Span    Span   `json:"span"`
	Message string `json:"message"`
}

func (d Diagnostic) Error() string {
	return fmt.Sprintf("%s: %s", d.Span, d.Message)
}

type Diagnostics []Diagnostic

func (d Diagnostics) Error() string {
	if len(d) == 0 {
		return ""
	}
	return d[0].Error()
}

// Package docs owns Hamster's canonical, embedded reference sources.
package docs

import "embed"

// Reference contains the language grammar, schema, and reference catalog.
//
//go:embed language/grammar.ebnf reference/*.yaml
var Reference embed.FS

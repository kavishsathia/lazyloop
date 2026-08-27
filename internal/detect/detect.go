// Package detect finds the machine checks a repository already offers, so
// writing a verifier is picking one rather than remembering a command. It is a
// registry of providers: some know a toolchain's conventions (go.mod means
// `go test ./...`), others read the project's own declared commands (a
// package.json's scripts, a Makefile's targets, a CI workflow's run steps).
// Supporting a new ecosystem is adding one provider.
package detect

import (
	"sort"
	"strings"
)

// A Check is one runnable command that could serve as a verifier.
type Check struct {
	Label   string `json:"label"`   // a short name: "tests", "lint", "build"
	Command string `json:"command"` // the shell command to run
	Source  string `json:"source"`  // where it came from, e.g. "go.mod" or "package.json"
}

// A Provider finds checks in a directory. It returns nothing when its
// ecosystem is not present, so every provider can run on every repository.
type Provider interface {
	Name() string
	Detect(dir string) []Check
}

// providers is the registry. Register adds one; the built-ins register in
// their own files' init.
var providers []Provider

// Register adds a provider to the registry.
func Register(p Provider) { providers = append(providers, p) }

// Detect runs every provider over dir, drops duplicate commands, and returns
// the checks ranked so the ones people usually want — tests, then static
// analysis, then builds — come first.
func Detect(dir string) []Check {
	var all []Check
	for _, provider := range providers {
		all = append(all, provider.Detect(dir)...)
	}
	seen := map[string]bool{}
	var unique []Check
	for _, check := range all {
		key := strings.TrimSpace(check.Command)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		unique = append(unique, check)
	}
	sort.SliceStable(unique, func(i, j int) bool {
		return rank(unique[i]) > rank(unique[j])
	})
	return unique
}

// rank scores a check by how likely it is the one a person wants as a gate.
func rank(check Check) int {
	c := strings.ToLower(check.Label + " " + check.Command)
	switch {
	case containsAny(c, "test", "spec", "pytest", "jest", "vitest"):
		return 5
	case containsAny(c, "lint", "vet", "clippy", "check", "typecheck", "mypy", "ruff", "eslint", "tsc"):
		return 4
	case containsAny(c, "build", "compile"):
		return 3
	case containsAny(c, "fmt", "format"):
		return 1
	}
	return 2
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

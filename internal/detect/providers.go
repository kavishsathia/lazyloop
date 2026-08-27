package detect

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

func init() {
	Register(goProvider{})
	Register(cargoProvider{})
	Register(pythonProvider{})
	Register(nodeProvider{})
	Register(makeProvider{})
	Register(justProvider{})
	Register(ciProvider{})
}

func exists(dir, name string) bool {
	_, err := os.Stat(filepath.Join(dir, name))
	return err == nil
}

// --- convention providers: the toolchain implies the commands ---

type goProvider struct{}

func (goProvider) Name() string { return "go" }
func (goProvider) Detect(dir string) []Check {
	if !exists(dir, "go.mod") {
		return nil
	}
	return []Check{
		{"tests", "go test ./...", "go.mod"},
		{"vet", "go vet ./...", "go.mod"},
		{"build", "go build ./...", "go.mod"},
	}
}

type cargoProvider struct{}

func (cargoProvider) Name() string { return "cargo" }
func (cargoProvider) Detect(dir string) []Check {
	if !exists(dir, "Cargo.toml") {
		return nil
	}
	return []Check{
		{"tests", "cargo test", "Cargo.toml"},
		{"clippy", "cargo clippy -- -D warnings", "Cargo.toml"},
		{"build", "cargo build", "Cargo.toml"},
	}
}

type pythonProvider struct{}

func (pythonProvider) Name() string { return "python" }
func (pythonProvider) Detect(dir string) []Check {
	python := exists(dir, "pyproject.toml") || exists(dir, "setup.py") || exists(dir, "tox.ini") || exists(dir, "pytest.ini")
	if !python {
		return nil
	}
	source := "pyproject.toml"
	checks := []Check{{"tests", "pytest", source}}
	if exists(dir, "ruff.toml") || fileContains(dir, "pyproject.toml", "ruff") {
		checks = append(checks, Check{"lint", "ruff check .", source})
	}
	if fileContains(dir, "pyproject.toml", "mypy") {
		checks = append(checks, Check{"types", "mypy .", source})
	}
	return checks
}

// --- manifest providers: read the project's own declared commands ---

type nodeProvider struct{}

func (nodeProvider) Name() string { return "node" }
func (nodeProvider) Detect(dir string) []Check {
	data, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return nil
	}
	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	if json.Unmarshal(data, &pkg) != nil {
		return nil
	}
	runner := "npm run"
	switch {
	case exists(dir, "pnpm-lock.yaml"):
		runner = "pnpm run"
	case exists(dir, "yarn.lock"):
		runner = "yarn"
	case exists(dir, "bun.lockb"):
		runner = "bun run"
	}
	var checks []Check
	for name := range pkg.Scripts {
		checks = append(checks, Check{name, runner + " " + name, "package.json:scripts." + name})
	}
	return checks
}

var makeTarget = regexp.MustCompile(`(?m)^([a-zA-Z][a-zA-Z0-9_.-]*)\s*:(?:[^=]|$)`)

type makeProvider struct{}

func (makeProvider) Name() string { return "make" }
func (makeProvider) Detect(dir string) []Check {
	name := "Makefile"
	if !exists(dir, name) {
		if !exists(dir, "makefile") {
			return nil
		}
		name = "makefile"
	}
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return nil
	}
	var checks []Check
	for _, match := range makeTarget.FindAllStringSubmatch(string(data), -1) {
		target := match[1]
		if strings.HasPrefix(target, ".") {
			continue // .PHONY and friends
		}
		checks = append(checks, Check{target, "make " + target, name})
	}
	return checks
}

var justRecipe = regexp.MustCompile(`(?m)^([a-zA-Z][a-zA-Z0-9_-]*)(?:\s+[^:]*)?:`)

type justProvider struct{}

func (justProvider) Name() string { return "just" }
func (justProvider) Detect(dir string) []Check {
	for _, name := range []string{"justfile", "Justfile", ".justfile"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		var checks []Check
		for _, match := range justRecipe.FindAllStringSubmatch(string(data), -1) {
			checks = append(checks, Check{match[1], "just " + match[1], name})
		}
		return checks
	}
	return nil
}

var runStep = regexp.MustCompile(`(?m)^\s*run:\s*(\S.*)$`)

type ciProvider struct{}

func (ciProvider) Name() string { return "ci" }

// Detect reads the single-line run steps from GitHub Actions workflows: what
// CI runs is the set of checks the project already trusts.
func (ciProvider) Detect(dir string) []Check {
	workflows := filepath.Join(dir, ".github", "workflows")
	entries, err := os.ReadDir(workflows)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var checks []Check
	for _, entry := range entries {
		if entry.IsDir() || !isYAML(entry.Name()) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(workflows, entry.Name()))
		if err != nil {
			continue
		}
		for _, match := range runStep.FindAllStringSubmatch(string(data), -1) {
			command := strings.TrimSpace(match[1])
			// Skip block scalars and shell noise; keep real one-line commands.
			if command == "|" || command == ">" || strings.HasPrefix(command, "#") || seen[command] {
				continue
			}
			seen[command] = true
			checks = append(checks, Check{"ci", command, "workflow:" + entry.Name()})
		}
	}
	return checks
}

func isYAML(name string) bool {
	return strings.HasSuffix(name, ".yml") || strings.HasSuffix(name, ".yaml")
}

func fileContains(dir, name, needle string) bool {
	data, err := os.ReadFile(filepath.Join(dir, name))
	return err == nil && strings.Contains(string(data), needle)
}

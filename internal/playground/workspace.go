package playground

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"hamster/internal/store"
)

// ScriptName is the loop every experiment runs, so the prompt that starts it is
// the same no matter what the script says.
const ScriptName = "playground"

// prepareWorkspace builds a throwaway repository for one experiment. Each run
// gets its own directory so two runs of the same task start from identical
// state, which is the only way their behaviour is comparable — and so nothing
// an experiment does can reach anything you care about.
func prepareWorkspace(root, hamsterBinary, script string) error {
	for _, directory := range []string{
		filepath.Join(root, ".hamster", "scripts"),
		filepath.Join(root, ".codex"),
	} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			return fmt.Errorf("create workspace: %w", err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, ".hamster", "config.toml"), []byte("version = 1\n"), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(root, ".hamster", "scripts", ScriptName+".hmstr"), []byte(script), 0o644); err != nil {
		return err
	}
	hooks, err := hookConfig(hamsterBinary)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(root, ".codex", "hooks.json"), hooks, 0o644); err != nil {
		return err
	}
	// Codex expects a repository, and a real one keeps the experiment honest:
	// the agent can branch and commit exactly as it would in your own project.
	if err := runGit(root, "init", "--quiet"); err != nil {
		return err
	}
	_ = runGit(root, "config", "user.email", "playground@hamster.invalid")
	_ = runGit(root, "config", "user.name", "Hamster Playground")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("# Hamster playground workspace\n"), 0o644); err != nil {
		return err
	}
	_ = runGit(root, "add", "-A")
	_ = runGit(root, "commit", "--quiet", "-m", "playground baseline")
	return nil
}

func runGit(root string, arguments ...string) error {
	command := exec.Command("git", arguments...)
	command.Dir = root
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("git %s: %w: %s", strings.Join(arguments, " "), err, output)
	}
	return nil
}

func hookConfig(binary string) ([]byte, error) {
	entry := func() map[string]any {
		return map[string]any{
			"hooks": []map[string]any{{
				"hooks": []map[string]any{{
					"command":       fmt.Sprintf("%q hook", binary),
					"statusMessage": "Hamster is checking loop state",
					"timeout":       30,
					"type":          "command",
				}},
			}},
		}
	}
	events := map[string]any{}
	for _, name := range []string{"SessionStart", "UserPromptSubmit", "SubagentStart", "SubagentStop", "Stop"} {
		events[name] = entry()["hooks"]
	}
	for _, name := range []string{"PreToolUse", "PostToolUse"} {
		matched := entry()["hooks"].([]map[string]any)
		matched[0]["matcher"] = "*"
		events[name] = matched
	}
	return json.MarshalIndent(map[string]any{"hooks": events}, "", "  ")
}

// StartPrompt is what Codex is actually asked. The instruction to start the loop
// is prepended rather than assumed, so what the agent was told is visible in the
// experiment record instead of hidden in the runtime.
func StartPrompt(prompt string) string {
	return fmt.Sprintf("Start the enforced Hamster loop by running exactly `hamster run %s`, then follow it until "+
		"Hamster reports the goal complete or escalates. Hamster governs which tools you may use; when it denies "+
		"an action, read the remediation and comply rather than working around it.\n\nYour task:\n%s", ScriptName, prompt)
}

// seedTaste adopts the clauses the experimenter typed. In a playground the
// person writing the standard is the supervisor, so there is nobody else to
// ratify it.
func seedTaste(state *store.Store, root string, clauses []store.PlaygroundTaste) error {
	for _, clause := range clauses {
		if strings.TrimSpace(clause.Name) == "" || strings.TrimSpace(clause.Clause) == "" {
			continue
		}
		if _, err := state.RecordTaste(contextTODO(), store.RecordTasteParams{
			WorkspaceRoot: root, Name: clause.Name, Question: clause.Question, Clause: clause.Clause,
		}); err != nil {
			return err
		}
		if _, err := state.RatifyTaste(contextTODO(), root, clause.Name); err != nil {
			return err
		}
	}
	return nil
}

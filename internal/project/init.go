package project

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

func Init(root, executable string) error {
	if err := os.MkdirAll(filepath.Join(root, ".hamster", "scripts"), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(root, ".codex"), 0o755); err != nil {
		return err
	}
	configPath := filepath.Join(root, ".hamster", "config.toml")
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		if err := atomicWrite(configPath, []byte("version = 1\n"), 0o644); err != nil {
			return err
		}
	}
	return installHooks(filepath.Join(root, ".codex", "hooks.json"), executable)
}

func installHooks(path, executable string) error {
	configuration := map[string]any{}
	if contents, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(contents, &configuration); err != nil {
			return fmt.Errorf("parse existing hooks.json: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	hooks, ok := configuration["hooks"].(map[string]any)
	if !ok {
		hooks = map[string]any{}
		configuration["hooks"] = hooks
	}
	command := strconv.Quote(executable) + " hook"
	for _, event := range []string{"PreToolUse", "PostToolUse"} {
		groups, _ := hooks[event].([]any)
		if !hasCommand(groups, command) {
			groups = append(groups, map[string]any{
				"matcher": "*",
				"hooks": []any{map[string]any{
					"type": "command", "command": command, "timeout": 30,
					"statusMessage": "Hamster is checking loop state",
				}},
			})
		}
		hooks[event] = groups
	}
	payload, err := json.MarshalIndent(configuration, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(path, append(payload, '\n'), 0o644)
}

func hasCommand(groups []any, command string) bool {
	for _, rawGroup := range groups {
		group, _ := rawGroup.(map[string]any)
		handlers, _ := group["hooks"].([]any)
		for _, rawHandler := range handlers {
			handler, _ := rawHandler.(map[string]any)
			if handler["command"] == command {
				return true
			}
		}
	}
	return false
}

func atomicWrite(path string, payload []byte, mode os.FileMode) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".hamster-init-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(mode); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(payload); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

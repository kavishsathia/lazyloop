// Command agent is a scripted stand-in for Codex, for the lazyloop demo. It
// fires the same hooks Codex would and really edits the buggy project, so
// Hamster governs a genuine loop to completion — no API key required. It fixes
// the three bugs in ./wheel one at a time, running the tests between each, and
// finishes when `go test ./...` passes.
package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

func main() {
	console := os.Getenv("HAMSTER_CONSOLE")
	session := "demo-" + id()
	banner()

	fire("SessionStart", console, session, "", "", "")
	prompt := readPrompt()
	say("you", prompt)
	fire("UserPromptSubmit", console, session, "", "", prompt)

	steps := []step{
		{"Let me see what's here.", "ls wheel", nil, nil},
		{"Running the tests to see what fails.", "go test ./...", nil, nil},
		{"Turns rounds to the nearest turn, but the comment says only full turns count. Truncating.",
			"edit wheel/wheel.go", ptr("wheel/wheel.go"), &edit{"int(distance/w.Circumference + 0.5)", "int(distance / w.Circumference)"}},
		{"Re-running the tests.", "go test ./...", nil, nil},
		{"Ration divides by hamsters+1 — an off-by-one that shorts every hamster. Fixing.",
			"edit wheel/feed.go", ptr("wheel/feed.go"), &edit{"pellets / (hamsters + 1)", "pellets / hamsters"}},
		{"Re-running the tests.", "go test ./...", nil, nil},
		{"TopSpeed carries a stray /100 — speed is just distance over time. Removing it.",
			"edit wheel/sprint.go", ptr("wheel/sprint.go"), &edit{"float64(turns) * c / float64(minutes) / 100", "float64(turns) * c / float64(minutes)"}},
		{"All three fixed. Running the full suite once more.", "go test ./...", nil, nil},
	}

	tool := 0
	for _, s := range steps {
		tool++
		id := fmt.Sprintf("tool-%d", tool)
		say("agent", s.narrate)
		fire("PreToolUse", console, session, id, s.command, "")
		output := s.run()
		if output != "" {
			fmt.Print(dim(indent(output)))
		}
		context := firePost(console, session, id, s.command, output)
		if context != "" {
			say("hamster", context)
			if strings.Contains(context, "completed it") || strings.Contains(context, "verified the loop") {
				break
			}
		}
		pause(5 * time.Second)
	}

	say("agent", "Done — the loop is green. Press ctrl-] to come back to the dashboard.")
	pause(30 * time.Second)
}

type edit struct{ from, to string }

type step struct {
	narrate string
	command string
	file    *string
	edit    *edit
}

func (s step) run() string {
	switch {
	case s.file != nil && s.edit != nil:
		data, err := os.ReadFile(*s.file)
		if err != nil {
			return "read " + *s.file + ": " + err.Error()
		}
		fixed := strings.Replace(string(data), s.edit.from, s.edit.to, 1)
		if err := os.WriteFile(*s.file, []byte(fixed), 0o644); err != nil {
			return "write " + *s.file + ": " + err.Error()
		}
		return "patched " + *s.file
	case strings.HasPrefix(s.command, "go test"):
		out, _ := exec.Command("go", "test", "./...").CombinedOutput()
		return strings.TrimSpace(string(out))
	case strings.HasPrefix(s.command, "ls"):
		out, _ := exec.Command("ls", "wheel").CombinedOutput()
		return strings.TrimSpace(string(out))
	}
	return ""
}

// fire sends a hook and returns Hamster's additional context, if any.
func fire(event, console, session, toolUseID, command, prompt string) string {
	payload := map[string]any{"session_id": session, "hook_event_name": event, "cwd": mustwd(), "model": "demo"}
	if event == "UserPromptSubmit" {
		payload["turn_id"] = "turn-1"
		payload["prompt"] = prompt
	}
	if toolUseID != "" {
		payload["turn_id"] = "turn-1"
		payload["tool_name"] = "Bash"
		payload["tool_use_id"] = toolUseID
		payload["tool_input"] = map[string]any{"command": command}
	}
	return send(console, payload)
}

func firePost(console, session, toolUseID, command, output string) string {
	payload := map[string]any{
		"session_id": session, "hook_event_name": "PostToolUse", "cwd": mustwd(), "turn_id": "turn-1",
		"tool_name": "Bash", "tool_use_id": toolUseID, "tool_input": map[string]any{"command": command},
		"tool_response": output,
	}
	return send(console, payload)
}

func send(console string, payload map[string]any) string {
	body, _ := json.Marshal(payload)
	cmd := exec.Command(binary(), "hook")
	cmd.Stdin = bytes.NewReader(body)
	cmd.Env = append(os.Environ(), "HAMSTER_CONSOLE="+console)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	var decision struct {
		HookSpecificOutput struct {
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	_ = json.Unmarshal(out, &decision)
	return decision.HookSpecificOutput.AdditionalContext
}

func binary() string {
	if b := os.Getenv("HAMSTER_BIN"); b != "" {
		return b
	}
	return "lazyloop"
}

func readPrompt() string {
	line := make(chan string, 1)
	go func() {
		buf := make([]byte, 4096)
		n, _ := os.Stdin.Read(buf)
		line <- strings.TrimSpace(string(buf[:n]))
	}()
	select {
	case p := <-line:
		if p == "" {
			return "Make this pass."
		}
		return p
	case <-time.After(4 * time.Second):
		return "Make this pass."
	}
}

func banner() {
	fmt.Print("\x1b[1;36m>_ Demo Codex\x1b[0m  (scripted — no API key)\n\n")
}

func say(who, text string) {
	colour := map[string]string{"agent": "\x1b[38;5;39m", "hamster": "\x1b[38;5;214m", "you": "\x1b[38;5;42m"}[who]
	fmt.Printf("%s%s\x1b[0m  %s\n\n", colour, who, text)
}

func dim(s string) string { return "\x1b[2m" + s + "\x1b[0m\n\n" }
func indent(s string) string {
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = "    " + lines[i]
	}
	return strings.Join(lines, "\n")
}

func pause(d time.Duration) { time.Sleep(d) }
func mustwd() string        { d, _ := os.Getwd(); return d }
func ptr(s string) *string  { return &s }
func id() string            { b := make([]byte, 6); rand.Read(b); return hex.EncodeToString(b) }

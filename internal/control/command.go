package control

import (
	"fmt"
	"path/filepath"
	"strings"
	"unicode"
)

type Command struct {
	Name string
	Args []string
}

func Parse(command string) (Command, bool, error) {
	words, err := shellWords(command)
	if err != nil || len(words) == 0 {
		return Command{}, false, err
	}
	index := 0
	if filepath.Base(words[0]) == "npx" {
		index++
		for index < len(words) && strings.HasPrefix(words[index], "-") {
			index++
		}
	}
	if index >= len(words) || filepath.Base(words[index]) != "hamster" {
		return Command{}, false, nil
	}
	index++
	if index >= len(words) {
		return Command{}, true, nil
	}
	return Command{Name: words[index], Args: words[index+1:]}, true, nil
}

// Invokes reports whether a command line runs the Hamster CLI in any command
// position — after a `cd`, behind an env assignment, inside `sh -c`. Parse only
// recognizes an invocation that is the entire command, so without this check a
// single word in front of `hamster resolve` slips past the authority split
// entirely.
//
// It matches on command position rather than content, so `echo hamster` and a
// commit message mentioning hamster are not invocations.
func Invokes(command string) bool {
	for _, segment := range commandSegments(command) {
		words, err := shellWords(segment)
		if err != nil || len(words) == 0 {
			continue
		}
		index := 0
		for index < len(words) && isAssignment(words[index]) {
			index++
		}
		if index >= len(words) {
			continue
		}
		name := filepath.Base(words[index])
		if name == "hamster" {
			return true
		}
		if name == "npx" {
			for _, word := range words[index+1:] {
				if strings.HasPrefix(word, "-") {
					continue
				}
				return filepath.Base(word) == "hamster"
			}
			continue
		}
		// A shell invoked with -c carries another command line as an argument.
		if name == "sh" || name == "bash" || name == "zsh" || name == "dash" {
			for offset := index + 1; offset < len(words)-1; offset++ {
				if words[offset] == "-c" && Invokes(words[offset+1]) {
					return true
				}
			}
		}
	}
	return false
}

func isAssignment(word string) bool {
	name, _, found := strings.Cut(word, "=")
	if !found || name == "" {
		return false
	}
	for _, character := range name {
		if character != '_' && !unicode.IsLetter(character) && !unicode.IsDigit(character) {
			return false
		}
	}
	return true
}

// commandSegments splits a command line on unquoted shell operators so each
// piece can be examined for its own leading command.
func commandSegments(input string) []string {
	var segments []string
	var current strings.Builder
	var quote rune
	escaped := false
	flush := func() {
		if strings.TrimSpace(current.String()) != "" {
			segments = append(segments, current.String())
		}
		current.Reset()
	}
	runes := []rune(input)
	for index := 0; index < len(runes); index++ {
		character := runes[index]
		if escaped {
			current.WriteRune(character)
			escaped = false
			continue
		}
		if character == '\\' && quote != '\'' {
			current.WriteRune(character)
			escaped = true
			continue
		}
		if quote != 0 {
			if character == quote {
				quote = 0
			}
			current.WriteRune(character)
			continue
		}
		switch character {
		case '\'', '"':
			quote = character
			current.WriteRune(character)
		case ';', '\n', '|', '&':
			if (character == '|' || character == '&') && index+1 < len(runes) && runes[index+1] == character {
				index++
			}
			flush()
		default:
			current.WriteRune(character)
		}
	}
	flush()
	return segments
}

func shellWords(input string) ([]string, error) {
	var words []string
	var current strings.Builder
	var quote rune
	escaped := false
	flush := func() {
		if current.Len() > 0 {
			words = append(words, current.String())
			current.Reset()
		}
	}
	for _, character := range input {
		if escaped {
			current.WriteRune(character)
			escaped = false
			continue
		}
		if character == '\\' && quote != '\'' {
			escaped = true
			continue
		}
		if quote != 0 {
			if character == quote {
				quote = 0
			} else {
				current.WriteRune(character)
			}
			continue
		}
		switch character {
		case '\'', '"':
			quote = character
		case ' ', '\t', '\n':
			flush()
		default:
			current.WriteRune(character)
		}
	}
	if escaped || quote != 0 {
		return nil, fmt.Errorf("unterminated shell quoting")
	}
	flush()
	return words, nil
}

package control

import (
	"fmt"
	"path/filepath"
	"strings"
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

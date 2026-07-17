package reference

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
	"hamster/docs"
	"hamster/internal/language"
)

type Catalog struct {
	Schema  string  `yaml:"schema" json:"schema"`
	Entries []Entry `yaml:"entries" json:"entries"`
}

type Entry struct {
	ID             string      `yaml:"id" json:"id"`
	Title          string      `yaml:"title" json:"title"`
	Kind           string      `yaml:"kind" json:"kind"`
	Summary        string      `yaml:"summary" json:"summary"`
	Aliases        []string    `yaml:"aliases,omitempty" json:"aliases,omitempty"`
	Keywords       []string    `yaml:"keywords,omitempty" json:"keywords,omitempty"`
	Since          string      `yaml:"since" json:"since"`
	Stability      string      `yaml:"stability" json:"stability"`
	Syntax         []string    `yaml:"syntax" json:"syntax"`
	Semantics      []string    `yaml:"semantics" json:"semantics"`
	Preconditions  []Condition `yaml:"preconditions,omitempty" json:"preconditions,omitempty"`
	Postconditions []Condition `yaml:"postconditions,omitempty" json:"postconditions,omitempty"`
	Failures       []Failure   `yaml:"failures,omitempty" json:"failures,omitempty"`
	Examples       []Example   `yaml:"examples,omitempty" json:"examples,omitempty"`
	Related        []string    `yaml:"related,omitempty" json:"related,omitempty"`
}

type Condition struct {
	Predicate   string `yaml:"predicate" json:"predicate"`
	Description string `yaml:"description" json:"description"`
	Checked     string `yaml:"checked,omitempty" json:"checked,omitempty"`
	Guarantee   string `yaml:"guarantee,omitempty" json:"guarantee,omitempty"`
	Evidence    string `yaml:"evidence,omitempty" json:"evidence,omitempty"`
}

type Failure struct {
	Code        string `yaml:"code" json:"code"`
	When        string `yaml:"when" json:"when"`
	Remediation string `yaml:"remediation" json:"remediation"`
}

type Example struct {
	Title    string `yaml:"title" json:"title"`
	Language string `yaml:"language" json:"language"`
	Source   string `yaml:"source" json:"source"`
	Test     string `yaml:"test" json:"test"`
}

func Load() (*Catalog, error) {
	payload, err := docs.Reference.ReadFile("reference/catalog.yaml")
	if err != nil {
		return nil, err
	}
	var catalog Catalog
	if err := yaml.Unmarshal(payload, &catalog); err != nil {
		return nil, fmt.Errorf("parse reference catalog: %w", err)
	}
	return &catalog, nil
}

func (c *Catalog) Validate(keywords []string) error {
	if c.Schema != "hamster.reference/v1alpha1" {
		return fmt.Errorf("unsupported reference schema %q", c.Schema)
	}
	ids, names, covered := map[string]bool{}, map[string]string{}, map[string]bool{}
	for i := range c.Entries {
		e := &c.Entries[i]
		if e.ID == "" || e.Title == "" || e.Kind == "" || e.Summary == "" || len(e.Syntax) == 0 || len(e.Semantics) == 0 {
			return fmt.Errorf("entry %d is missing a required field", i)
		}
		if ids[e.ID] {
			return fmt.Errorf("duplicate reference id %q", e.ID)
		}
		ids[e.ID] = true
		for _, name := range append([]string{e.ID}, e.Aliases...) {
			key := strings.ToLower(name)
			if prior := names[key]; prior != "" {
				return fmt.Errorf("reference name %q is shared by %s and %s", name, prior, e.ID)
			}
			names[key] = e.ID
		}
		for _, keyword := range e.Keywords {
			covered[keyword] = true
		}
		for _, condition := range e.Postconditions {
			if condition.Guarantee != "enforced" && condition.Guarantee != "observed" && condition.Guarantee != "declared" {
				return fmt.Errorf("%s has invalid guarantee %q", e.ID, condition.Guarantee)
			}
		}
		for _, example := range e.Examples {
			if example.Test != "none" && example.Test != "parse" {
				return fmt.Errorf("%s example %q has unsupported test mode %q", e.ID, example.Title, example.Test)
			}
			if example.Test == "parse" {
				_, diagnostics := language.Parse(example.Source)
				if len(diagnostics) > 0 {
					return fmt.Errorf("%s example %q does not parse: %s", e.ID, example.Title, diagnostics.Error())
				}
			}
		}
	}
	for _, keyword := range keywords {
		if !covered[keyword] {
			return fmt.Errorf("language keyword %q has no reference owner", keyword)
		}
	}
	return nil
}

func (c *Catalog) Resolve(name string) (*Entry, error) {
	needle := strings.ToLower(strings.TrimSpace(name))
	for i := range c.Entries {
		e := &c.Entries[i]
		if strings.ToLower(e.ID) == needle {
			return e, nil
		}
		for _, alias := range e.Aliases {
			if strings.ToLower(alias) == needle {
				return e, nil
			}
		}
	}
	return nil, fmt.Errorf("unknown reference %q", name)
}

func (c *Catalog) Search(query string) []Entry {
	words := strings.Fields(strings.ToLower(query))
	type hit struct {
		entry Entry
		score int
	}
	var hits []hit
	for _, entry := range c.Entries {
		haystack := strings.ToLower(entry.ID + " " + entry.Title + " " + entry.Summary + " " + strings.Join(entry.Aliases, " ") + " " + strings.Join(entry.Semantics, " "))
		score := 0
		for _, word := range words {
			if !strings.Contains(haystack, word) {
				score = -1
				break
			}
			score++
			if strings.Contains(strings.ToLower(entry.ID+" "+strings.Join(entry.Aliases, " ")), word) {
				score += 3
			}
		}
		if score >= 0 && len(words) > 0 {
			hits = append(hits, hit{entry, score})
		}
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].score == hits[j].score {
			return hits[i].entry.ID < hits[j].entry.ID
		}
		return hits[i].score > hits[j].score
	})
	result := make([]Entry, len(hits))
	for i := range hits {
		result[i] = hits[i].entry
	}
	return result
}

func (c *Catalog) List() []Entry {
	result := append([]Entry(nil), c.Entries...)
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

var ErrNoQuery = errors.New("reference query is empty")

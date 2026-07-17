package reference

import (
	"strings"
	"testing"

	"hamster/internal/language"
)

func TestEmbeddedCatalogIsValidAndCoversLanguage(t *testing.T) {
	catalog, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.Validate(language.Keywords()); err != nil {
		t.Fatal(err)
	}
	if len(catalog.Entries) < 18 {
		t.Fatalf("entries = %d, want at least 18", len(catalog.Entries))
	}
}

func TestResolveAliasesAndSearch(t *testing.T) {
	catalog, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	entry, err := catalog.Resolve("fork")
	if err != nil {
		t.Fatal(err)
	}
	if entry.ID != "process.fork" {
		t.Fatalf("ID = %q", entry.ID)
	}
	results := catalog.Search("child process")
	found := false
	for _, result := range results {
		found = found || result.ID == "process.fork"
	}
	if !found {
		t.Fatalf("search results = %#v", results)
	}
}

func TestRenderCompactIncludesContract(t *testing.T) {
	catalog, _ := Load()
	entry, _ := catalog.Resolve("fork")
	output := Render(*entry, false)
	for _, want := range []string{"process.fork", "PRECONDITIONS", "POSTCONDITIONS", "GUARANTEE"} {
		if !strings.Contains(output, want) {
			t.Fatalf("render missing %q:\n%s", want, output)
		}
	}
}

package index

import (
	"errors"
	"testing"
)

func TestLookupByID(t *testing.T) {
	cat := seedCatalog(t)
	for _, name := range []string{"7zip.7zip", "7ZIP.7Zip"} {
		got, err := cat.Lookup(name)
		if err != nil {
			t.Fatalf("lookup %q: %v", name, err)
		}
		if got == nil || got.ID != "7zip.7zip" {
			t.Errorf("lookup %q = %v, want 7zip.7zip", name, got)
		}
	}
}

func TestLookupByMoniker(t *testing.T) {
	cat := seedCatalog(t)
	for _, name := range []string{"vscode", "VSCode"} {
		got, err := cat.Lookup(name)
		if err != nil {
			t.Fatalf("lookup %q: %v", name, err)
		}
		if got == nil || got.ID != "Microsoft.VisualStudioCode" {
			t.Errorf("lookup %q = %v, want Microsoft.VisualStudioCode", name, got)
		}
	}
}

// An identifier names one package by construction, so it is never read as
// somebody else's moniker.
func TestLookupPrefersIDOverMoniker(t *testing.T) {
	cat := seedCatalog(t)
	got, err := cat.Lookup("Loom.Loom")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.ID != "Loom.Loom" {
		t.Errorf("lookup Loom.Loom = %v, want the package with that id, not Loom.Loom.Portable", got)
	}
}

// "firefox" is on five packages here: three locale forks fold away, and the
// ESR channel that remains hangs off Mozilla.Firefox itself.
func TestLookupFoldsMonikerOntoBasePackage(t *testing.T) {
	cat := seedCatalog(t)
	got, err := cat.Lookup("firefox")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.ID != "Mozilla.Firefox" {
		t.Errorf("lookup firefox = %v, want Mozilla.Firefox", got)
	}
}

func TestLookupAmbiguousMoniker(t *testing.T) {
	cat := seedCatalog(t)
	_, err := cat.Lookup("loom")
	var amb *AmbiguousError
	if !errors.As(err, &amb) {
		t.Fatalf("lookup loom error = %v, want an AmbiguousError", err)
	}
	want := []string{"Loom.Loom", "Teradata.Loom"}
	if len(amb.Candidates) != len(want) {
		t.Fatalf("candidates = %v, want %v", amb.Candidates, want)
	}
	for i, id := range want {
		if amb.Candidates[i].ID != id {
			t.Errorf("candidate %d = %q, want %q", i, amb.Candidates[i].ID, id)
		}
	}
	if amb.Moniker != "loom" {
		t.Errorf("moniker = %q, want loom", amb.Moniker)
	}
}

func TestLookupUnknown(t *testing.T) {
	cat := seedCatalog(t)
	got, err := cat.Lookup("nothing-by-this-name")
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Errorf("lookup = %v, want nil", got)
	}
}

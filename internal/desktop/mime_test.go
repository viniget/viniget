package desktop

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeMIMEDatabase stands up a shared-mime-info globs2 file and points the XDG
// search path at it, so the resolution tests do not depend on what the machine
// running them happens to have installed.
func fakeMIMEDatabase(t *testing.T, lines ...string) {
	t.Helper()
	dir := t.TempDir()
	mime := filepath.Join(dir, "mime")
	if err := os.MkdirAll(mime, 0o755); err != nil {
		t.Fatal(err)
	}
	body := strings.Join(append([]string{"# generated"}, lines...), "\n") + "\n"
	if err := os.WriteFile(filepath.Join(mime, "globs2"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_DATA_HOME", dir)
	t.Setenv("XDG_DATA_DIRS", dir)
}

func TestResolveMIMETypes(t *testing.T) {
	fakeMIMEDatabase(t,
		"50:application/pdf:*.pdf",
		"50:text/plain:*.txt",
		"80:text/html:*.html",
	)

	got := ResolveMIMETypes([]string{".pdf", "TXT", ".zzz", ".pdf", "", "*.html"})
	want := []MIMEType{
		{Extension: "pdf", Type: "application/pdf"},
		{Extension: "txt", Type: "text/plain"},
		{Extension: "zzz", Type: "application/x-viniget-zzz", Declared: true},
		{Extension: "html", Type: "text/html"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestSystemGlobsPrefersHigherWeight(t *testing.T) {
	fakeMIMEDatabase(t,
		"50:application/x-low:*.demo",
		"80:application/x-high:*.demo",
		"50:application/x-glob:*.ta[rz]", // a pattern, not a plain extension
		"broken line",
	)
	globs := systemGlobs()
	if globs["demo"] != "application/x-high" {
		t.Errorf("demo = %q, want the higher-weighted type", globs["demo"])
	}
	if _, ok := globs["ta[rz]"]; ok {
		t.Error("a wildcard glob was read as a plain extension")
	}
}

func TestNormalizeExt(t *testing.T) {
	cases := map[string]string{
		".PDF": "pdf", "pdf": "pdf", "*.pdf": "pdf", " .Pdf ": "pdf",
		"": "", ".": "", "a/b": "", "two words": "",
	}
	for in, want := range cases {
		if got := normalizeExt(in); got != want {
			t.Errorf("normalizeExt(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWriteMIMEPackage(t *testing.T) {
	dir := t.TempDir()

	// Nothing to declare when every extension was already known: no file, and
	// so nothing to clean up later.
	path, err := WriteMIMEPackage(dir, "Vendor.Demo", "Demo",
		[]MIMEType{{Extension: "pdf", Type: "application/pdf"}})
	if err != nil {
		t.Fatal(err)
	}
	if path != "" {
		t.Errorf("wrote %s for an extension the system already knows", path)
	}

	path, err = WriteMIMEPackage(dir, "Vendor.Demo", "Demo", []MIMEType{
		{Extension: "pdf", Type: "application/pdf"},
		{Extension: "dmo", Type: "application/x-viniget-dmo", Declared: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "packages", "viniget-Vendor.Demo.xml"); path != want {
		t.Errorf("path = %q, want %q", path, want)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body := string(b)
	for _, want := range []string{
		`<?xml version="1.0" encoding="UTF-8"?>`,
		`xmlns="http://www.freedesktop.org/standards/shared-mime-info"`,
		`type="application/x-viniget-dmo"`,
		`pattern="*.dmo"`,
		"Demo file (.dmo)",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("declaration missing %q:\n%s", want, body)
		}
	}
	// Only the invented type is declared; redeclaring application/pdf would
	// be viniget picking a fight with the system database.
	if strings.Contains(body, "application/pdf") {
		t.Errorf("redeclared a type the system already owns:\n%s", body)
	}
}

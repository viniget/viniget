package install

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/viniget/viniget/internal/manifest"
)

func writeZip(t *testing.T, entries map[string]string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "payload.zip")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for name, body := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()
	return path
}

func TestUnzip(t *testing.T) {
	src := writeZip(t, map[string]string{
		"App/app.exe":       "MZ",
		"App/data/read.txt": "hello",
	})
	dest := t.TempDir()
	if err := unzip(src, dest); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dest, "App", "data", "read.txt"))
	if err != nil || string(b) != "hello" {
		t.Fatalf("extracted file = %q, %v", b, err)
	}
}

// A hash-verified vendor payload is not the same thing as a trusted one.
func TestUnzipRefusesEscapingEntries(t *testing.T) {
	for _, name := range []string{"../escaped.txt", `..\..\windows\system32\evil.dll`} {
		src := writeZip(t, map[string]string{name: "x"})
		dest := t.TempDir()
		if err := unzip(src, dest); err == nil {
			t.Errorf("unzip accepted the escaping entry %q", name)
		}
		if _, err := os.Stat(filepath.Join(filepath.Dir(dest), "escaped.txt")); err == nil {
			t.Errorf("entry %q was written outside the destination", name)
		}
	}
}

func TestFindNestedIsCaseInsensitive(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "Tools", "Bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(root, "Tools", "Bin", "App.exe")
	if err := os.WriteFile(exe, []byte("MZ"), 0o755); err != nil {
		t.Fatal(err)
	}

	// A Wine prefix is a case-sensitive tree standing in for a case-insensitive
	// filesystem; manifest authors write the path however Explorer showed it.
	for _, rel := range []string{`Tools\Bin\App.exe`, "tools/bin/app.exe", `TOOLS\BIN\APP.EXE`} {
		got, ok := findNested(root, rel)
		if !ok || got != exe {
			t.Errorf("findNested(%q) = %q, %v; want %q", rel, got, ok, exe)
		}
	}
	if _, ok := findNested(root, `Tools\Bin\missing.exe`); ok {
		t.Error("findNested found a file that is not there")
	}
}

func TestInferPortableEntry(t *testing.T) {
	root := t.TempDir()
	files := []string{
		"Demo.exe",
		"DemoUpdater.exe",
		"tools/DemoHelper.exe",
		"runtime.exe",
	}
	for _, name := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("MZ"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	got, err := inferPortableEntry(root, "Vendor.Demo.Portable", "Demo (portable)")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "Demo.exe")
	if got != want {
		t.Errorf("entry = %q, want %q", got, want)
	}
}

func TestInferPortableEntryUsesSoleExecutable(t *testing.T) {
	root := t.TempDir()
	want := filepath.Join(root, "bin", "unexpected-name.EXE")
	if err := os.MkdirAll(filepath.Dir(want), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(want, []byte("MZ"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := inferPortableEntry(root, "Vendor.Demo")
	if err != nil || got != want {
		t.Errorf("entry = %q, %v; want %q", got, err, want)
	}
}

func TestInferPortableEntryRejectsOnlyHelpers(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"unins000.exe", "DemoUpdater.exe", "DemoHelper.exe", "crashpad_handler.exe"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("MZ"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	got, err := inferPortableEntry(root, "Vendor.Demo")
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Errorf("entry = %q, want no inferred entry for helper executables", got)
	}
}

func TestInstallArchiveInfersUndeclaredEntry(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "Demo.exe"), []byte("MZ"), 0o755); err != nil {
		t.Fatal(err)
	}
	prefix := t.TempDir()
	res := &Result{ID: "Vendor.Demo", Name: "Demo"}
	if err := installArchive(&Choice{Type: "portable", URL: "https://example.test/demo.zip"}, src, prefix, res); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(prefix, "drive_c", "Program Files", "Demo", "Demo.exe")
	if res.Entry != want {
		t.Errorf("entry = %q, want %q", res.Entry, want)
	}
	if len(res.Notes) < 2 || !strings.Contains(res.Notes[1], "inferred") {
		t.Errorf("notes = %v, want the heuristic choice to be identified", res.Notes)
	}
}

func TestInstallArchiveDoesNotOverrideDeclaredEntry(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "Guess.exe"), []byte("MZ"), 0o755); err != nil {
		t.Fatal(err)
	}
	prefix := t.TempDir()
	res := &Result{ID: "Vendor.Demo", Name: "Demo"}
	c := &Choice{Type: "portable", Nested: []manifest.NestedFile{{RelativeFilePath: "Missing.exe"}}}
	if err := installArchive(c, src, prefix, res); err != nil {
		t.Fatal(err)
	}
	if res.Entry != "" {
		t.Errorf("entry = %q; an explicit manifest path must not be replaced by a guess", res.Entry)
	}
}

func TestSecurePath(t *testing.T) {
	dest := "/tmp/prefix"
	if got, err := securePath(dest, `App\bin\x.exe`); err != nil || got != "/tmp/prefix/App/bin/x.exe" {
		t.Errorf("securePath = %q, %v", got, err)
	}
	for _, bad := range []string{"/etc/passwd", `C:\windows\evil.dll`, "../x", "a/../../x"} {
		if _, err := securePath(dest, bad); err == nil {
			t.Errorf("securePath accepted %q", bad)
		}
	}
}

package install

import (
	"os"
	"path/filepath"
	"testing"
)

func touch(t *testing.T, prefix, rel string) {
	t.Helper()
	p := filepath.Join(prefix, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotIgnoresUnscoredTrees(t *testing.T) {
	prefix := t.TempDir()
	touch(t, prefix, "drive_c/Program Files/App/app.exe")
	// Wine itself writes here on any run at all; counting it would report a
	// delta for an installer that did nothing.
	touch(t, prefix, "drive_c/windows/temp/wine.tmp")
	touch(t, prefix, "system.reg")

	got, err := snapshot(prefix)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("snapshot = %v, want only the Program Files entry", got)
	}
	if _, ok := got["drive_c/Program Files/App/app.exe"]; !ok {
		t.Errorf("snapshot missed the installed executable: %v", got)
	}
}

func TestSnapshotToleratesMissingTrees(t *testing.T) {
	// A fresh 32-bit prefix has no "Program Files (x86)" until something needs it.
	if _, err := snapshot(t.TempDir()); err != nil {
		t.Fatalf("snapshot of an empty prefix: %v", err)
	}
}

func TestSnapshotIncludesCommonInstallTrees(t *testing.T) {
	prefix := t.TempDir()
	files := []string{
		"drive_c/Program Files/App/app.exe",
		"drive_c/Program Files (x86)/App/app.exe",
		"drive_c/ProgramData/App/app.exe",
		"drive_c/users/me/AppData/Local/Programs/App/app.exe",
	}
	for _, rel := range files {
		touch(t, prefix, rel)
	}

	got, err := snapshot(prefix)
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range files {
		if _, ok := got[filepath.FromSlash(rel)]; !ok {
			t.Errorf("snapshot missed %s: %v", rel, got)
		}
	}
}

func TestDiffCountsExecutables(t *testing.T) {
	prefix := t.TempDir()
	touch(t, prefix, "drive_c/Program Files/App/old.dll")
	before, err := snapshot(prefix)
	if err != nil {
		t.Fatal(err)
	}

	touch(t, prefix, "drive_c/Program Files/App/app.EXE")
	touch(t, prefix, "drive_c/Program Files/App/readme.txt")
	touch(t, prefix, "drive_c/users/me/Desktop/App.lnk")
	after, err := snapshot(prefix)
	if err != nil {
		t.Fatal(err)
	}

	d := diff(before, after)
	if len(d.Files) != 3 {
		t.Errorf("new files = %v, want 3", d.Files)
	}
	// Case matters here: installers ship .EXE as often as .exe.
	if len(d.Executables) != 1 || filepath.Base(d.Executables[0]) != "app.EXE" {
		t.Errorf("executables = %v, want the one .EXE", d.Executables)
	}
}

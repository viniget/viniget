package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/viniget/viniget/internal/desktop"
	"github.com/viniget/viniget/internal/install"
)

func TestWriteLauncherReinfersInstallerEntry(t *testing.T) {
	prefix := filepath.Join(t.TempDir(), "RARLab.WinRAR")
	appDir := filepath.Join(prefix, "drive_c", "Program Files", "WinRAR")
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(appDir, "Rar.exe")
	want := filepath.Join(appDir, "WinRAR.exe")
	for _, path := range []string{stale, want} {
		if err := os.WriteFile(path, []byte("MZ"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	drives := filepath.Join(prefix, "dosdevices")
	if err := os.MkdirAll(drives, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../drive_c", filepath.Join(drives, "c:")); err != nil {
		t.Fatal(err)
	}

	dirs := desktop.Dirs{Launchers: filepath.Join(t.TempDir(), "launchers")}
	script, err := writeLauncher(install.Record{
		ID: "RARLab.WinRAR", Name: "WinRAR", InstallerType: "exe",
		Prefix: prefix, Entry: stale,
	}, dirs, "")
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(script)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `C:\Program Files\WinRAR\WinRAR.exe`) {
		t.Errorf("launcher did not replace stale entry with %q:\n%s", want, body)
	}
}

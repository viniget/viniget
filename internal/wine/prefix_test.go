package wine

import (
	"os"
	"path/filepath"
	"testing"
)

// fakePrefix builds the parts of a prefix these tests care about: the drive
// mappings wine creates in dosdevices.
func fakePrefix(t *testing.T) string {
	t.Helper()
	prefix := t.TempDir()
	dd := filepath.Join(prefix, "dosdevices")
	if err := os.MkdirAll(filepath.Join(prefix, "drive_c"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dd, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../drive_c", filepath.Join(dd, "c:")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/", filepath.Join(dd, "z:")); err != nil {
		t.Fatal(err)
	}
	return prefix
}

func TestDOSPathViaZ(t *testing.T) {
	prefix := fakePrefix(t)
	got, err := DOSPath(prefix, "/tmp/cache/setup.exe")
	if err != nil {
		t.Fatal(err)
	}
	if got != `Z:\tmp\cache\setup.exe` {
		t.Errorf("DOSPath = %q, want Z:\\tmp\\cache\\setup.exe", got)
	}
}

// Both C: and Z: match a payload sitting inside the prefix. Only C: is right,
// which is why the scan is longest-target-first rather than first-match.
func TestDOSPathPrefersTheMoreSpecificDrive(t *testing.T) {
	prefix := fakePrefix(t)
	payload := filepath.Join(prefix, "drive_c", "windows", "temp", "app.msi")
	if err := os.MkdirAll(filepath.Dir(payload), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(payload, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := DOSPath(prefix, payload)
	if err != nil {
		t.Fatal(err)
	}
	if got != `C:\windows\temp\app.msi` {
		t.Errorf("DOSPath = %q, want the C: mapping", got)
	}
}

func TestDOSPathNoPrefix(t *testing.T) {
	if _, err := DOSPath(t.TempDir(), "/tmp/x"); err == nil {
		t.Error("a prefix with no dosdevices should not resolve a path")
	}
}

// A prefix that moves must not keep reading the one it was cloned from.
func TestNormalizeDrivesRepointsAnAbsoluteC(t *testing.T) {
	prefix := fakePrefix(t)
	link := filepath.Join(prefix, "dosdevices", "c:")
	os.Remove(link)
	if err := os.Symlink("/somewhere/else/drive_c", link); err != nil {
		t.Fatal(err)
	}
	if err := normalizeDrives(prefix); err != nil {
		t.Fatal(err)
	}
	target, err := os.Readlink(link)
	if err != nil {
		t.Fatal(err)
	}
	if target != "../drive_c" {
		t.Errorf("c: points at %q, want ../drive_c", target)
	}
}

func TestNormalizeDrivesLeavesRelativeAlone(t *testing.T) {
	prefix := fakePrefix(t)
	if err := normalizeDrives(prefix); err != nil {
		t.Fatal(err)
	}
	target, _ := os.Readlink(filepath.Join(prefix, "dosdevices", "c:"))
	if target != "../drive_c" {
		t.Errorf("c: = %q", target)
	}
	// A prefix that has not been booted yet has no dosdevices at all.
	if err := normalizeDrives(t.TempDir()); err != nil {
		t.Errorf("normalizeDrives on an unbooted prefix: %v", err)
	}
}

func TestSwapKeepsThePreviousPrefixUntilTheMoveSucceeds(t *testing.T) {
	root := t.TempDir()
	final := filepath.Join(root, "Pkg.Id")
	staging := final + ".installing"
	for _, d := range []string{final, staging} {
		if err := os.MkdirAll(filepath.Join(d, "drive_c"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(final, "drive_c", "old"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staging, "drive_c", "new"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Swap(staging, final); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(final, "drive_c", "new")); err != nil {
		t.Errorf("the new prefix is not in place: %v", err)
	}
	if _, err := os.Stat(filepath.Join(final, "drive_c", "old")); err == nil {
		t.Error("the previous prefix survived the swap")
	}
	if _, err := os.Stat(staging); err == nil {
		t.Error("the staging prefix was left behind")
	}
	if _, err := os.Stat(final + ".previous"); err == nil {
		t.Error("the backup was left behind")
	}
}

func TestSafeID(t *testing.T) {
	s := NewStore(t.TempDir())
	if _, err := s.For("Mozilla.Firefox"); err != nil {
		t.Errorf("a normal identifier was rejected: %v", err)
	}
	// Identifiers come off the network and end up in a path.
	for _, bad := range []string{"", "../../etc", "a/b", `a\b`, ".hidden", "x..y"} {
		if _, err := s.For(bad); err == nil {
			t.Errorf("For(%q) should have been rejected", bad)
		}
	}
}

func TestStorePaths(t *testing.T) {
	s := NewStore("/data")
	if s.Base() != "/data/prefixes/base" {
		t.Errorf("Base = %q", s.Base())
	}
	got, err := s.For("Notepad++.Notepad++")
	if err != nil || got != "/data/prefixes/Notepad++.Notepad++" {
		t.Errorf("For = %q, %v", got, err)
	}
}

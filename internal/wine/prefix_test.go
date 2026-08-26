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
	if err := os.Mkdir(filepath.Join(staging, "dosdevices"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(staging, "drive_c"), filepath.Join(staging, "dosdevices", "c:")); err != nil {
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
	if target, err := os.Readlink(filepath.Join(final, "dosdevices", "c:")); err != nil || target != "../drive_c" {
		t.Errorf("swapped prefix c: = %q, %v; want ../drive_c", target, err)
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

func TestCloneFromPreservesSourceAndNormalizesDestination(t *testing.T) {
	store := NewStore(t.TempDir())
	src := fakePrefix(t)
	file := filepath.Join(src, "drive_c", "Program Files", "Demo", "app.exe")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("app"), 0o644); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(store.Dir, "prefixes", "Demo.clone")
	if err := store.CloneFrom(src, dest); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(file); err != nil {
		t.Errorf("source was changed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "drive_c", "Program Files", "Demo", "app.exe")); err != nil {
		t.Errorf("destination lost source file: %v", err)
	}
	target, err := os.Readlink(filepath.Join(dest, "dosdevices", "c:"))
	if err != nil || target != "../drive_c" {
		t.Errorf("destination c: = %q, %v", target, err)
	}
}

func TestPathsIncludesHelperTransactionDebris(t *testing.T) {
	store := NewStore(t.TempDir())
	prefix, _ := store.For("Vendor.Demo")
	for _, path := range []string{prefix + ".helping", prefix + ".installing.previous"} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	paths := store.Paths("Vendor.Demo")
	if len(paths) != 2 {
		t.Errorf("Paths = %v, want helper checkpoint and interrupted swap backup", paths)
	}
}

// The Desktop is the one shell folder wineboot links into the user's own home
// that viniget cannot live with: an installer writes its shortcut through the
// link onto the real desktop, where it is a .lnk no Linux file manager can
// launch, and the walk that collects shortcuts will not descend a symlinked
// root, so viniget never sees it either.
func TestContainDesktopReplacesTheLink(t *testing.T) {
	prefix := t.TempDir()
	home := t.TempDir()
	user := filepath.Join(prefix, "drive_c", "users", "catch")
	if err := os.MkdirAll(user, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(home, filepath.Join(user, "Desktop")); err != nil {
		t.Fatal(err)
	}

	contained, err := ContainDesktop(prefix)
	if err != nil {
		t.Fatal(err)
	}
	if len(contained) != 1 {
		t.Fatalf("contained %v, want the one Desktop", contained)
	}
	info, err := os.Lstat(filepath.Join(user, "Desktop"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		t.Errorf("Desktop is %s, want a real directory", info.Mode())
	}
	// A shortcut written through the old link would have landed here.
	if entries, err := os.ReadDir(home); err != nil || len(entries) != 0 {
		t.Errorf("the user's own desktop was touched: %v %v", entries, err)
	}
}

// The other shell folders stay linked. An application that cannot open the
// files it was installed for is a worse outcome than a tidy prefix, and none
// of them feed the menu.
func TestContainDesktopLeavesTheOtherFoldersLinked(t *testing.T) {
	prefix := t.TempDir()
	home := t.TempDir()
	user := filepath.Join(prefix, "drive_c", "users", "catch")
	if err := os.MkdirAll(user, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Desktop", "Documents", "Downloads"} {
		if err := os.Symlink(home, filepath.Join(user, name)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := ContainDesktop(prefix); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Documents", "Downloads"} {
		info, err := os.Lstat(filepath.Join(user, name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode()&os.ModeSymlink == 0 {
			t.Errorf("%s was contained; only Desktop should be", name)
		}
	}
}

// A prefix already containing its own Desktop -- every prefix cloned from a
// contained baseline -- is left exactly as it is, contents and all.
func TestContainDesktopKeepsARealDirectory(t *testing.T) {
	prefix := t.TempDir()
	desktop := filepath.Join(prefix, "drive_c", "users", "catch", "Desktop")
	if err := os.MkdirAll(desktop, 0o755); err != nil {
		t.Fatal(err)
	}
	shortcut := filepath.Join(desktop, "App.lnk")
	if err := os.WriteFile(shortcut, []byte("L\x00\x00\x00"), 0o644); err != nil {
		t.Fatal(err)
	}
	contained, err := ContainDesktop(prefix)
	if err != nil {
		t.Fatal(err)
	}
	if len(contained) != 0 {
		t.Errorf("contained %v, want nothing to do", contained)
	}
	if _, err := os.Stat(shortcut); err != nil {
		t.Errorf("the shortcut in it was destroyed: %v", err)
	}
}

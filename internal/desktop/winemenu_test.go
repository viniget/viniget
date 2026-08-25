package desktop

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// wineEntry writes the kind of .desktop file winemenubuilder produces: the
// prefix in Exec= inside quotes and in Path= bare, and an icon named by a
// hash of the source rather than by the package.
func wineEntry(t *testing.T, dirs Dirs, rel, prefix, icon string) string {
	t.Helper()
	path := filepath.Join(dirs.Applications, wineMenuDir, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "[Desktop Entry]\nName=Demo\n" +
		`Exec=env "WINEPREFIX=` + prefix + `" wine "C:\\Demo.lnk"` + "\n" +
		"Type=Application\nPath=" + prefix + "/drive_c/Program Files/Demo\n" +
		"Icon=" + icon + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func themeIcon(t *testing.T, dirs Dirs, size, name string) string {
	t.Helper()
	path := filepath.Join(dirs.Icons, "hicolor", size, "apps", name+".png")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("\x89PNG"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// The case this whole file exists for: wine wrote the entry mid-install, so
// it names the staging prefix, which was renamed the moment the install
// succeeded. The entry is a broken duplicate of viniget's and nothing else
// will ever revisit it.
func TestPruneWineMenuRemovesStagingEntries(t *testing.T) {
	dirs := testDirs(t)
	prefix := filepath.Join(t.TempDir(), "prefixes", "Vendor.Demo")

	entry := wineEntry(t, dirs, "Programs/Demo.desktop", prefix+".installing", "5555_demo.0")
	icon48 := themeIcon(t, dirs, "48x48", "5555_demo.0")
	icon32 := themeIcon(t, dirs, "32x32", "5555_demo.0")

	removed := PruneWineMenu(dirs, prefix)
	if len(removed) != 3 {
		t.Fatalf("removed %d files, want 3: %v", len(removed), removed)
	}
	for _, path := range []string{entry, icon48, icon32} {
		if exists(path) {
			t.Errorf("%s survived the sweep", path)
		}
	}
	// The emptied Start Menu folder goes with it, or the menu shows an empty
	// submenu where the application used to be.
	if exists(filepath.Join(dirs.Applications, wineMenuDir, "Programs")) {
		t.Error("the emptied menu directory survived")
	}
}

// The prefix as it stands is matched too, not only the staging sibling: an
// application launched by hand can write a shortcut at any time.
func TestPruneWineMenuRemovesEntriesForTheLivePrefix(t *testing.T) {
	dirs := testDirs(t)
	prefix := filepath.Join(t.TempDir(), "prefixes", "Vendor.Demo")
	entry := wineEntry(t, dirs, "Programs/Demo.desktop", prefix, "5555_demo.0")

	if removed := PruneWineMenu(dirs, prefix); len(removed) == 0 {
		t.Fatal("nothing was removed")
	}
	if exists(entry) {
		t.Error("the entry survived the sweep")
	}
}

// A substring match would read ".../prefixes/Vendor.Demo" as a reference in
// an entry belonging to ".../prefixes/Vendor.DemoPro", and take another
// package's menu entry with it.
func TestPruneWineMenuLeavesOtherPackagesAlone(t *testing.T) {
	dirs := testDirs(t)
	root := t.TempDir()
	prefix := filepath.Join(root, "prefixes", "Vendor.Demo")
	other := wineEntry(t, dirs, "Programs/Pro.desktop",
		filepath.Join(root, "prefixes", "Vendor.DemoPro"), "7ABC_pro.0")
	icon := themeIcon(t, dirs, "48x48", "7ABC_pro.0")

	if removed := PruneWineMenu(dirs, prefix); len(removed) != 0 {
		t.Fatalf("removed %v, want nothing", removed)
	}
	if !exists(other) || !exists(icon) {
		t.Error("another package's entry was swept")
	}
}

// Wine names its icons after a hash of the source path, so two prefixes
// holding the same application land on the same name. Deleting one out from
// under a working entry is worse than leaving a stale file on disk.
func TestPruneWineMenuKeepsAStillReferencedIcon(t *testing.T) {
	dirs := testDirs(t)
	root := t.TempDir()
	prefix := filepath.Join(root, "prefixes", "Vendor.Demo")

	stale := wineEntry(t, dirs, "Programs/Demo.desktop", prefix+".installing", "5555_demo.0")
	keep := wineEntry(t, dirs, "Programs/Other.desktop",
		filepath.Join(root, "prefixes", "Other.Package"), "5555_demo.0")
	icon := themeIcon(t, dirs, "48x48", "5555_demo.0")

	removed := PruneWineMenu(dirs, prefix)
	if len(removed) != 1 || removed[0] != stale {
		t.Fatalf("removed %v, want just the stale entry", removed)
	}
	if !exists(icon) || !exists(keep) {
		t.Error("an icon another entry still names was deleted")
	}
}

// Integration sweeps before it writes, so the entry a user is left with is
// viniget's alone.
func TestIntegrateSweepsWineMenuEntries(t *testing.T) {
	dirs := testDirs(t)
	prefix := fakePrefix(t)
	stale := wineEntry(t, dirs, "Programs/Demo.desktop", prefix+".installing", "5555_demo.0")

	res, err := Integrate(Package{ID: "Vendor.Demo", Name: "Demo", Prefix: prefix},
		Options{Dirs: dirs, NoRefresh: true})
	if err != nil {
		t.Fatal(err)
	}
	if exists(stale) {
		t.Error("the stale wine entry survived integration")
	}
	if !strings.Contains(strings.Join(res.Notes, "\n"), "menu builder") {
		t.Errorf("the sweep was not reported: %v", res.Notes)
	}
	// And it is not claimed as viniget's own, or removal would report a file
	// it never wrote.
	for _, f := range res.Files {
		if f == stale {
			t.Error("a swept file was recorded as one viniget wrote")
		}
	}
}

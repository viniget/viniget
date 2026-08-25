package install

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/viniget/viniget/internal/desktop"
	"github.com/viniget/viniget/internal/wine"
)

// uninstallFixture builds what an uninstall has to take apart: a data
// directory with a prefix in it, and the four desktop directories with one
// package's integration written into them.
type uninstallFixture struct {
	db       *DB
	prefixes *wine.Store
	dirs     desktop.Dirs
}

func newFixture(t *testing.T) *uninstallFixture {
	t.Helper()
	data, share := t.TempDir(), t.TempDir()
	db, err := OpenDB(StatePath(data))
	if err != nil {
		t.Fatal(err)
	}
	return &uninstallFixture{
		db:       db,
		prefixes: wine.NewStore(data),
		dirs: desktop.Dirs{
			Applications: filepath.Join(share, "applications"),
			Icons:        filepath.Join(share, "icons"),
			MIME:         filepath.Join(share, "mime"),
			Launchers:    filepath.Join(data, "launchers"),
		},
	}
}

// install writes the files a real install would leave behind, and returns the
// integration file list the record would carry.
func (f *uninstallFixture) install(t *testing.T, id string) []string {
	t.Helper()
	prefix, err := f.prefixes.For(id)
	if err != nil {
		t.Fatal(err)
	}
	files := []string{
		filepath.Join(f.dirs.Applications, "viniget-"+id+".desktop"),
		filepath.Join(f.dirs.Icons, "hicolor", "48x48", "apps", "viniget-"+id+".png"),
		filepath.Join(f.dirs.MIME, "packages", "viniget-"+id+".xml"),
		filepath.Join(f.dirs.Launchers, id),
	}
	for _, p := range append(files, filepath.Join(prefix, "drive_c", "app.exe")) {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return files
}

func (f *uninstallFixture) record(t *testing.T, id string) {
	t.Helper()
	prefix, _ := f.prefixes.For(id)
	f.db.Put(Record{
		ID: id, Version: "1.0", Name: id, Status: StatusOK,
		Prefix: prefix, Desktop: f.install(t, id), InstalledAt: time.Now(),
	})
}

func gone(t *testing.T, paths ...string) {
	t.Helper()
	for _, p := range paths {
		if _, err := os.Lstat(p); err == nil {
			t.Errorf("%s survived the uninstall", p)
		}
	}
}

func TestUninstallRemovesEverything(t *testing.T) {
	f := newFixture(t)
	f.record(t, "Demo.Pkg")
	rec, _ := f.db.Get("Demo.Pkg")

	out, err := Uninstall(f.db, f.prefixes, f.dirs, "Demo.Pkg")
	if err != nil {
		t.Fatal(err)
	}
	if out.Orphan {
		t.Error("a recorded package was reported as an orphan")
	}
	if len(out.Desktop) != 4 {
		t.Errorf("desktop files removed = %v, want 4", out.Desktop)
	}
	if len(out.Prefixes.Paths) != 1 || out.Prefixes.Bytes != 1 {
		t.Errorf("prefixes = %+v", out.Prefixes)
	}
	gone(t, append(rec.Desktop, rec.Prefix)...)
	if _, ok := f.db.Get("Demo.Pkg"); ok {
		t.Error("the record outlived the files it described")
	}
}

// The record's file list is exact for a reason: globbing "viniget-Demo.Pkg*"
// out of the applications directory would take Demo.Pkg.Extra with it.
func TestUninstallLeavesASimilarlyNamedPackageAlone(t *testing.T) {
	f := newFixture(t)
	f.record(t, "Demo.Pkg")
	f.record(t, "Demo.Pkg.Extra")
	keep, _ := f.db.Get("Demo.Pkg.Extra")

	if _, err := Uninstall(f.db, f.prefixes, f.dirs, "Demo.Pkg"); err != nil {
		t.Fatal(err)
	}
	for _, p := range append(keep.Desktop, keep.Prefix) {
		if _, err := os.Lstat(p); err != nil {
			t.Errorf("Demo.Pkg.Extra lost %s", p)
		}
	}
	if _, ok := f.db.Get("Demo.Pkg.Extra"); !ok {
		t.Error("Demo.Pkg.Extra lost its record")
	}
}

// An install whose record could not be written leaves a prefix that `viniget
// list` cannot show. Uninstalling it is the way out, and the glob fallback is
// what finds its menu entry.
func TestUninstallCleansUpAnOrphan(t *testing.T) {
	f := newFixture(t)
	files := f.install(t, "Orphan.Pkg")
	prefix, _ := f.prefixes.For("Orphan.Pkg")

	out, err := Uninstall(f.db, f.prefixes, f.dirs, "Orphan.Pkg")
	if err != nil {
		t.Fatal(err)
	}
	if !out.Orphan {
		t.Error("a package with no record should be reported as an orphan")
	}
	if len(out.Desktop) != len(files) {
		t.Errorf("removed %v, want all of %v", out.Desktop, files)
	}
	gone(t, append(files, prefix)...)
}

// A prefix deleted by hand leaves a record and a menu entry that still have
// to go, so this is a successful uninstall with something worth saying.
func TestUninstallSurvivesAPrefixRemovedByHand(t *testing.T) {
	f := newFixture(t)
	f.record(t, "Demo.Pkg")
	rec, _ := f.db.Get("Demo.Pkg")
	if err := os.RemoveAll(rec.Prefix); err != nil {
		t.Fatal(err)
	}

	out, err := Uninstall(f.db, f.prefixes, f.dirs, "Demo.Pkg")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := f.db.Get("Demo.Pkg"); ok {
		t.Error("the record survived")
	}
	gone(t, rec.Desktop...)
	if len(out.Notes) == 0 || !strings.Contains(out.Notes[0], "already been removed") {
		t.Errorf("notes = %v, want one about the missing prefix", out.Notes)
	}
}

// The record is the only thing that can still find the prefix, so it stays
// until the prefix is actually gone.
func TestUninstallKeepsTheRecordWhenThePrefixCannotBeRemoved(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the directory permissions this test relies on")
	}
	f := newFixture(t)
	f.record(t, "Demo.Pkg")
	rec, _ := f.db.Get("Demo.Pkg")

	parent := filepath.Dir(rec.Prefix)
	if err := os.Chmod(parent, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(parent, 0o755) })

	if _, err := Uninstall(f.db, f.prefixes, f.dirs, "Demo.Pkg"); err == nil {
		t.Fatal("removing an undeletable prefix should have failed")
	}
	if _, ok := f.db.Get("Demo.Pkg"); !ok {
		t.Error("the record was dropped over a prefix that is still there")
	}
	// The menu entry goes first regardless: an application that cannot be
	// launched beats a launcher pointing into a half-deleted prefix.
	gone(t, rec.Desktop...)
}

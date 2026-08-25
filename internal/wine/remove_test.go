package wine

import (
	"os"
	"path/filepath"
	"testing"
)

// mkPrefix builds a directory with one file of n bytes in it.
func mkPrefix(t *testing.T, path string, n int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(path, "drive_c"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "drive_c", "payload"), make([]byte, n), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A failed install kept with -keep-failed, or a swap that did not finish,
// leaves a sibling next to the prefix that nothing else ever looks at.
func TestRemoveTakesTheStagingAndBackupSiblings(t *testing.T) {
	s := NewStore(t.TempDir())
	prefix, err := s.For("Pkg.Id")
	if err != nil {
		t.Fatal(err)
	}
	mkPrefix(t, prefix, 100)
	mkPrefix(t, prefix+".installing", 20)
	mkPrefix(t, prefix+".previous", 3)
	// A different package whose name merely starts the same way.
	other, _ := s.For("Pkg.Id.Extra")
	mkPrefix(t, other, 7)

	rm, err := s.Remove("Pkg.Id")
	if err != nil {
		t.Fatal(err)
	}
	if len(rm.Paths) != 3 {
		t.Errorf("removed %v, want the prefix and both siblings", rm.Paths)
	}
	if rm.Bytes != 123 {
		t.Errorf("Bytes = %d, want 123", rm.Bytes)
	}
	for _, p := range []string{prefix, prefix + ".installing", prefix + ".previous"} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s survived", p)
		}
	}
	if _, err := os.Stat(other); err != nil {
		t.Error("Pkg.Id.Extra was taken along with Pkg.Id")
	}
}

// safeID only keeps an identifier inside the prefixes directory, and the
// baseline is inside it.
func TestRemoveRefusesTheBaseline(t *testing.T) {
	s := NewStore(t.TempDir())
	mkPrefix(t, s.Base(), 10)
	if _, err := s.Remove("base"); err == nil {
		t.Fatal("removing \"base\" should be refused")
	}
	if _, err := os.Stat(s.Base()); err != nil {
		t.Error("the baseline prefix was deleted anyway")
	}
}

func TestRemoveRejectsAnEscapingIdentifier(t *testing.T) {
	s := NewStore(t.TempDir())
	for _, bad := range []string{"", "../../etc", "a/b"} {
		if _, err := s.Remove(bad); err == nil {
			t.Errorf("Remove(%q) should have been rejected", bad)
		}
	}
}

// Uninstalling something whose prefix a user already deleted by hand is not
// an error: there is still a record and a menu entry to clean up.
func TestRemoveOfAMissingPrefixIsNotAnError(t *testing.T) {
	s := NewStore(t.TempDir())
	rm, err := s.Remove("Never.Installed")
	if err != nil {
		t.Fatalf("Remove on a missing prefix: %v", err)
	}
	if len(rm.Paths) != 0 || rm.Bytes != 0 {
		t.Errorf("removed %v, %d bytes from nothing", rm.Paths, rm.Bytes)
	}
}

func TestPathsFindsAnUnrecordedPrefix(t *testing.T) {
	s := NewStore(t.TempDir())
	if got := s.Paths("Orphan.Pkg"); len(got) != 0 {
		t.Errorf("Paths = %v on an empty store", got)
	}
	prefix, _ := s.For("Orphan.Pkg")
	mkPrefix(t, prefix, 1)
	if got := s.Paths("Orphan.Pkg"); len(got) != 1 || got[0] != prefix {
		t.Errorf("Paths = %v, want [%s]", got, prefix)
	}
}

// A symlink into the prefix must not be followed and counted, or a prefix
// with a link to /usr would report tens of gigabytes.
func TestApparentSizeCountsRegularFilesOnly(t *testing.T) {
	root := t.TempDir()
	mkPrefix(t, root, 64)
	if err := os.Symlink("/usr", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if got := apparentSize(root); got != 64 {
		t.Errorf("apparentSize = %d, want 64", got)
	}
}

package wine

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Store owns viniget's prefixes on disk.
//
// One baseline prefix is created once and every install reflinks off it. On
// btrfs or XFS that clone is effectively free -- 0.003s and zero new extents
// for a 444 MB prefix, against 0.158s for a real copy -- which is what makes
// "throw the prefix away and start over" a reasonable response to a failed
// install rather than a 40-second penalty. On ext4 the same call degrades to
// a real copy: slower, still correct.
type Store struct {
	Dir string
}

// DefaultDir is $XDG_DATA_HOME/viniget, falling back to ~/.local/share/viniget.
// Prefixes are state, not cache: losing them loses installed applications, so
// they do not live next to the index.
func DefaultDir() string {
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return filepath.Join(d, "viniget")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "viniget")
	}
	return filepath.Join(home, ".local", "share", "viniget")
}

// NewStore returns a Store rooted at dir, or the default data dir when empty.
func NewStore(dir string) *Store {
	if dir == "" {
		dir = DefaultDir()
	}
	return &Store{Dir: dir}
}

// Base is the baseline prefix every install is cloned from.
func (s *Store) Base() string { return filepath.Join(s.Dir, "prefixes", "base") }

// For returns the prefix a package is installed into.
func (s *Store) For(id string) (string, error) {
	if err := safeID(id); err != nil {
		return "", err
	}
	return filepath.Join(s.Dir, "prefixes", id), nil
}

// safeID rejects identifiers that would escape the prefix directory. Winget
// identifiers are dot-separated alphanumerics in practice, but they are
// attacker-influenced strings that end up in a path, so they get checked.
func safeID(id string) error {
	if id == "" || strings.ContainsAny(id, `/\`) || strings.HasPrefix(id, ".") || strings.Contains(id, "..") {
		return fmt.Errorf("unusable package identifier %q", id)
	}
	return nil
}

// HasBase reports whether the baseline prefix has been created.
func (s *Store) HasBase() bool {
	_, err := os.Stat(filepath.Join(s.Base(), "system.reg"))
	return err == nil
}

// EnsureBase creates the baseline prefix if it is missing.
func (s *Store) EnsureBase(ctx context.Context, verbose bool) error {
	if s.HasBase() {
		return nil
	}
	// A half-written prefix from an interrupted boot is worse than none.
	if err := os.RemoveAll(s.Base()); err != nil {
		return err
	}
	r := &Runner{Prefix: s.Base(), Display: Headless(), Verbose: verbose}
	if err := r.Boot(ctx); err != nil {
		os.RemoveAll(s.Base())
		return err
	}
	return nil
}

// Clone reflinks the baseline prefix to dest, replacing anything already there.
func (s *Store) Clone(dest string) error {
	if err := os.RemoveAll(dest); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	cp, err := exec.LookPath("cp")
	if err != nil {
		return fmt.Errorf("cloning a prefix needs coreutils cp: %w", err)
	}
	out, err := exec.Command(cp, "-a", "--reflink=auto", s.Base(), dest).CombinedOutput()
	if err != nil {
		return fmt.Errorf("clone prefix: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return normalizeDrives(dest)
}

// Swap moves a freshly installed staging prefix into its final home, keeping
// the previous one until the move succeeds.
//
// This is the payoff of cloning: an install that fails leaves the previous
// prefix exactly as it was, because nothing was ever written into it.
func Swap(staging, final string) error {
	backup := final + ".previous"
	os.RemoveAll(backup)
	if _, err := os.Stat(final); err == nil {
		if err := os.Rename(final, backup); err != nil {
			return fmt.Errorf("set aside previous prefix: %w", err)
		}
	}
	if err := os.Rename(staging, final); err != nil {
		os.Rename(backup, final) // put the old one back
		return fmt.Errorf("install prefix into place: %w", err)
	}
	os.RemoveAll(backup)
	return normalizeDrives(final)
}

// normalizeDrives repoints drive_c at the prefix it now lives in.
//
// wineboot writes dosdevices/c: as the relative link ../drive_c, which
// survives both the clone and the rename. Some tooling rewrites it to an
// absolute path, and an absolute link would leave a moved prefix quietly
// reading and writing the baseline. Cheap to check, expensive to miss.
func normalizeDrives(prefix string) error {
	link := filepath.Join(prefix, "dosdevices", "c:")
	target, err := os.Readlink(link)
	if err != nil {
		return nil // no dosdevices yet, or c: is a real directory
	}
	if target == "../drive_c" {
		return nil
	}
	if err := os.Remove(link); err != nil {
		return err
	}
	return os.Symlink("../drive_c", link)
}

// DOSPath maps a host path onto the prefix's drive letters.
//
// Wine maps Z: to / by default, so every host path is already addressable
// from inside the prefix and installer payloads never need copying in. The
// longest-target-wins scan matters when the payload sits under the prefix
// itself, where both C: and Z: match and only C: is correct.
func DOSPath(prefix, host string) (string, error) {
	abs, err := filepath.Abs(host)
	if err != nil {
		return "", err
	}
	entries, err := os.ReadDir(filepath.Join(prefix, "dosdevices"))
	if err != nil {
		return "", err
	}

	type drive struct{ letter, root string }
	var drives []drive
	for _, e := range entries {
		name := e.Name()
		if len(name) != 2 || name[1] != ':' {
			continue // skip c:: and the like, which are device nodes
		}
		root, err := filepath.EvalSymlinks(filepath.Join(prefix, "dosdevices", name))
		if err != nil {
			continue
		}
		drives = append(drives, drive{letter: strings.ToUpper(name[:1]), root: root})
	}
	sort.Slice(drives, func(i, j int) bool { return len(drives[i].root) > len(drives[j].root) })

	for _, d := range drives {
		rel, err := filepath.Rel(d.root, abs)
		if err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
			continue
		}
		if rel == "." {
			return d.letter + `:\`, nil
		}
		return d.letter + `:\` + strings.ReplaceAll(rel, "/", `\`), nil
	}
	return "", fmt.Errorf("no drive in %s maps %s", prefix, abs)
}

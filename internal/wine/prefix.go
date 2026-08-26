package wine

import (
	"context"
	"fmt"
	"io/fs"
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
		// A baseline created before viniget started disabling the menu
		// builder still has it on, and so does every prefix cloned from it.
		disableMenuBuilder(ctx, s.Base(), verbose)
		ContainDesktop(s.Base())
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
	if _, err := ContainDesktop(s.Base()); err != nil {
		os.RemoveAll(s.Base())
		return err
	}
	disableMenuBuilder(ctx, s.Base(), verbose)
	return nil
}

// menuBuilderOff is the registry value NoMenuBuilder writes, as it appears in
// user.reg once wineserver has flushed it.
const menuBuilderOff = `"winemenubuilder.exe"=""`

// disableMenuBuilder turns wine's menu builder off inside the prefix itself.
//
// Runner.Env already disables it for everything viniget runs, so this is for
// everything it does not: the launcher scripts, and the `WINEPREFIX=... wine
// <exe>` line viniget prints for a package it found no entry point for.
// Without it an application that writes a Start Menu shortcut on first launch
// still puts a second, wine-generated entry in the menu -- see NoMenuBuilder.
// Written to the baseline only, because every install is a clone of it.
//
// Best-effort by design: the environment variable is the guarantee, and a
// prefix wine cannot write a registry key into is not one worth failing an
// install over.
func disableMenuBuilder(ctx context.Context, prefix string, verbose bool) {
	if data, err := os.ReadFile(filepath.Join(prefix, "user.reg")); err == nil &&
		strings.Contains(string(data), menuBuilderOff) {
		return
	}
	r := &Runner{Prefix: prefix, Display: Headless(), Verbose: verbose}
	if _, err := r.Run(ctx, ServerWait, "wine", "reg", "add", `HKCU\Software\Wine\DllOverrides`,
		"/v", "winemenubuilder.exe", "/t", "REG_SZ", "/d", "", "/f"); err != nil {
		return
	}
	// The key lives in the running wineserver until it exits, and the check
	// above reads the file, so settling here is what keeps this from running
	// again on every install.
	r.Settle(ctx)
}

// Clone reflinks the baseline prefix to dest, replacing anything already there.
func (s *Store) Clone(dest string) error {
	return s.CloneFrom(s.Base(), dest)
}

// CloneFrom reflinks any complete prefix to dest, replacing dest first.
// Helper transactions use it to checkpoint an installed package without
// mutating the live prefix. Clone remains the baseline-specific convenience.
func (s *Store) CloneFrom(src, dest string) error {
	if src == "" || dest == "" || filepath.Clean(src) == filepath.Clean(dest) {
		return fmt.Errorf("clone prefix: unusable source or destination")
	}
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
	out, err := exec.Command(cp, "-a", "--reflink=auto", src, dest).CombinedOutput()
	if err != nil {
		_ = os.RemoveAll(dest)
		return fmt.Errorf("clone prefix: %v: %s", err, strings.TrimSpace(string(out)))
	}
	// Done on the clone rather than trusted from the baseline: a baseline
	// made by an older viniget still carries the symlink, and cp -a preserves
	// it faithfully.
	if _, err := ContainDesktop(dest); err != nil {
		_ = os.RemoveAll(dest)
		return err
	}
	if err := normalizeDrives(dest); err != nil {
		_ = os.RemoveAll(dest)
		return err
	}
	return nil
}

// ContainDesktop replaces a prefix's Desktop folders with real directories,
// and returns the ones it had to change.
//
// wineboot points drive_c/users/<user>/Desktop at $HOME/Desktop, and that one
// symlink breaks desktop integration from both ends. An installer that offers
// to "create a desktop shortcut" writes its .lnk onto the real desktop, where
// it is a Windows shortcut file no file manager on Linux can launch -- and
// viniget never sees it, because the walk that collects shortcuts does not
// descend through a symlinked root, so a package whose only shortcut is that
// one produces no menu entry at all. Reading through the link instead would
// be worse: every prefix shares that directory, so each install would be
// scanning every other package's shortcuts.
//
// Only Desktop is contained. Documents, Downloads, Pictures and the rest stay
// linked to the user's own: an application that cannot reach the files it was
// installed to open is a regression, and none of them feed the menu.
//
// The registry keeps naming C:\users\<user>\Desktop either way, so the
// installer still writes exactly where it meant to -- inside the prefix now.
func ContainDesktop(prefix string) ([]string, error) {
	matches, _ := filepath.Glob(filepath.Join(prefix, "drive_c", "users", "*", "Desktop"))
	var contained []string
	for _, dir := range matches {
		info, err := os.Lstat(dir)
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			continue
		}
		if err := os.Remove(dir); err != nil {
			return contained, fmt.Errorf("unlink %s: %w", dir, err)
		}
		if err := os.Mkdir(dir, 0o755); err != nil {
			return contained, fmt.Errorf("replace %s: %w", dir, err)
		}
		contained = append(contained, dir)
	}
	return contained, nil
}

// Removal is what deleting one package's prefixes took off disk.
type Removal struct {
	// Paths are the directories removed: the package's prefix, plus any
	// staging or backup sibling an interrupted install left beside it.
	Paths []string `json:"paths,omitempty"`
	// Bytes is their apparent size -- the sum of the file sizes, which is
	// deliberately not a claim about space reclaimed. A prefix is a reflink
	// clone of a baseline that is still there, so how much of this the
	// filesystem gives back depends on which extents the baseline still holds.
	Bytes int64 `json:"bytes"`
}

// Paths returns the prefix directories that exist for a package.
//
// More than one can, and the extras are the interesting part: ".installing"
// is a failed install kept for inspection, ".helping" is an interrupted
// inferred-helper checkpoint, and the ".previous" forms are swaps that did
// not finish. All are this package's state and nothing else ever looks at
// them, so whatever removes the prefix has to remove them too.
func (s *Store) Paths(id string) []string {
	prefix, err := s.For(id)
	if err != nil {
		return nil
	}
	var out []string
	for _, path := range []string{
		prefix,
		prefix + ".installing",
		prefix + ".previous",
		prefix + ".helping",
		prefix + ".installing.previous",
	} {
		if _, err := os.Lstat(path); err == nil {
			out = append(out, path)
		}
	}
	return out
}

// Remove deletes a package's prefix and its siblings.
//
// The baseline is refused by name. safeID only guarantees an identifier stays
// inside the prefixes directory, and "base" is inside it: a package that
// managed to be called that would otherwise take the prefix every other
// install is cloned from with it, costing the next install a 20-40s wineboot
// and -- since the clones hold the extents -- freeing almost nothing.
func (s *Store) Remove(id string) (Removal, error) {
	prefix, err := s.For(id)
	if err != nil {
		return Removal{}, err
	}
	if prefix == s.Base() {
		return Removal{}, fmt.Errorf("%q is viniget's baseline prefix, not an installed package", id)
	}

	var rm Removal
	for _, path := range s.Paths(id) {
		size := apparentSize(path)
		if err := os.RemoveAll(path); err != nil {
			return rm, fmt.Errorf("remove prefix %s: %w", path, err)
		}
		rm.Paths = append(rm.Paths, path)
		rm.Bytes += size
	}
	return rm, nil
}

// apparentSize sums the regular file sizes under a path.
//
// Apparent rather than blocks allocated, because on a reflinked prefix
// neither number is the space a delete gives back and only this one is
// stable: du reports the baseline's 444 MB against every clone of it, so
// "freed 4.8 GB" would be the reading for eight packages sharing 1 GB.
// Unreadable entries are skipped rather than failing the walk -- a size is
// never worth refusing to uninstall over.
func apparentSize(root string) int64 {
	var total int64
	filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil && info.Mode().IsRegular() {
			total += info.Size()
		}
		return nil
	})
	return total
}

// Swap moves a freshly installed staging prefix into its final home, keeping
// the previous one until the move succeeds.
//
// This is the payoff of cloning: an install that fails leaves the previous
// prefix exactly as it was, because nothing was ever written into it.
func Swap(staging, final string) error {
	// Validate and normalize the candidate while the live prefix is still in
	// place. Doing this after the renames could commit the candidate and only
	// then discover an unusable drive mapping, violating the transaction the
	// caller relies on.
	if err := normalizeDrives(staging); err != nil {
		return fmt.Errorf("normalize staging prefix: %w", err)
	}
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
	return nil
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

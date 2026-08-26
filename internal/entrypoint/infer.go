// Package entrypoint ranks executable files when a package provides no
// authoritative launch target.
package entrypoint

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Root is one directory in which executables may be inferred. SkipDirs names
// directory basenames that must not be descended into, case-insensitively.
// Baseline is the corresponding directory in a pristine prefix; files already
// present there are infrastructure rather than part of the installed package.
type Root struct {
	Path     string
	Baseline string
	SkipDirs []string
}

type candidate struct {
	path           string
	rel            string
	key            string
	nameScore      int
	directoryScore int
	depth          int
	root           int
}

// Infer finds the executable a package most likely expects a user to launch.
//
// Package-related names beat unrelated ones, files nearer a search root beat
// bundled tools, and shorter names break the remaining ties. Obvious
// installers, uninstallers, updaters and crash helpers are excluded unless
// their name exactly matches a package hint. Root order and the final path
// comparison keep the result deterministic when choices are otherwise equal.
func Infer(roots []Root, id string, hints ...string) (string, error) {
	keys := packageHints(id, hints...)
	found := make([]candidate, 0, 16)
	seen := make(map[string]bool)

	for i, root := range roots {
		if root.Path == "" {
			continue
		}
		err := filepath.WalkDir(root.Path, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				if os.IsNotExist(err) {
					return nil
				}
				return err
			}
			if d.IsDir() {
				if path != root.Path && skippedDir(d.Name(), root.SkipDirs) {
					return filepath.SkipDir
				}
				return nil
			}
			if seen[path] || !strings.EqualFold(filepath.Ext(d.Name()), ".exe") {
				return nil
			}
			seen[path] = true
			rel, err := filepath.Rel(root.Path, path)
			if err != nil {
				return err
			}
			if root.Baseline != "" {
				if _, err := os.Stat(filepath.Join(root.Baseline, rel)); err == nil {
					return nil
				}
			}
			key := nameKey(strings.TrimSuffix(d.Name(), filepath.Ext(d.Name())))
			nameScore := nameMatch(key, keys)
			if nameScore != 0 && noisyName(key) {
				return nil
			}
			directoryScore := directoryMatch(rel, keys)
			found = append(found, candidate{
				path:           path,
				rel:            rel,
				key:            key,
				nameScore:      nameScore,
				directoryScore: directoryScore,
				depth:          strings.Count(filepath.ToSlash(rel), "/"),
				root:           i,
			})
			return nil
		})
		if err != nil {
			return "", err
		}
	}

	if len(found) == 0 {
		return "", nil
	}
	sort.Slice(found, func(i, j int) bool {
		a, b := found[i], found[j]
		// The executable's own name is stronger evidence than its containing
		// directory. Every WinRAR binary lives under Program Files/WinRAR;
		// flattening those two signals into one score made the shortest sibling,
		// the console-only Rar.exe, beat the exact WinRAR.exe match.
		if a.nameScore != b.nameScore {
			return a.nameScore < b.nameScore
		}
		if a.directoryScore != b.directoryScore {
			return a.directoryScore < b.directoryScore
		}
		if a.depth != b.depth {
			return a.depth < b.depth
		}
		if len(a.key) != len(b.key) {
			return len(a.key) < len(b.key)
		}
		if a.root != b.root {
			return a.root < b.root
		}
		al, bl := strings.ToLower(a.rel), strings.ToLower(b.rel)
		if al != bl {
			return al < bl
		}
		return a.rel < b.rel
	})
	return found[0].path, nil
}

// directoryMatch lets a conventional package directory identify an unusually
// named main binary. It is deliberately kept separate from the executable's
// own name score: a package-named directory is useful evidence for runtime.exe,
// but must not make runtime.exe tie an exact Demo.exe filename match.
func directoryMatch(rel string, hints []string) int {
	best := 2
	dir := filepath.Dir(rel)
	for dir != "." && dir != string(filepath.Separator) {
		part := filepath.Base(dir)
		if score := nameMatch(nameKey(part), hints); score < best {
			best = score
		}
		next := filepath.Dir(dir)
		if next == dir {
			break
		}
		dir = next
	}
	return best
}

func skippedDir(name string, skipped []string) bool {
	for _, skip := range skipped {
		if strings.EqualFold(name, skip) {
			return true
		}
	}
	return false
}

// packageHints turns the package identifier and display name into the filename
// fragments publishers commonly reuse for their main executable.
func packageHints(id string, hints ...string) []string {
	seen := map[string]bool{}
	var keys []string
	add := func(s string) {
		base := filepath.Base(strings.SplitN(s, "?", 2)[0])
		key := nameKey(strings.TrimSuffix(base, filepath.Ext(base)))
		if key == "" || seen[key] || genericHint(key) {
			return
		}
		seen[key] = true
		keys = append(keys, key)
	}

	add(id)
	parts := strings.Split(id, ".")
	if len(parts) > 1 {
		// The first identifier segment is normally the publisher. Later
		// segments cover IDs such as Vendor.Product.Portable when PackageName
		// is unavailable.
		for _, part := range parts[1:] {
			add(part)
		}
	}
	for _, hint := range hints {
		add(hint)
	}
	return keys
}

// nameMatch returns 0 for an exact hint match, 1 for a useful partial match
// and 2 for an unrelated name.
func nameMatch(name string, hints []string) int {
	best := 2
	for _, hint := range hints {
		if name == hint {
			return 0
		}
		if len(name) >= 3 && len(hint) >= 3 &&
			(strings.Contains(name, hint) || strings.Contains(hint, name)) {
			best = 1
		}
	}
	return best
}

func nameKey(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func genericHint(s string) bool {
	switch s {
	case "portable", "x64", "x86", "win64", "win32", "64bit", "32bit":
		return true
	}
	return false
}

func noisyName(s string) bool {
	for _, fragment := range []string{
		"unins", "uninstall", "setup", "install", "update",
		"crashpad", "crashreport", "helper", "vcredist",
	} {
		if strings.Contains(s, fragment) {
			return true
		}
	}
	return false
}

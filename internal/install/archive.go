package install

import (
	"archive/zip"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// unzip extracts a zip payload into dest.
//
// Go's archive/zip is used rather than shelling out to 7z, which the Python
// prototype did: it removes a runtime dependency, and it lets viniget reject
// entries that would escape dest. Winget payloads are vendor-controlled and
// hash-verified, but "..\..\windows\system32" inside a zip would otherwise
// write straight through the prefix boundary.
func unzip(src, dest string) error {
	zr, err := zip.OpenReader(src)
	if err != nil {
		return fmt.Errorf("open archive: %w", err)
	}
	defer zr.Close()

	for _, f := range zr.File {
		target, err := securePath(dest, f.Name)
		if err != nil {
			return err
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := extractFile(f, target); err != nil {
			return err
		}
	}
	return nil
}

func extractFile(f *zip.File, target string) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()

	// Executable bits from the archive are meaningless to Wine but do decide
	// whether the host can run the file through wine at all.
	mode := f.Mode().Perm() | 0o644
	out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, rc); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// securePath joins a zip entry name onto dest, rejecting anything that could
// land outside it.
//
// Rejecting rather than clamping is deliberate. A clamped
// "..\..\windows\system32\evil.dll" still gets written, just somewhere
// arbitrary, and a payload that ships such an entry is either malicious or
// broken -- neither is worth extracting. Windows archives use backslashes,
// which filepath.Clean leaves alone on Linux, so names are normalised first.
func securePath(dest, name string) (string, error) {
	slashed := strings.ReplaceAll(name, `\`, "/")
	reject := func() (string, error) {
		return "", fmt.Errorf("archive entry %q escapes the extraction directory", name)
	}
	if strings.HasPrefix(slashed, "/") || strings.Contains(strings.SplitN(slashed, "/", 2)[0], ":") {
		return reject()
	}
	clean := path.Clean(slashed)
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return reject()
	}
	target := filepath.Join(dest, filepath.FromSlash(clean))
	if target != dest && !strings.HasPrefix(target, dest+string(os.PathSeparator)) {
		return reject()
	}
	return target, nil
}

// findNested locates a NestedInstallerFiles entry inside an extracted archive.
//
// The lookup is case-insensitive because a Wine prefix is a case-sensitive
// directory tree standing in for a case-insensitive filesystem, and manifest
// authors write the path however it looked in Explorer.
func findNested(root, rel string) (string, bool) {
	rel = strings.ReplaceAll(rel, `\`, "/")
	direct := filepath.Join(root, filepath.Clean("/"+rel))
	if _, err := os.Stat(direct); err == nil {
		return direct, true
	}
	return lookupInsensitive(root, strings.Split(strings.Trim(rel, "/"), "/"))
}

func lookupInsensitive(root string, parts []string) (string, bool) {
	cur := root
	for _, part := range parts {
		if part == "" || part == "." {
			continue
		}
		next := filepath.Join(cur, part)
		if _, err := os.Stat(next); err == nil {
			cur = next
			continue
		}
		entries, err := os.ReadDir(cur)
		if err != nil {
			return "", false
		}
		found := false
		for _, e := range entries {
			if strings.EqualFold(e.Name(), part) {
				cur, found = filepath.Join(cur, e.Name()), true
				break
			}
		}
		if !found {
			return "", false
		}
	}
	return cur, true
}

// inferPortableEntry finds the executable an extracted portable package most
// likely expects a user to launch. Portable archives do not create Start Menu
// shortcuts and some manifests omit NestedInstallerFiles, so there is no
// authoritative source left in that case.
//
// Package-related names beat unrelated ones, files nearer the archive root
// beat bundled tools, and shorter names break the remaining ties. Obvious
// installers, uninstallers, updaters and crash helpers are excluded unless
// their name exactly matches a package hint. The final path comparison keeps
// the result deterministic even when the archive contains equivalent choices.
func inferPortableEntry(root, id string, hints ...string) (string, error) {
	type candidate struct {
		path  string
		rel   string
		key   string
		match int
		depth int
	}

	keys := portableHints(id, hints...)
	var found []candidate
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.EqualFold(filepath.Ext(d.Name()), ".exe") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		key := portableKey(strings.TrimSuffix(d.Name(), filepath.Ext(d.Name())))
		match := portableNameMatch(key, keys)
		if match != 0 && portableNoise(key) {
			return nil
		}
		found = append(found, candidate{
			path: path, rel: rel, key: key, match: match,
			depth: strings.Count(filepath.ToSlash(rel), "/"),
		})
		return nil
	})
	if err != nil {
		return "", err
	}
	if len(found) == 0 {
		return "", nil
	}

	sort.Slice(found, func(i, j int) bool {
		a, b := found[i], found[j]
		if a.match != b.match {
			return a.match < b.match
		}
		if a.depth != b.depth {
			return a.depth < b.depth
		}
		if len(a.key) != len(b.key) {
			return len(a.key) < len(b.key)
		}
		al, bl := strings.ToLower(a.rel), strings.ToLower(b.rel)
		if al != bl {
			return al < bl
		}
		return a.rel < b.rel
	})
	return found[0].path, nil
}

// portableHints turns the package identifier, display name and command aliases
// into the filename fragments publishers commonly reuse for the main
// executable.
func portableHints(id string, hints ...string) []string {
	seen := map[string]bool{}
	var keys []string
	add := func(s string) {
		base := filepath.Base(strings.SplitN(s, "?", 2)[0])
		key := portableKey(strings.TrimSuffix(base, filepath.Ext(base)))
		if key == "" || seen[key] || portableGenericHint(key) {
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

// portableNameMatch returns 0 for an exact hint match, 1 for a useful partial
// match and 2 for an unrelated name.
func portableNameMatch(name string, hints []string) int {
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

func portableKey(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func portableGenericHint(s string) bool {
	switch s {
	case "portable", "x64", "x86", "win64", "win32", "64bit", "32bit":
		return true
	}
	return false
}

func portableNoise(s string) bool {
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

// isZip reports whether a payload is really a zip, whatever type the manifest
// declared for it.
//
// SumatraPDF is the worked example: its portable variant declares
// InstallerType "portable" and ships a .zip. Trusting the declared type drops
// the archive into Program Files as one unopened file, which scores as
// files-but-no-exe -- installed, in the sense that nothing runnable came out
// of it. The magic number is checked rather than the extension because the
// same manifests that lie about the type also lie about the filename.
func isZip(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	var magic [4]byte
	if _, err := io.ReadFull(f, magic[:]); err != nil {
		return false
	}
	return magic == [4]byte{'P', 'K', 0x03, 0x04}
}

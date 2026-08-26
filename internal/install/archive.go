package install

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/viniget/viniget/internal/entrypoint"
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
	return entrypoint.Infer([]entrypoint.Root{{Path: root}}, id, hints...)
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

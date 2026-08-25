package desktop

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Cleaning up after wine's own menu builder.
//
// winemenubuilder.exe is disabled for everything viniget drives -- see
// wine.NoMenuBuilder -- but it ran for every install made before that, and it
// writes into the same two directories viniget does. What it left behind is a
// .desktop file per Start Menu shortcut naming the ".installing" staging
// prefix that no longer exists, sitting in the menu next to viniget's own
// working entry with a near-identical name and icon.
//
// Nothing else will ever remove those: they are wine's files by author and
// viniget's by subject, and wine only revisits them while running against the
// prefix they came from, which in this case has been renamed out from under
// them. So integration and uninstall both sweep them, keyed on the prefix
// path they name.

// wineMenuDir is where winemenubuilder puts its entries, relative to the
// applications directory.
const wineMenuDir = "wine"

// PruneWineMenu removes the menu entries wine's own menu builder wrote for a
// prefix, and the icons they named. It returns what was deleted.
//
// The prefix is matched against its staging and backup siblings too, since
// the entries worth removing are precisely the ones written mid-install.
func PruneWineMenu(dirs Dirs, prefix string) []string {
	if prefix == "" || dirs.Applications == "" {
		return nil
	}
	root := filepath.Join(dirs.Applications, wineMenuDir)

	var stale []string
	icons := map[string]bool{}
	filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".desktop") {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil || !referencesPrefix(string(body), prefix) {
			return nil
		}
		stale = append(stale, path)
		if name := entryIcon(string(body)); name != "" {
			icons[name] = true
		}
		return nil
	})
	if len(stale) == 0 {
		return nil
	}

	var removed []string
	for _, path := range stale {
		if err := removeIfPresent(path); err == nil {
			removed = append(removed, path)
		}
	}
	removed = append(removed, pruneIcons(dirs, icons)...)
	pruneEmptyDirs(root)
	sort.Strings(removed)
	return removed
}

// referencesPrefix reports whether a .desktop file names the prefix or one of
// the siblings an install leaves beside it.
//
// The boundary check is the whole point: a plain substring test would read
// ".../prefixes/Mozilla.Firefox" as a reference in an entry belonging to
// ".../prefixes/Mozilla.Firefox.ESR", and delete another package's menu.
func referencesPrefix(body, prefix string) bool {
	for _, cand := range []string{prefix, prefix + ".installing", prefix + ".previous"} {
		for i := 0; i < len(body); {
			j := strings.Index(body[i:], cand)
			if j < 0 {
				break
			}
			end := i + j + len(cand)
			if end == len(body) || pathEnd(body[end]) {
				return true
			}
			i = end
		}
	}
	return false
}

// pathEnd reports whether c ends a path rather than continuing it. Wine
// writes the prefix into Exec= inside double quotes and into Path= bare, so
// both a quote and a newline are terminators, and a slash means the match was
// the whole directory component.
func pathEnd(c byte) bool {
	switch c {
	case '/', '"', '\'', ' ', '\t', '\n', '\r':
		return true
	}
	return false
}

// entryIcon reads the Icon key out of a desktop entry.
func entryIcon(body string) string {
	for _, line := range strings.Split(body, "\n") {
		if name, ok := strings.CutPrefix(strings.TrimSpace(line), "Icon="); ok {
			return strings.TrimSpace(name)
		}
	}
	return ""
}

// pruneIcons deletes the icon files named by entries that were just removed.
//
// An icon is only deleted once nothing left in the menu names it. Wine's icon
// filenames are a hash of the source path and its index, so two prefixes
// holding the same application can land on the same name -- and an icon
// deleted out from under somebody else's working entry is a worse outcome
// than one stale file left on disk.
func pruneIcons(dirs Dirs, names map[string]bool) []string {
	if len(names) == 0 || dirs.Icons == "" {
		return nil
	}
	for name := range referencedIcons(dirs) {
		delete(names, name)
	}

	var removed []string
	for name := range names {
		// An absolute path is a legal Icon value and is not wine's doing;
		// viniget's own icons are never reached from a wine entry.
		if name == "" || filepath.IsAbs(name) || strings.HasPrefix(name, "viniget-") {
			continue
		}
		for _, path := range iconFiles(dirs, name) {
			if err := removeIfPresent(path); err == nil {
				removed = append(removed, path)
			}
		}
	}
	return removed
}

// referencedIcons is every icon name the surviving menu still points at.
func referencedIcons(dirs Dirs) map[string]bool {
	out := map[string]bool{}
	filepath.WalkDir(dirs.Applications, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".desktop") {
			return nil
		}
		if body, err := os.ReadFile(path); err == nil {
			if name := entryIcon(string(body)); name != "" {
				out[name] = true
			}
		}
		return nil
	})
	return out
}

// iconFiles lists the theme files carrying one icon name.
//
// The sizes are read rather than globbed: an icon name comes from an
// application's own filename, and "IrfanView [4.75]" is a glob pattern.
func iconFiles(dirs Dirs, name string) []string {
	var out []string
	sizes, err := os.ReadDir(filepath.Join(dirs.Icons, "hicolor"))
	if err != nil {
		return nil
	}
	for _, size := range sizes {
		for _, ext := range []string{".png", ".xpm", ".svg"} {
			path := filepath.Join(dirs.Icons, "hicolor", size.Name(), "apps", name+ext)
			if _, err := os.Lstat(path); err == nil {
				out = append(out, path)
			}
		}
	}
	return out
}

// pruneEmptyDirs removes the directories a swept entry left behind. Wine
// mirrors the Start Menu's folders, so removing "Programs/Foo/Foo.desktop"
// leaves an empty "Foo" that some menus render as an empty submenu.
func pruneEmptyDirs(root string) {
	var dirs []string
	filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() && path != root {
			dirs = append(dirs, path)
		}
		return nil
	})
	// Deepest first, so a directory emptied by its own children goes too.
	sort.Sort(sort.Reverse(sort.StringSlice(dirs)))
	for _, dir := range dirs {
		if entries, err := os.ReadDir(dir); err == nil && len(entries) == 0 {
			os.Remove(dir)
		}
	}
}

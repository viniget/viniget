package desktop

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Package is what desktop integration needs to know about an installed
// package. It is assembled from the manifest at install time and from the
// installed-package record afterwards, which is why nothing here is required
// beyond the identifier and the prefix.
type Package struct {
	ID      string
	Name    string
	Version string
	// Comment is the manifest's short description.
	Comment string
	// Tags are the manifest's tags, which become menu categories and keywords.
	Tags []string
	// Extensions are the manifest's FileExtensions, which become associations.
	Extensions []string
	// Prefix is the wine prefix the package was installed into.
	Prefix string
	// Entry is a manifest-declared entry point. Archive packages have no
	// installer to write a shortcut, so this is all they ever have.
	Entry string
	// DLLOverrides is the WINEDLLOVERRIDES the generated launchers carry, for
	// the few applications that will not start under wine without one.
	DLLOverrides string
}

// Options configures one integration.
type Options struct {
	Dirs Dirs
	// NoAssociations leaves MimeType= off the entry and writes no MIME
	// declaration, for a package whose declared file types are not wanted.
	NoAssociations bool
	// NoRefresh skips the desktop database updaters. They are the slow part
	// and are pure cache maintenance, so a batch run does them once at the end
	// rather than once per package.
	NoRefresh bool
}

// App is one launchable application discovered in a prefix.
type App struct {
	// Name is the menu label: the shortcut's own filename, which is what
	// Windows displays. It is deliberately not the package name -- Git
	// installs "Git Bash" and "Git GUI", and calling either of them "Git"
	// would be wrong.
	Name string `json:"name"`
	// Target is the host path of the executable.
	Target string `json:"target"`
	// Args are the fixed arguments the shortcut carried.
	Args []string `json:"args,omitempty"`
	// WorkingDir is the host directory the shortcut launches from.
	WorkingDir string `json:"working_dir,omitempty"`
	// Description is the shortcut's own comment, when it has one.
	Description string `json:"description,omitempty"`
	// Icon names the file the icon is read out of, and which icon in it.
	Icon IconSource `json:"-"`
	// Source records where the app was found: the Start Menu, the Desktop, or
	// the manifest. Only the first is authoritative.
	Source string `json:"source"`
	// Slug distinguishes the secondary entries of a suite. The primary app
	// has none.
	Slug string `json:"slug,omitempty"`
}

// Written is what integration produced for one app.
type Written struct {
	App
	DesktopFile string `json:"desktop_file"`
	Launcher    string `json:"launcher"`
	// DLLOverrides is the WINEDLLOVERRIDES the launcher was written with.
	DLLOverrides string   `json:"dll_overrides,omitempty"`
	IconName     string   `json:"icon,omitempty"`
	IconFiles    []string `json:"icon_files,omitempty"`
	IconSizes    []int    `json:"icon_sizes,omitempty"`
}

// Result is everything one integration produced.
type Result struct {
	Apps      []Written  `json:"apps"`
	MIMETypes []MIMEType `json:"mime_types,omitempty"`
	MIMEFile  string     `json:"mime_file,omitempty"`
	// Files is every path written, so removal can be exact rather than a glob
	// over a directory full of other packages' entries.
	Files []string `json:"files,omitempty"`
	Notes []string `json:"notes,omitempty"`
}

// Entry returns the primary launch target, which is what viniget reports as
// the package's entry point.
func (r *Result) Entry() string {
	if len(r.Apps) == 0 {
		return ""
	}
	return r.Apps[0].Target
}

func (r *Result) note(format string, a ...any) {
	r.Notes = append(r.Notes, fmt.Sprintf(format, a...))
}

// Apps discovers the launchable applications a prefix contains, best first.
//
// The Start Menu is the source of truth. A package that installed no shortcut
// at all falls back to the entry point its manifest declared, which is the
// only thing archive packages ever have.
func Apps(pkg Package) []App {
	var apps []App
	seen := map[string]bool{}

	for _, sc := range FindShortcuts(pkg.Prefix, pkg.Name) {
		target := sc.Resolve(pkg.Prefix)
		if target == "" || !strings.EqualFold(filepath.Ext(target), ".exe") {
			continue
		}
		// A Desktop shortcut is nearly always a duplicate of a Start Menu one.
		// Ranking already put the Start Menu first, so the first sighting of a
		// target wins and the duplicate is dropped.
		if seen[target] {
			continue
		}
		seen[target] = true

		source := "start-menu"
		if sc.Desktop {
			source = "desktop"
		}
		apps = append(apps, App{
			Name:        sc.Label(),
			Target:      target,
			Args:        SplitWindowsArgs(sc.Arguments),
			WorkingDir:  sc.ResolveWorkingDir(pkg.Prefix),
			Description: sc.Name,
			Icon:        iconSourceFor(sc, pkg.Prefix, target),
			Source:      source,
		})
	}

	if len(apps) == 0 && pkg.Entry != "" {
		if target := existingPath(pkg.Entry); target != "" {
			name := pkg.Name
			if name == "" {
				name = strings.TrimSuffix(filepath.Base(target), filepath.Ext(target))
			}
			apps = append(apps, App{
				Name:   name,
				Target: target,
				Icon:   IconSource{Path: target},
				Source: "manifest",
			})
		}
	}

	assignSlugs(apps)
	return apps
}

// iconSourceFor picks where an app's icon is read from: the shortcut's own
// IconLocation when it names something that exists, and otherwise the
// executable, which carries its own icon in almost every case.
func iconSourceFor(sc *Shortcut, prefix, target string) IconSource {
	if sc.IconLocation != "" {
		loc, index := ParseIconLocation(sc.IconLocation)
		if host := resolveWindowsPath(prefix, loc); host != "" {
			return IconSource{Path: host, Index: index}
		}
	}
	return IconSource{Path: target}
}

// assignSlugs names the secondary entries of a suite. The primary keeps the
// bare package identifier so that its .desktop file has a stable, predictable
// name, and the rest are distinguished by their label.
func assignSlugs(apps []App) {
	used := map[string]bool{}
	for i := range apps {
		if i == 0 {
			continue
		}
		slug := slugify(apps[i].Name)
		if slug == "" {
			slug = fmt.Sprintf("%d", i)
		}
		base := slug
		for n := 2; used[slug]; n++ {
			slug = fmt.Sprintf("%s-%d", base, n)
		}
		used[slug] = true
		apps[i].Slug = slug
	}
}

func slugify(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			dash = false
		case b.Len() > 0 && !dash:
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

// Integrate generates the desktop entries, icons, launchers and associations
// for an installed package.
//
// A package with nothing launchable in it is not an error: plenty of the
// catalog is codecs, runtimes and resource packs, and a runtime that installed
// correctly and put nothing in the Start Menu has behaved exactly as intended.
func Integrate(pkg Package, opt Options) (*Result, error) {
	res := &Result{}
	if pkg.Prefix == "" {
		return res, fmt.Errorf("desktop: package %s has no prefix", pkg.ID)
	}

	// Regenerating replaces rather than accumulates. An upgrade that dropped a
	// shortcut would otherwise leave its entry in the menu forever, pointing
	// at an executable that is no longer there -- and everything written here
	// is derived from the prefix, so clearing first costs nothing.
	Remove(opt.Dirs, pkg.ID, nil)

	apps := Apps(pkg)
	if len(apps) == 0 {
		res.note("nothing launchable found: no Start Menu shortcut resolved to an executable, and the manifest declares no entry point")
		return res, nil
	}

	var types []MIMEType
	if !opt.NoAssociations {
		types = ResolveMIMETypes(pkg.Extensions)
		res.MIMETypes = types
	}

	categories := Categories(pkg.Tags)
	keywords := Keywords(append([]string{pkg.Name}, pkg.Tags...))

	for i, app := range apps {
		stem := "viniget-" + pkg.ID
		if app.Slug != "" {
			stem += "-" + app.Slug
		}

		launcher := &Launcher{
			ID:           pkg.ID,
			Prefix:       pkg.Prefix,
			Target:       app.Target,
			Args:         app.Args,
			WorkingDir:   app.WorkingDir,
			DLLOverrides: pkg.DLLOverrides,
		}
		scriptPath, err := launcher.Write(opt.Dirs.Launchers, LauncherName(pkg.ID, app.Slug))
		if err != nil {
			return res, fmt.Errorf("write launcher: %w", err)
		}
		res.Files = append(res.Files, scriptPath)

		w := Written{App: app, Launcher: scriptPath, DLLOverrides: pkg.DLLOverrides}
		w.IconName, w.IconFiles, w.IconSizes = installIcon(app.Icon, opt.Dirs, stem, res)
		res.Files = append(res.Files, w.IconFiles...)

		comment := pkg.Comment
		if comment == "" {
			comment = app.Description
		}
		entry := &Entry{
			Name:       app.Name,
			Comment:    comment,
			Exec:       ExecLine(scriptPath),
			Path:       launcher.workingDir(),
			Icon:       w.IconName,
			Categories: categories,
			Keywords:   keywords,
			Package:    pkg.ID,
			Prefix:     pkg.Prefix,
			Version:    pkg.Version,
		}
		// Associations go on the primary entry only. A suite's secondary
		// entries -- Git GUI next to Git Bash -- have no claim on the
		// package's file types.
		if i == 0 {
			entry.MimeTypes = uniqueTypes(types)
		}

		w.DesktopFile = filepath.Join(opt.Dirs.Applications, stem+".desktop")
		if err := entry.Write(w.DesktopFile); err != nil {
			return res, fmt.Errorf("write desktop entry: %w", err)
		}
		res.Files = append(res.Files, w.DesktopFile)
		res.Apps = append(res.Apps, w)
	}

	if len(types) > 0 {
		path, err := WriteMIMEPackage(opt.Dirs.MIME, pkg.ID, pkg.Name, types)
		if err != nil {
			return res, fmt.Errorf("write mime declaration: %w", err)
		}
		if path != "" {
			res.MIMEFile = path
			res.Files = append(res.Files, path)
		}
	}

	if !opt.NoRefresh {
		Refresh(opt.Dirs, res.MIMEFile != "", res)
	}
	return res, nil
}

// installIcon extracts an icon and puts it where the entry can name it.
//
// The return is the value for the entry's Icon key: a theme name when the
// icon had at least one size the hicolor theme has a directory for, and an
// absolute path when it did not. Both are legal, and the fallback is what
// keeps an application with only a 20x20 icon from having no icon at all.
func installIcon(src IconSource, dirs Dirs, stem string, res *Result) (string, []string, []int) {
	if src.Path == "" {
		return "", nil, nil
	}
	images, err := IconImages(src)
	if err != nil {
		res.note("no icon in %s: %v", filepath.Base(src.Path), err)
		return "", nil, nil
	}
	files, sizes, err := WriteIcons(dirs.Icons, stem, images)
	if err != nil {
		res.note("could not write icons: %v", err)
		return "", files, sizes
	}
	if len(files) > 0 {
		return stem, files, sizes
	}
	// Every size was one the icon theme has no directory for. A .desktop file
	// may name an icon by absolute path instead, so the icon still shows up.
	path, err := WriteIconFile(filepath.Join(dirs.Icons, "viniget"), stem, images[0])
	if err != nil {
		res.note("could not write icon: %v", err)
		return "", nil, nil
	}
	size := images[0].Bounds().Dx()
	res.note("icon is %dx%d, which the hicolor theme has no directory for; referenced by path instead",
		size, images[0].Bounds().Dy())
	return path, []string{path}, []int{size}
}

// uniqueTypes lists the MIME types an entry advertises, once each.
//
// Several declared extensions routinely resolve to one type -- .djv and .djvu
// are both image/vnd.djvu, .wim and .swm both application/x-ms-wim -- and a
// repeated value in MimeType= is a malformed entry.
func uniqueTypes(types []MIMEType) []string {
	seen := make(map[string]bool, len(types))
	out := make([]string, 0, len(types))
	for _, t := range types {
		if seen[t.Type] {
			continue
		}
		seen[t.Type] = true
		out = append(out, t.Type)
	}
	return out
}

// Remove deletes a package's desktop integration.
//
// The recorded file list is used when there is one, because it is exact. The
// glob fallback is for packages installed before viniget recorded what it
// wrote, and is deliberately two narrow patterns rather than one wide one:
// "viniget-Mozilla.Firefox*" would take Mozilla.Firefox.ESR with it.
func Remove(dirs Dirs, id string, files []string) ([]string, error) {
	if len(files) == 0 {
		files = integrationFiles(dirs, id)
	}
	var removed []string
	var firstErr error
	for _, f := range files {
		if _, err := os.Lstat(f); err != nil {
			continue
		}
		if err := removeIfPresent(f); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		removed = append(removed, f)
	}
	return removed, firstErr
}

func integrationFiles(dirs Dirs, id string) []string {
	var out []string
	add := func(patterns ...string) {
		for _, p := range patterns {
			matches, _ := filepath.Glob(p)
			out = append(out, matches...)
		}
	}
	stem := "viniget-" + id
	add(
		filepath.Join(dirs.Applications, stem+".desktop"),
		filepath.Join(dirs.Applications, stem+"-*.desktop"),
		filepath.Join(dirs.Icons, "hicolor", "*", "apps", stem+".png"),
		filepath.Join(dirs.Icons, "hicolor", "*", "apps", stem+"-*.png"),
		filepath.Join(dirs.Icons, "viniget", stem+".png"),
		filepath.Join(dirs.Icons, "viniget", stem+"-*.png"),
		filepath.Join(dirs.MIME, "packages", stem+".xml"),
		filepath.Join(dirs.Launchers, id),
		filepath.Join(dirs.Launchers, id+"-*"),
	)
	sort.Strings(out)
	return out
}

// Refresh rebuilds the desktop caches.
//
// Every one of these is optional and best-effort. A desktop environment that
// watches the directories does not need them; one that does not will pick the
// changes up on next login regardless. None of them is worth failing an
// install over, so a missing tool is silent and a failing one is a note.
func Refresh(dirs Dirs, mime bool, res *Result) {
	type job struct {
		bin  string
		args []string
	}
	jobs := []job{
		{"update-desktop-database", []string{dirs.Applications}},
		{"gtk-update-icon-cache", []string{"-q", "-t", "-f", filepath.Join(dirs.Icons, "hicolor")}},
	}
	if mime {
		jobs = append(jobs, job{"update-mime-database", []string{dirs.MIME}})
	}
	for _, j := range jobs {
		if _, err := exec.LookPath(j.bin); err != nil {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		out, err := exec.CommandContext(ctx, j.bin, j.args...).CombinedOutput()
		cancel()
		if err != nil && res != nil {
			res.note("%s: %v: %s", j.bin, err, strings.TrimSpace(string(out)))
		}
	}
}

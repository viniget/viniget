package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/viniget/viniget/internal/cli"
	"github.com/viniget/viniget/internal/desktop"
	"github.com/viniget/viniget/internal/install"
	"github.com/viniget/viniget/internal/manifest"
	"github.com/viniget/viniget/internal/wine"
)

// desktopPackage assembles what integration needs to know about a package.
//
// The prefix and the identifier are the only parts it cannot do without. The
// manifest supplies the description, the menu categories and the file types,
// all of which degrade to "absent" rather than to an error -- which is what
// makes regenerating an entry work offline.
func desktopPackage(rec install.Record, pkg *manifest.Package, overrides *string) desktop.Package {
	d := desktop.Package{
		ID:           rec.ID,
		Name:         rec.Name,
		Version:      rec.Version,
		Prefix:       rec.Prefix,
		Entry:        rec.Entry,
		DLLOverrides: resolveOverrides(overrides, rec.ID),
	}
	if pkg == nil {
		return d
	}
	if pkg.Locale != nil {
		if d.Name == "" {
			d.Name = pkg.Locale.PackageName
		}
		d.Comment = pkg.Locale.ShortDescription
		d.Tags = pkg.Locale.Tags
	}
	if pkg.Installer != nil {
		if choice, err := install.Select(pkg.Installer); err == nil {
			d.Extensions = choice.FileExtensions
		} else {
			d.Extensions = pkg.Installer.FileExtensions
		}
	}
	return d
}

// resolveOverrides decides what WINEDLLOVERRIDES a launcher carries: what was
// asked for when something was asked for -- including "none", which is why
// this takes a pointer -- and viniget's own entry for the package otherwise.
func resolveOverrides(asked *string, id string) string {
	if asked != nil {
		return *asked
	}
	return desktop.DLLOverridesFor(id)
}

func cmdDesktop(args []string) int {
	fs := flag.NewFlagSet("desktop", flag.ExitOnError)
	all := fs.Bool("all", false, "act on every installed package")
	remove := fs.Bool("remove", false, "remove the desktop integration instead of generating it")
	noAssoc := fs.Bool("no-associations", false, "do not offer the package as a handler for its file types")
	asJSON := fs.Bool("json", false, "emit JSON")
	dataDir := fs.String("data", "", "data directory for prefixes and installed-package state")
	cache := fs.String("cache", "", "index cache directory")
	noRefresh := fs.Bool("no-refresh", false, "never refresh the index")
	dllOverrides := fs.String("dll-overrides", "",
		"WINEDLLOVERRIDES for the generated launcher; pass it empty to drop the one viniget applies by default")
	rest := parseArgs(fs, args)

	// Distinguished from "left alone" so that clearing an override sticks
	// rather than being reapplied from the quirk table on the next run.
	var overrides *string
	if flagGiven(fs, "dll-overrides") {
		overrides = dllOverrides
	}

	prefixes := wine.NewStore(*dataDir)
	db, err := install.OpenDB(install.StatePath(prefixes.Dir))
	if err != nil {
		return errf("%v", err)
	}
	dirs := desktop.UserDirs(prefixes.Dir)

	var records []install.Record
	switch {
	case *all:
		records = db.All()
		if len(records) == 0 {
			fmt.Fprintln(os.Stderr, "no packages installed (try: viniget install <id>)")
			return 0
		}
	case len(rest) > 0:
		for _, id := range rest {
			rec, ok := db.Get(id)
			if !ok {
				return errf("%s is not installed (try: viniget list)", id)
			}
			records = append(records, rec)
		}
	default:
		fmt.Fprintln(os.Stderr, "usage: viniget desktop <package-id>... | -all")
		return 2
	}

	if *remove {
		return removeDesktop(db, dirs, records, *asJSON)
	}
	return generateDesktop(db, dirs, records, *cache, *noRefresh, *noAssoc, *asJSON, overrides)
}

func generateDesktop(db *install.DB, dirs desktop.Dirs, records []install.Record,
	cache string, noRefresh, noAssoc, asJSON bool, overrides *string) int {

	ctx := context.Background()
	results := map[string]*desktop.Result{}
	failed := false
	wroteMIME := false

	for _, rec := range records {
		if rec.Prefix == "" {
			fmt.Fprintf(os.Stderr, "viniget: %s has no prefix recorded; reinstall it\n", rec.ID)
			failed = true
			continue
		}
		// The manifest is metadata only and never blocks: a package whose
		// manifest cannot be fetched still gets an entry, just without the
		// description, categories and file types that only the manifest knows.
		pkg, err := resolve(ctx, cache, noRefresh, rec.ID, rec.Version)
		if err != nil {
			pkg = nil
		}

		// The flag decides when it was given, the record when it was not, and
		// viniget's own table when neither has an opinion.
		asked := overrides
		if asked == nil {
			asked = rec.DLLOverrides
		}

		// Refreshed once at the end rather than once per package: the
		// updaters walk whole directories, and on -all that is the slow part.
		res, err := desktop.Integrate(desktopPackage(rec, pkg, asked), desktop.Options{
			Dirs:           dirs,
			NoAssociations: noAssoc,
			NoRefresh:      true,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "viniget: %s: %v\n", rec.ID, err)
			failed = true
			continue
		}
		if pkg == nil && len(res.Apps) > 0 {
			res.Notes = append(res.Notes,
				"manifest unavailable; the entry has no description, categories or file types")
		}
		results[rec.ID] = res
		wroteMIME = wroteMIME || res.MIMEFile != ""

		rec.Desktop = res.Files
		rec.DLLOverrides = asked
		if entry := res.Entry(); entry != "" {
			rec.Entry = entry
		}
		db.Put(rec)

		if !asJSON {
			fmt.Printf("%s\n", rec.ID)
			cli.DesktopResult(os.Stdout, res)
		}
	}

	desktop.Refresh(dirs, wroteMIME, nil)
	if err := db.Save(); err != nil {
		fmt.Fprintf(os.Stderr, "viniget: warning: could not record the integration: %v\n", err)
	}

	if asJSON {
		if err := cli.JSON(os.Stdout, results); err != nil {
			return errf("%v", err)
		}
	}
	if failed {
		return 1
	}
	return 0
}

func removeDesktop(db *install.DB, dirs desktop.Dirs, records []install.Record, asJSON bool) int {
	removed := map[string][]string{}
	failed := false

	for _, rec := range records {
		files, err := desktop.Remove(dirs, rec.ID, rec.Desktop)
		if err != nil {
			fmt.Fprintf(os.Stderr, "viniget: %s: %v\n", rec.ID, err)
			failed = true
		}
		removed[rec.ID] = files
		rec.Desktop = nil
		db.Put(rec)
		if !asJSON {
			cli.DesktopRemoved(os.Stdout, rec.ID, files)
		}
	}

	desktop.Refresh(dirs, true, nil)
	if err := db.Save(); err != nil {
		fmt.Fprintf(os.Stderr, "viniget: warning: could not record the removal: %v\n", err)
	}

	if asJSON {
		if err := cli.JSON(os.Stdout, removed); err != nil {
			return errf("%v", err)
		}
	}
	if failed {
		return 1
	}
	return 0
}

// cmdRun launches an installed package.
//
// This is the terminal counterpart of the generated menu entry, and it runs
// the very same launcher script rather than reimplementing it -- so what a
// double-click does and what this does cannot drift apart. The script is
// derived state, regenerated from the prefix if it has gone missing.
const runUsage = `usage: viniget run [flags] <package-id> [argument ...]

Everything after the package identifier is passed to the application, flags
included, so viniget's own flags go before it.

`

func cmdRun(args []string) int {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	dataDir := fs.String("data", "", "data directory for prefixes and installed-package state")
	entry := fs.String("entry", "", "launch a secondary entry point of a suite, by its slug")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, runUsage)
		fs.PrintDefaults()
	}
	// Parsed rather than parseArgs: every other command accepts its flags
	// anywhere, but here everything after the package identifier belongs to
	// the application -- flags included, since Windows programs have their own
	// and viniget has no business intercepting them.
	fs.Parse(args)
	rest := fs.Args()

	if len(rest) == 0 {
		fs.Usage()
		return 2
	}
	id, appArgs := rest[0], rest[1:]

	prefixes := wine.NewStore(*dataDir)
	db, err := install.OpenDB(install.StatePath(prefixes.Dir))
	if err != nil {
		return errf("%v", err)
	}
	rec, ok := db.Get(id)
	if !ok {
		return errf("%s is not installed (try: viniget list)", id)
	}
	if err := wine.Available(); err != nil {
		return errf("%v", err)
	}

	dirs := desktop.UserDirs(prefixes.Dir)
	script := filepath.Join(dirs.Launchers, desktop.LauncherName(rec.ID, *entry))
	if _, err := os.Stat(script); err != nil {
		script, err = writeLauncher(rec, dirs, *entry)
		if err != nil {
			return errf("%v", err)
		}
	}

	argv := append([]string{script}, appArgs...)
	if err := syscall.Exec(script, argv, os.Environ()); err != nil {
		return errf("launch %s: %v", rec.ID, err)
	}
	return 0 // unreachable: Exec replaces the process
}

// writeLauncher regenerates a missing launcher script by rediscovering the
// package's entry point in its prefix.
func writeLauncher(rec install.Record, dirs desktop.Dirs, slug string) (string, error) {
	apps := desktop.Apps(desktop.Package{
		ID: rec.ID, Name: rec.Name, Prefix: rec.Prefix, Entry: rec.Entry,
	})
	var app *desktop.App
	for i := range apps {
		if apps[i].Slug == slug {
			app = &apps[i]
			break
		}
	}
	if app == nil {
		if slug != "" {
			return "", fmt.Errorf("%s has no entry point %q (try: viniget desktop %s)", rec.ID, slug, rec.ID)
		}
		return "", fmt.Errorf("nothing launchable found in %s", rec.Prefix)
	}
	l := &desktop.Launcher{
		ID:           rec.ID,
		Prefix:       rec.Prefix,
		Target:       app.Target,
		Args:         app.Args,
		WorkingDir:   app.WorkingDir,
		DLLOverrides: resolveOverrides(rec.DLLOverrides, rec.ID),
	}
	return l.Write(dirs.Launchers, desktop.LauncherName(rec.ID, slug))
}

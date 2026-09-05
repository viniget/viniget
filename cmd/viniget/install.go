package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/viniget/viniget/internal/cli"
	"github.com/viniget/viniget/internal/desktop"
	"github.com/viniget/viniget/internal/githubauth"
	"github.com/viniget/viniget/internal/helper"
	"github.com/viniget/viniget/internal/index"
	"github.com/viniget/viniget/internal/install"
	"github.com/viniget/viniget/internal/manifest"
	"github.com/viniget/viniget/internal/wine"
)

// lookupPackage resolves a package identifier or moniker against the index
// without fetching its manifest. Keeping the cheap lookup separate lets
// `update` report an already-current package without touching Wine or GitHub.
func lookupPackage(ctx context.Context, cache string, noRefresh bool, name string) (*index.Package, *index.Store, error) {
	cat, store, err := openCatalog(ctx, cache, noRefresh)
	if err != nil {
		return nil, nil, err
	}
	defer cat.Close()

	pkg, err := cat.Lookup(name)
	if err != nil {
		var ambiguous *index.AmbiguousError
		if errors.As(err, &ambiguous) {
			// Passed through unwrapped: the caller prints the candidates, and
			// a list of identifiers is the whole of the useful answer here.
			return nil, nil, err
		}
		return nil, nil, fmt.Errorf("lookup: %w", err)
	}
	if pkg == nil {
		return nil, nil, fmt.Errorf("no package with id or moniker %q (try: viniget search %s)", name, name)
	}
	return pkg, store, nil
}

// fetchPackage expands an index row into the complete manifest used by the
// installer. Version may be empty, in which case the index's latest is used.
func fetchPackage(ctx context.Context, store *index.Store, pkg *index.Package, version string) (*manifest.Package, error) {
	if version == "" {
		version = pkg.Version
	}

	f := &manifest.Fetcher{GitHubToken: githubauth.TokenFromEnv(), Fallback: v1Fallback(store)}
	full, err := f.Fetch(ctx, pkg.ID, version)
	if err != nil {
		return nil, fmt.Errorf("fetch manifest: %w", err)
	}
	if full.Locale == nil && pkg.Name != "" {
		full.Locale = &manifest.LocaleManifest{PackageName: pkg.Name}
	}
	return full, nil
}

// resolve turns a package identifier or moniker into a fully fetched
// manifest. Version may be empty, in which case the index's latest is used.
func resolve(ctx context.Context, cache string, noRefresh bool, name, version string) (*manifest.Package, error) {
	pkg, store, err := lookupPackage(ctx, cache, noRefresh, name)
	if err != nil {
		return nil, err
	}
	return fetchPackage(ctx, store, pkg, version)
}

func cmdInstall(args []string) int { return cmdInstallOrUpdate(args, false) }

// cmdUpdate keeps the historical no-argument index refresh and treats a
// package identifier as an atomic reinstall at the newest indexed version.
func cmdUpdate(args []string) int { return cmdInstallOrUpdate(args, true) }

func cmdInstallOrUpdate(args []string, updating bool) int {
	command := "install"
	if updating {
		command = "update"
	}
	fs := flag.NewFlagSet(command, flag.ExitOnError)
	var with stringList
	version := ""
	if !updating {
		fs.StringVar(&version, "version", "", "install a specific version instead of the latest")
	}
	timeout := fs.Duration("timeout", 0,
		"override how long the installer may run (default 4m unattended, 15m interactive)")
	force := false
	resetHelpers := false
	if !updating {
		fs.BoolVar(&force, "force", false, "reinstall a package that is already installed")
		fs.BoolVar(&resetHelpers, "reset-helpers", false, "with -force, do not preserve helpers recorded by the previous install")
	}
	keepFailed := fs.Bool("keep-failed", false, "keep the prefix from a failed install for inspection")
	fs.Var(&with, "with", "install a required helper verb before the package (repeatable)")
	noAutoHelpers := fs.Bool("no-auto-helpers", false, "do not infer helpers from manifest dependencies")
	helperTimeout := fs.Duration("helper-timeout", helper.DefaultTimeout, "maximum runtime for each helper verb")
	visible := fs.Bool("visible", false, "run the installer on your own display instead of a private Xvfb")
	verbose := fs.Bool("verbose", false, "show wine's own output")
	noDesktop := fs.Bool("no-desktop", false, "do not generate a menu entry, icon or file associations")
	noAssoc := fs.Bool("no-associations", false, "do not offer the package as a handler for its file types")
	asJSON := fs.Bool("json", false, "emit JSON")
	dataDir := fs.String("data", "", "data directory for prefixes and installed-package state")
	cache := fs.String("cache", "", "index cache directory")
	noRefresh := fs.Bool("no-refresh", false, "never refresh the index")
	dllOverrides := fs.String("dll-overrides", "",
		"WINEDLLOVERRIDES for the generated launcher; pass it empty to drop the one viniget applies by default")
	rest := parseArgs(fs, args)
	if updating && len(rest) == 0 {
		packageFlag := false
		fs.Visit(func(f *flag.Flag) {
			if f.Name != "cache" {
				packageFlag = true
			}
		})
		if packageFlag {
			fmt.Fprintln(os.Stderr, "usage: viniget update [-cache <dir>] [<package-id>]")
			return 2
		}
		return refreshIndex(*cache)
	}
	if resetHelpers && !force {
		fmt.Fprintln(os.Stderr, "viniget: -reset-helpers requires -force")
		return 2
	}
	for _, name := range with {
		if _, err := helper.Lookup(name); err != nil {
			return errf("%v", err)
		}
	}

	// Distinguished from "left alone" so that clearing an override sticks
	// rather than being reapplied from the quirk table on the next run.
	var overrides *string
	if flagGiven(fs, "dll-overrides") {
		overrides = dllOverrides
	}

	if len(rest) == 0 {
		fmt.Fprintln(os.Stderr, "usage: viniget install <package-id|moniker>")
		return 2
	}
	if updating && len(rest) != 1 {
		fmt.Fprintln(os.Stderr, "usage: viniget update [flags] <package-id>")
		return 2
	}
	name := rest[0]

	ctx := context.Background()
	prefixes := wine.NewStore(*dataDir)
	db, err := install.OpenDB(install.StatePath(prefixes.Dir))
	if err != nil {
		return errf("%v", err)
	}

	var (
		pkg                 *manifest.Package
		prev                install.Record
		previouslyInstalled bool
	)
	if updating {
		prev, previouslyInstalled = db.Get(name)
		if !previouslyInstalled {
			return errf("%s is not installed (try: viniget list)", name)
		}
		indexed, store, lookupErr := lookupPackage(ctx, *cache, *noRefresh, prev.ID)
		if lookupErr != nil {
			return errf("%v", lookupErr)
		}
		if prev.Version == indexed.Version {
			if *asJSON {
				out := updateCheck(prev, indexed.Version, false)
				if err := cli.JSON(os.Stdout, out); err != nil {
					return errf("%v", err)
				}
			} else {
				fmt.Printf("%s %s is already current\n", displayRecordName(prev), prev.Version)
			}
			return 0
		}
		if err := wine.Available(); err != nil {
			return errf("%v", err)
		}
		pkg, err = fetchPackage(ctx, store, indexed, "")
		if err != nil {
			return errf("%v", err)
		}
		if overrides == nil {
			overrides = prev.DLLOverrides
		}
		if !*asJSON {
			fmt.Fprintf(os.Stderr, "updating %s %s to %s ...\n", prev.ID, prev.Version, pkg.Version)
		}
	} else {
		if err := wine.Available(); err != nil {
			return errf("%v", err)
		}
		pkg, err = resolve(ctx, *cache, *noRefresh, name, version)
		if err != nil {
			var ambiguous *index.AmbiguousError
			if errors.As(err, &ambiguous) {
				return ambiguousf(ambiguous)
			}
			return errf("%v", err)
		}
		// A moniker is a nickname several packages may answer to, so the
		// identifier it landed on is worth stating before anything is downloaded.
		if !strings.EqualFold(name, pkg.ID) {
			fmt.Fprintf(os.Stderr, "%s is %s\n", name, pkg.ID)
		}

		prev, previouslyInstalled = db.Get(pkg.ID)
		if previouslyInstalled && !force {
			fmt.Fprintf(os.Stderr,
				"%s %s is already installed in %s\nreinstall it with: viniget install %s -force\n",
				prev.ID, prev.Version, prev.Prefix, prev.ID)
			return 1
		}
	}

	log := io.Writer(os.Stderr)
	if *asJSON {
		log = nil
	}
	opt := install.Options{
		Prefixes:        prefixes,
		Cache:           filepath.Join(index.NewStore(*cache).Dir, "downloads"),
		GitHubToken:     githubauth.TokenFromEnv(),
		Timeout:         *timeout,
		Verbose:         *verbose,
		KeepFailed:      *keepFailed,
		ExplicitHelpers: append([]string(nil), with...),
		AutoHelpers:     !*noAutoHelpers,
		HelperTimeout:   *helperTimeout,
		Log:             log,
	}
	if previouslyInstalled && !resetHelpers {
		opt.RecordedHelpers = append([]string(nil), prev.Helpers...)
	}
	if log != nil && isTerminal(os.Stderr) {
		opt.Progress = log
	}
	if *visible {
		opt.Display = wine.OnHost()
	} else {
		opt.Display = wine.Headless()
	}

	res, runErr := install.Run(ctx, pkg, opt)

	// Desktop integration is the last stage of an install, not a separate
	// step, because it is where the launch target comes from: until the Start
	// Menu shortcut has been read, viniget knows the prefix it wrote to and --
	// for archive packages only -- what the manifest declared.
	var integration *desktop.Result
	if res != nil && res.Status.Installed() {
		integration = integrate(res, pkg, prefixes.Dir, *noDesktop, *noAssoc, overrides)
		rec := install.RecordOf(res)
		rec.DLLOverrides = overrides
		if integration != nil {
			rec.Entry, rec.Desktop = res.Entry, integration.Files
		} else if updating {
			// -no-desktop skips regeneration; keep the paths already on disk
			// recorded so a later uninstall can still remove them exactly.
			rec.Desktop = append([]string(nil), prev.Desktop...)
		}
		db.Put(rec)
		if err := db.Save(); err != nil {
			// The application is installed either way; losing the record only
			// means viniget will not list it, so this is a warning.
			fmt.Fprintf(os.Stderr, "viniget: warning: could not record the install: %v\n", err)
		}
	}

	if *asJSON {
		if res == nil {
			return errf("%v", runErr)
		}
		out := struct {
			*install.Result
			PreviousVersion string          `json:"previous_version,omitempty"`
			Updated         *bool           `json:"updated,omitempty"`
			Desktop         *desktop.Result `json:"desktop,omitempty"`
		}{Result: res, Desktop: integration}
		if updating {
			updated := res.Status.Installed()
			out.PreviousVersion = prev.Version
			out.Updated = &updated
		}
		if err := cli.JSON(os.Stdout, out); err != nil {
			return errf("%v", err)
		}
		return exitFor(res, runErr)
	}

	if runErr != nil {
		var unsupported *install.UnsupportedError
		if errors.As(runErr, &unsupported) {
			return errf("%s: %v", pkg.ID, runErr)
		}
		return errf("%v", runErr)
	}
	var renderErr error
	if updating {
		renderErr = cli.UpdateResult(os.Stdout, res, prev.Version)
	} else {
		renderErr = cli.InstallResult(os.Stdout, res)
	}
	if renderErr != nil {
		return errf("%v", renderErr)
	}
	if integration != nil {
		if err := cli.DesktopResult(os.Stdout, integration); err != nil {
			return errf("%v", err)
		}
	}
	if res.Status.Installed() {
		if res.Entry != "" {
			fmt.Printf("\nrun it with:  viniget run %s\n", res.ID)
		} else {
			fmt.Printf("\nrun it with:  WINEPREFIX=%s wine <exe>\n", res.Prefix)
		}
	}
	return exitFor(res, runErr)
}

type updateState struct {
	ID              string `json:"id"`
	Name            string `json:"name,omitempty"`
	PreviousVersion string `json:"previous_version"`
	Version         string `json:"version"`
	Updated         bool   `json:"updated"`
}

func updateCheck(rec install.Record, version string, updated bool) updateState {
	return updateState{
		ID: rec.ID, Name: rec.Name, PreviousVersion: rec.Version,
		Version: version, Updated: updated,
	}
}

func displayRecordName(rec install.Record) string {
	if rec.Name != "" {
		return rec.Name
	}
	return rec.ID
}

// stringList implements a repeatable string flag without accepting a
// comma-separated mini-language that helper names do not need.
type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }
func (s *stringList) Set(value string) error {
	*s = append(*s, value)
	return nil
}

// isTerminal reports whether f is a tty, which decides whether a rewriting
// progress line makes sense.
func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// ambiguousf reports a moniker that several unrelated packages answer to.
// The identifiers are the answer: one of them is what the user has to type.
func ambiguousf(e *index.AmbiguousError) int {
	fmt.Fprintf(os.Stderr, "viniget: %v; install one by id:\n", e)
	cli.Packages(os.Stderr, e.Candidates)
	return 1
}

func exitFor(res *install.Result, err error) int {
	if err != nil || res == nil || !res.Status.Installed() {
		return 1
	}
	return 0
}

func cmdList(args []string) int {
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "emit JSON")
	dataDir := fs.String("data", "", "data directory for prefixes and installed-package state")
	parseArgs(fs, args)

	prefixes := wine.NewStore(*dataDir)
	db, err := install.OpenDB(install.StatePath(prefixes.Dir))
	if err != nil {
		return errf("%v", err)
	}
	records := db.All()

	if *asJSON {
		if err := cli.JSON(os.Stdout, records); err != nil {
			return errf("%v", err)
		}
		return 0
	}
	if len(records) == 0 {
		fmt.Fprintln(os.Stderr, "no packages installed (try: viniget install <id>)")
		return 0
	}
	if err := cli.Installed(os.Stdout, records); err != nil {
		return errf("%v", err)
	}
	return 0
}

// integrate runs desktop integration for a freshly installed package and
// updates the result's entry point with what it found.
//
// A failure here is reported and then dropped: the application is installed,
// and a menu entry that could not be written is a smaller problem than an
// install that reports itself as failed when the files are all on disk.
func integrate(res *install.Result, pkg *manifest.Package, dataDir string, skip, noAssoc bool,
	overrides *string) *desktop.Result {

	d := desktop.Package{
		ID:           res.ID,
		Name:         res.Name,
		Version:      res.Version,
		Prefix:       res.Prefix,
		Entry:        res.Entry,
		EntrySource:  recordedEntrySource(res.InstallerType),
		Extensions:   res.FileExtensions,
		DLLOverrides: resolveOverrides(overrides, res.ID),
	}
	if pkg != nil && pkg.Locale != nil {
		d.Comment = pkg.Locale.ShortDescription
		d.Tags = pkg.Locale.Tags
	}

	// Even with integration turned off the prefix is still read for its
	// shortcuts, because the entry point is worth having on its own.
	if skip {
		if apps := desktop.Apps(d); len(apps) > 0 {
			res.Entry = apps[0].Target
		}
		return nil
	}

	out, err := desktop.Integrate(d, desktop.Options{
		Dirs:           desktop.UserDirs(dataDir),
		NoAssociations: noAssoc,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "viniget: warning: desktop integration: %v\n", err)
		return out
	}
	if entry := out.Entry(); entry != "" {
		res.Entry = entry
	}
	return out
}

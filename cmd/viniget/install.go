package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/viniget/viniget/internal/cli"
	"github.com/viniget/viniget/internal/index"
	"github.com/viniget/viniget/internal/install"
	"github.com/viniget/viniget/internal/manifest"
	"github.com/viniget/viniget/internal/wine"
)

// resolve turns a package identifier into a fully fetched manifest. Version
// may be empty, in which case the index's latest is used.
func resolve(ctx context.Context, cache string, noRefresh bool, id, version string) (*manifest.Package, error) {
	cat, store, err := openCatalog(ctx, cache, noRefresh)
	if err != nil {
		return nil, err
	}
	defer cat.Close()

	pkg, err := cat.Get(id)
	if err != nil {
		return nil, fmt.Errorf("lookup: %w", err)
	}
	if pkg == nil {
		return nil, fmt.Errorf("no package with id %q (try: viniget search %s)", id, id)
	}
	if version == "" {
		version = pkg.Version
	}

	f := &manifest.Fetcher{Fallback: v1Fallback(store)}
	full, err := f.Fetch(ctx, pkg.ID, version)
	if err != nil {
		return nil, fmt.Errorf("fetch manifest: %w", err)
	}
	if full.Locale == nil && pkg.Name != "" {
		full.Locale = &manifest.LocaleManifest{PackageName: pkg.Name}
	}
	return full, nil
}

func cmdInstall(args []string) int {
	fs := flag.NewFlagSet("install", flag.ExitOnError)
	version := fs.String("version", "", "install a specific version instead of the latest")
	timeout := fs.Duration("timeout", install.DefaultTimeout, "how long the installer may run")
	force := fs.Bool("force", false, "reinstall a package that is already installed")
	keepFailed := fs.Bool("keep-failed", false, "keep the prefix from a failed install for inspection")
	visible := fs.Bool("visible", false, "run the installer on your own display instead of a private Xvfb")
	verbose := fs.Bool("verbose", false, "show wine's own output")
	asJSON := fs.Bool("json", false, "emit JSON")
	dataDir := fs.String("data", "", "data directory for prefixes and installed-package state")
	cache := fs.String("cache", "", "index cache directory")
	noRefresh := fs.Bool("no-refresh", false, "never refresh the index")
	rest := parseArgs(fs, args)

	if len(rest) == 0 {
		fmt.Fprintln(os.Stderr, "usage: viniget install <package-id>")
		return 2
	}
	id := rest[0]

	if err := wine.Available(); err != nil {
		return errf("%v", err)
	}

	ctx := context.Background()
	prefixes := wine.NewStore(*dataDir)
	db, err := install.OpenDB(install.StatePath(prefixes.Dir))
	if err != nil {
		return errf("%v", err)
	}

	pkg, err := resolve(ctx, *cache, *noRefresh, id, *version)
	if err != nil {
		return errf("%v", err)
	}

	if prev, ok := db.Get(pkg.ID); ok && !*force {
		fmt.Fprintf(os.Stderr,
			"%s %s is already installed in %s\nreinstall it with: viniget install %s -force\n",
			prev.ID, prev.Version, prev.Prefix, prev.ID)
		return 1
	}

	log := io.Writer(os.Stderr)
	if *asJSON {
		log = nil
	}
	opt := install.Options{
		Prefixes:   prefixes,
		Cache:      filepath.Join(index.NewStore(*cache).Dir, "downloads"),
		Timeout:    *timeout,
		Verbose:    *verbose,
		KeepFailed: *keepFailed,
		Log:        log,
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

	if res != nil && res.Status.Installed() {
		db.Put(install.RecordOf(res))
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
		if err := cli.JSON(os.Stdout, res); err != nil {
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
	if err := cli.InstallResult(os.Stdout, res); err != nil {
		return errf("%v", err)
	}
	if res.Status.Installed() {
		fmt.Printf("\nrun it with:  WINEPREFIX=%s wine <exe>\n", res.Prefix)
		if res.Entry != "" {
			fmt.Printf("              WINEPREFIX=%s wine %q\n", res.Prefix, res.Entry)
		}
	}
	return exitFor(res, runErr)
}

// isTerminal reports whether f is a tty, which decides whether a rewriting
// progress line makes sense.
func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
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

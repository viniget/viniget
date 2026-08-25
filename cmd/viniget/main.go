// Command viniget installs winget packages on Linux.
//
// Search, show and update are pure metadata and need no Wine at all. Install
// is where Wine enters: it picks the variant Wine can drive, downloads it,
// clones the baseline prefix, runs the installer unattended, and scores the
// result on the filesystem delta rather than the exit code.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/viniget/viniget/internal/cli"
	"github.com/viniget/viniget/internal/index"
	"github.com/viniget/viniget/internal/manifest"
)

const usage = `viniget - winget packages for Linux

Usage:
  viniget search <term>   Search the package index
  viniget show <id>       Show a package's manifest
  viniget install <id>    Install a package into its own wine prefix
  viniget run <id>        Launch an installed package
  viniget list            List installed packages
  viniget desktop <id>    Regenerate menu entries, icons and associations
  viniget update          Refresh the package index

Run "viniget <command> -h" for command flags.
`

func main() { os.Exit(run()) }

func run() int {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		return 2
	}
	args := os.Args[2:]
	switch os.Args[1] {
	case "search":
		return cmdSearch(args)
	case "show":
		return cmdShow(args)
	case "install":
		return cmdInstall(args)
	case "run":
		return cmdRun(args)
	case "desktop":
		return cmdDesktop(args)
	case "list", "ls":
		return cmdList(args)
	case "update":
		return cmdUpdate(args)
	case "help", "-h", "--help":
		fmt.Print(usage)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "viniget: unknown command %q\n\n%s", os.Args[1], usage)
		return 2
	}
}

// parseArgs parses flags that appear before, between or after positional
// arguments, and returns the positionals. flag.FlagSet stops at the first
// non-flag argument on its own, which would silently ignore a trailing
// "--limit 50" -- the way people actually type these commands.
func parseArgs(fs *flag.FlagSet, args []string) []string {
	var positional []string
	for {
		fs.Parse(args)
		if fs.NArg() == 0 {
			return positional
		}
		positional = append(positional, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

// flagGiven reports whether a flag was actually typed, as opposed to left at
// its zero value. The difference matters wherever the zero value is itself a
// choice somebody might mean.
func flagGiven(fs *flag.FlagSet, name string) bool {
	given := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			given = true
		}
	})
	return given
}

func errf(format string, a ...any) int {
	fmt.Fprintf(os.Stderr, "viniget: "+format+"\n", a...)
	return 1
}

// openCatalog makes an index available and opens it.
func openCatalog(ctx context.Context, cache string, noRefresh bool) (*index.Catalog, *index.Store, error) {
	store := index.NewStore(cache)
	if _, err := os.Stat(store.DBPath()); err != nil {
		fmt.Fprintln(os.Stderr, "fetching winget package index ...")
	}
	if _, _, err := store.Ensure(ctx, index.DefaultTTL, noRefresh); err != nil {
		return nil, nil, err
	}
	cat, err := index.Open(store.DBPath())
	if err != nil {
		return nil, nil, err
	}
	return cat, store, nil
}

func cmdSearch(args []string) int {
	fs := flag.NewFlagSet("search", flag.ExitOnError)
	limit := fs.Int("limit", 25, "maximum results")
	asJSON := fs.Bool("json", false, "emit JSON")
	noRefresh := fs.Bool("no-refresh", false, "never refresh the index")
	cache := fs.String("cache", "", "index cache directory")
	rest := parseArgs(fs, args)

	if len(rest) == 0 {
		fmt.Fprintln(os.Stderr, "usage: viniget search <term>")
		return 2
	}
	term := strings.Join(rest, " ")

	ctx := context.Background()
	cat, _, err := openCatalog(ctx, *cache, *noRefresh)
	if err != nil {
		return errf("%v", err)
	}
	defer cat.Close()

	results, err := cat.Search(term, *limit)
	if err != nil {
		return errf("search: %v", err)
	}
	if *asJSON {
		if err := cli.JSON(os.Stdout, results); err != nil {
			return errf("%v", err)
		}
		return 0
	}
	if len(results) == 0 {
		fmt.Fprintf(os.Stderr, "no packages match %q\n", term)
		return 1
	}
	if err := cli.SearchResults(os.Stdout, results); err != nil {
		return errf("%v", err)
	}
	return 0
}

func cmdShow(args []string) int {
	fs := flag.NewFlagSet("show", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "emit JSON")
	noRefresh := fs.Bool("no-refresh", false, "never refresh the index")
	cache := fs.String("cache", "", "index cache directory")
	rest := parseArgs(fs, args)

	if len(rest) == 0 {
		fmt.Fprintln(os.Stderr, "usage: viniget show <package-id>")
		return 2
	}
	id := rest[0]

	ctx := context.Background()
	cat, store, err := openCatalog(ctx, *cache, *noRefresh)
	if err != nil {
		return errf("%v", err)
	}
	defer cat.Close()

	pkg, err := cat.Get(id)
	if err != nil {
		return errf("lookup: %v", err)
	}
	if pkg == nil {
		return errf("no package with id %q (try: viniget search %s)", id, id)
	}

	f := &manifest.Fetcher{Fallback: v1Fallback(store)}
	full, err := f.Fetch(ctx, pkg.ID, pkg.Version)
	if err != nil {
		return errf("fetch manifest: %v", err)
	}
	if *asJSON {
		if err := cli.JSON(os.Stdout, full); err != nil {
			return errf("%v", err)
		}
		return 0
	}
	if err := cli.Package(os.Stdout, full); err != nil {
		return errf("%v", err)
	}
	return 0
}

func cmdUpdate(args []string) int {
	fs := flag.NewFlagSet("update", flag.ExitOnError)
	cache := fs.String("cache", "", "index cache directory")
	parseArgs(fs, args)

	ctx := context.Background()
	store := index.NewStore(*cache)
	first := false
	if _, err := os.Stat(store.DBPath()); err != nil {
		first = true
		fmt.Fprintln(os.Stderr, "fetching winget package index ...")
	}

	meta, changed, err := store.Refresh(ctx)
	if err != nil {
		return errf("%v", err)
	}

	cat, err := index.Open(store.DBPath())
	if err != nil {
		return errf("%v", err)
	}
	defer cat.Close()
	packages, families, err := cat.Families()
	if err != nil {
		return errf("%v", err)
	}

	switch {
	case first:
		fmt.Printf("fetched index %s\n", meta.SourceVersion)
	case changed:
		fmt.Printf("updated index to %s\n", meta.SourceVersion)
	default:
		fmt.Printf("index %s is already current\n", meta.SourceVersion)
	}
	fmt.Printf("%d packages, %d families\n", packages, families)
	return 0
}

// v1Fallback resolves a manifest directory through the v1 pathparts tree.
//
// It is only reached when a derived manifest URL 404s, which the derivation
// rule says should never happen. Downloading the v1 index is deferred until
// then because it costs 19.5 MB against the v2 index's 3.5 MB.
func v1Fallback(store *index.Store) func(context.Context, string, string) (string, error) {
	return func(ctx context.Context, id, version string) (string, error) {
		path, err := store.EnsureV1(ctx)
		if err != nil {
			return "", err
		}
		pi, err := index.OpenPathIndex(path)
		if err != nil {
			return "", err
		}
		defer pi.Close()
		return pi.ManifestDir(id)
	}
}

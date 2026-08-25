package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/viniget/viniget/internal/cli"
	"github.com/viniget/viniget/internal/desktop"
	"github.com/viniget/viniget/internal/install"
	"github.com/viniget/viniget/internal/wine"
)

const uninstallUsage = `usage: viniget uninstall [flags] <package-id>...
       viniget uninstall -all

Deletes the package's wine prefix, its menu entry, icons, launcher and file
type declarations, and its installed-package record. Nothing survives it, so
there is no vendor uninstaller to run: everything the application wrote is
inside the prefix.

The downloaded installer payload is left in the cache, where it is keyed by
its digest and may be shared with another package.

`

func cmdUninstall(args []string) int {
	fs := flag.NewFlagSet("uninstall", flag.ExitOnError)
	all := fs.Bool("all", false, "uninstall every installed package")
	yes := fs.Bool("yes", false, "do not ask for confirmation")
	asJSON := fs.Bool("json", false, "emit JSON")
	dataDir := fs.String("data", "", "data directory for prefixes and installed-package state")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, uninstallUsage)
		fs.PrintDefaults()
	}
	rest := parseArgs(fs, args)

	prefixes := wine.NewStore(*dataDir)
	db, err := install.OpenDB(install.StatePath(prefixes.Dir))
	if err != nil {
		return errf("%v", err)
	}
	dirs := desktop.UserDirs(prefixes.Dir)

	var ids []string
	switch {
	case *all:
		for _, rec := range db.All() {
			ids = append(ids, rec.ID)
		}
		if len(ids) == 0 {
			fmt.Fprintln(os.Stderr, "no packages installed")
			return 0
		}
		if !*yes {
			if ok, err := confirmUninstall(ids); err != nil {
				return errf("%v", err)
			} else if !ok {
				fmt.Fprintln(os.Stderr, "nothing was removed")
				return 1
			}
		}
	case len(rest) > 0:
		// An identifier with no record but a prefix on disk is accepted, and
		// is the whole reason this looks at the filesystem at all: an install
		// whose record could not be written leaves a prefix that `viniget
		// list` cannot show and nothing else knows how to remove.
		for _, id := range dedupe(rest) {
			_, recorded := db.Get(id)
			if !recorded && len(prefixes.Paths(id)) == 0 {
				return errf("%s is not installed (try: viniget list)", id)
			}
			ids = append(ids, id)
		}
	default:
		fs.Usage()
		return 2
	}

	var removed []*install.Removed
	failed := false
	for i, id := range ids {
		out, err := install.Uninstall(db, prefixes, dirs, id)
		if err != nil {
			fmt.Fprintf(os.Stderr, "viniget: %s: %v\n", id, err)
			failed = true
		}
		removed = append(removed, out)
		if !*asJSON {
			if i > 0 {
				fmt.Println()
			}
			if err := cli.Uninstalled(os.Stdout, out, dirs); err != nil {
				return errf("%v", err)
			}
		}
	}

	// Once at the end rather than once per package: the updaters walk whole
	// directories, and on -all that is the slow part.
	desktop.Refresh(dirs, true, nil)
	if err := db.Save(); err != nil {
		// The files are gone either way. A record that outlived them makes
		// `viniget list` wrong, which is worth saying loudly and is not worth
		// pretending the removal failed over.
		fmt.Fprintf(os.Stderr, "viniget: warning: could not record the removal: %v\n", err)
		failed = true
	}

	if *asJSON {
		if err := cli.JSON(os.Stdout, removed); err != nil {
			return errf("%v", err)
		}
	} else {
		baseline := ""
		if len(db.All()) == 0 && prefixes.HasBase() {
			baseline = prefixes.Base()
		}
		cli.UninstallTotal(os.Stdout, removed, baseline)
	}

	if failed {
		return 1
	}
	return 0
}

// confirmUninstall asks before a bulk removal.
//
// Only -all asks. Naming a package is already an unambiguous instruction, and
// a prompt on every uninstall is a prompt people learn to answer without
// reading. Off a terminal there is nobody to ask, so -all refuses rather than
// guessing that a script meant it.
func confirmUninstall(ids []string) (bool, error) {
	what := fmt.Sprintf("%d packages and their prefixes", len(ids))
	if len(ids) == 1 {
		what = "1 package and its prefix"
	}
	if !isTerminal(os.Stdin) {
		return false, fmt.Errorf("uninstall -all would delete %s; pass -yes to confirm", what)
	}
	fmt.Fprintf(os.Stderr, "uninstall %s?\n", what)
	for _, id := range ids {
		fmt.Fprintf(os.Stderr, "  %s\n", id)
	}
	fmt.Fprint(os.Stderr, "continue? [y/N] ")

	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return false, nil
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true, nil
	}
	return false, nil
}

// dedupe keeps the first sighting of each identifier. A repeated argument
// would otherwise come back on the second pass as an orphan with nothing left
// to remove, which reads like a bug.
func dedupe(ids []string) []string {
	seen := make(map[string]bool, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

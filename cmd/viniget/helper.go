package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"time"

	"github.com/viniget/viniget/internal/cli"
	"github.com/viniget/viniget/internal/helper"
	"github.com/viniget/viniget/internal/install"
	"github.com/viniget/viniget/internal/wine"
)

const helperUsage = `usage: viniget helper list [-json]
       viniget helper [flags] <package-id> <verb>...

Adds one or more curated Winetricks runtime helpers to an installed package's
Wine prefix. The live prefix is replaced only when every requested verb works.

`

func cmdHelper(args []string) int {
	if len(args) > 0 && args[0] == "list" {
		return cmdHelperList(args[1:])
	}
	fs := flag.NewFlagSet("helper", flag.ExitOnError)
	force := fs.Bool("force", false, "reapply helpers already recorded for the package")
	timeout := fs.Duration("timeout", helper.DefaultTimeout, "maximum runtime for each helper verb")
	visible := fs.Bool("visible", false, "run Winetricks on your own display instead of a private Xvfb")
	verbose := fs.Bool("verbose", false, "show Winetricks and Wine output")
	keepFailed := fs.Bool("keep-failed", false, "keep the failed staging prefix for inspection")
	asJSON := fs.Bool("json", false, "emit JSON")
	dataDir := fs.String("data", "", "data directory for prefixes and installed-package state")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, helperUsage)
		fs.PrintDefaults()
	}
	rest := parseArgs(fs, args)
	if len(rest) < 2 {
		fs.Usage()
		return 2
	}
	id, names := rest[0], rest[1:]

	prefixes := wine.NewStore(*dataDir)
	db, err := install.OpenDB(install.StatePath(prefixes.Dir))
	if err != nil {
		return errf("%v", err)
	}
	rec, ok := db.Get(id)
	if !ok {
		return errf("%s is not installed (try: viniget list)", id)
	}
	if info, err := os.Stat(rec.Prefix); err != nil || !info.IsDir() {
		return errf("installed prefix %s is missing", rec.Prefix)
	}

	seen := map[string]bool{}
	var requests []helper.Request
	out := &helper.RunResult{ID: id, Prefix: rec.Prefix}
	for _, name := range names {
		def, err := helper.Lookup(name)
		if err != nil {
			return errf("%v", err)
		}
		if seen[def.Verb] {
			continue
		}
		seen[def.Verb] = true
		req := helper.Request{Verb: def.Verb, Source: helper.SourceExplicit, Architecture: rec.Architecture}
		if slices.Contains(rec.Helpers, def.Verb) && !*force {
			out.Helpers = append(out.Helpers, helper.ResultFor(req, helper.StatusAlreadyPresent))
			continue
		}
		requests = append(requests, req)
	}

	if len(requests) > 0 {
		if err := wine.Available(); err != nil {
			return errf("%v", err)
		}
		ctx := context.Background()
		executor, err := helper.NewExecutor(ctx, "")
		if err != nil {
			return errf("%v", err)
		}
		var log io.Writer = os.Stderr
		if *asJSON {
			log = nil
		}
		display := wine.Headless()
		if *visible {
			display = wine.OnHost()
		}
		applied, runErr := helper.ApplyExisting(ctx, executor, prefixes, id, requests, helper.TransactionOptions{
			Options: helper.Options{
				Display: display, Timeout: *timeout, Force: *force,
				Verbose: *verbose, Output: helperCommandOutput(*verbose, log),
			},
			KeepFailed: *keepFailed,
			Log:        log,
		})
		if applied != nil {
			out.Helpers = append(out.Helpers, applied.Helpers...)
			out.Notes = append(out.Notes, applied.Notes...)
		}
		if runErr == nil {
			for _, verb := range helper.SuccessfulVerbs(out.Helpers) {
				if !slices.Contains(rec.Helpers, verb) {
					rec.Helpers = append(rec.Helpers, verb)
				}
			}
			db.Put(rec)
			if err := db.Save(); err != nil {
				fmt.Fprintf(os.Stderr, "viniget: warning: could not record helpers: %v\n", err)
			}
		}
		if *asJSON {
			if err := cli.JSON(os.Stdout, out); err != nil {
				return errf("%v", err)
			}
		} else {
			_ = cli.HelperResult(os.Stdout, out)
		}
		if runErr != nil {
			if !*asJSON {
				return errf("%v", runErr)
			}
			return 1
		}
		return 0
	}

	if *asJSON {
		if err := cli.JSON(os.Stdout, out); err != nil {
			return errf("%v", err)
		}
	} else {
		_ = cli.HelperResult(os.Stdout, out)
	}
	return 0
}

func cmdHelperList(args []string) int {
	fs := flag.NewFlagSet("helper list", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "emit JSON")
	if rest := parseArgs(fs, args); len(rest) != 0 {
		fmt.Fprintln(os.Stderr, "usage: viniget helper list [-json]")
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	executor, err := helper.NewExecutor(ctx, "")
	if err != nil {
		fmt.Fprintf(os.Stderr, "viniget: warning: %v\n", err)
	}
	defs := executor.List()
	if *asJSON {
		if err := cli.JSON(os.Stdout, defs); err != nil {
			return errf("%v", err)
		}
		return 0
	}
	if err := cli.HelperDefinitions(os.Stdout, defs); err != nil {
		return errf("%v", err)
	}
	return 0
}

func helperCommandOutput(verbose bool, w io.Writer) io.Writer {
	if verbose {
		return w
	}
	return nil
}

package helper

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/viniget/viniget/internal/wine"
)

// RunResult is the result of applying a group of helpers to one installed
// package prefix.
type RunResult struct {
	ID      string   `json:"id"`
	Prefix  string   `json:"prefix"`
	Helpers []Result `json:"helpers"`
	Notes   []string `json:"notes,omitempty"`
}

type TransactionOptions struct {
	Options
	KeepFailed bool
	Log        io.Writer
}

// ApplyExisting adds a required helper group to an installed prefix. The live
// prefix is stopped and cloned; it is replaced only after every verb succeeds.
func ApplyExisting(ctx context.Context, executor *Executor, prefixes *wine.Store, id string, requests []Request, opt TransactionOptions) (*RunResult, error) {
	final, err := prefixes.For(id)
	if err != nil {
		return nil, err
	}
	if info, err := os.Stat(final); err != nil || !info.IsDir() {
		return nil, fmt.Errorf("installed prefix %s is missing", final)
	}
	out := &RunResult{ID: id, Prefix: final}
	if len(requests) == 0 {
		return out, nil
	}

	// A reflink of a prefix with running processes would be an inconsistent
	// checkpoint. Naming a package here is an explicit request to modify it, so
	// stopping its Wine session is part of the transaction.
	live := &wine.Runner{Prefix: final}
	live.Kill(ctx)

	staging := final + ".installing"
	if err := prefixes.CloneFrom(final, staging); err != nil {
		return out, err
	}
	cleanup := func() {
		if !opt.KeepFailed {
			_ = os.RemoveAll(staging)
		} else {
			out.Notes = append(out.Notes, "failed prefix kept at "+staging)
		}
	}

	for _, req := range requests {
		if opt.Log != nil {
			fmt.Fprintf(opt.Log, "installing helper %s ...\n", req.Verb)
		}
		runOpt := opt.Options
		runOpt.Prefix = staging
		res, err := executor.Apply(ctx, req, runOpt)
		out.Helpers = append(out.Helpers, res)
		if err != nil {
			cleanup()
			return out, err
		}
	}
	if err := wine.Swap(staging, final); err != nil {
		cleanup()
		return out, err
	}
	return out, nil
}

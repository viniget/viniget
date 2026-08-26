package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/viniget/viniget/internal/helper"
)

func HelperDefinitions(w io.Writer, defs []helper.ListedDefinition) error {
	t := newTable(w)
	fmt.Fprintln(t, "VERB\tDESCRIPTION\tAVAILABLE\t")
	for _, d := range defs {
		available := "yes"
		if !d.Available {
			available = "no"
		}
		fmt.Fprintf(t, "%s\t%s\t%s\t\n", d.Verb, d.Title, available)
	}
	return t.Flush()
}

func HelperResult(w io.Writer, r *helper.RunResult) error {
	for _, h := range r.Helpers {
		switch h.Status {
		case helper.StatusInstalled:
			fmt.Fprintf(w, "installed helper %s for %s\n", h.Verb, r.ID)
		case helper.StatusAlreadyPresent:
			fmt.Fprintf(w, "helper %s is already recorded for %s\n", h.Verb, r.ID)
		default:
			fmt.Fprintf(w, "helper %s for %s: %s\n", h.Verb, r.ID, h.Status)
		}
		t := newTable(w)
		row(t, "Source", string(h.Source))
		row(t, "Prefix", r.Prefix)
		row(t, "Dependency", h.Dependency)
		row(t, "Minimum version", h.MinimumVersion)
		row(t, "Detected version", h.DetectedVersion)
		if h.Seconds > 0 || h.ExitCode != "" {
			row(t, "Took", fmt.Sprintf("%ss (exit %s)", trimFloat(h.Seconds), dash(h.ExitCode)))
		}
		if err := t.Flush(); err != nil {
			return err
		}
		for _, note := range h.Notes {
			fmt.Fprintf(w, "  · %s\n", note)
		}
	}
	if len(r.Notes) > 0 {
		fmt.Fprintln(w)
		for _, note := range r.Notes {
			fmt.Fprintf(w, "  · %s\n", note)
		}
	}
	return nil
}

func helperNames(results []helper.Result) string {
	var names []string
	for _, h := range results {
		if h.Status == helper.StatusInstalled || h.Status == helper.StatusAlreadyPresent {
			names = append(names, h.Verb)
		}
	}
	return strings.Join(names, ", ")
}

package cli

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/viniget/viniget/internal/install"
)

// InstallResult renders the outcome of one install attempt.
//
// The counts are printed even on success because they are the evidence the
// verdict rests on: viniget scores an install on the filesystem delta, so
// "ok" with 0 new files would be a bug worth seeing.
func InstallResult(w io.Writer, r *install.Result) error {
	name := r.Name
	if name == "" {
		name = r.ID
	}

	switch r.Status {
	case install.StatusOK:
		fmt.Fprintf(w, "installed %s %s\n", name, r.Version)
	case install.StatusTimeoutInstalled:
		fmt.Fprintf(w, "installed %s %s (installer ran past its timeout; the files are all there)\n",
			name, r.Version)
	default:
		fmt.Fprintf(w, "%s %s: %s\n", name, r.Version, r.Status)
	}

	t := newTable(w)
	row(t, "Type", installerLabel(r))
	if r.Architecture != "" {
		row(t, "Architecture", r.Architecture)
	}
	row(t, "Scope", r.Scope)
	row(t, "Prefix", r.Prefix)
	row(t, "Entry point", r.Entry)
	row(t, "Wrote", fmt.Sprintf("%d files, %d executables", r.NewFiles, r.NewExecutables))
	row(t, "Took", fmt.Sprintf("%ss (exit %s)", trimFloat(r.Seconds), dash(r.ExitCode)))
	row(t, "Product code", r.ProductCode)
	row(t, "Wine", r.WineVersion)
	row(t, "Helpers", helperNames(r.Helpers))
	if err := t.Flush(); err != nil {
		return err
	}

	if len(r.Notes) > 0 {
		fmt.Fprintln(w)
		for _, n := range r.Notes {
			fmt.Fprintf(w, "  · %s\n", n)
		}
	}
	return nil
}

// installerLabel shows the container when there is one, because "portable"
// and "a zip containing a portable" fail in different ways.
func installerLabel(r *install.Result) string {
	label := r.InstallerType
	if r.Container != "" && r.Container != r.InstallerType {
		label = fmt.Sprintf("%s in %s", r.InstallerType, r.Container)
	}
	if r.Interactive {
		label += " (interactive)"
	}
	return label
}

// Installed renders the installed-package list.
func Installed(w io.Writer, records []install.Record) error {
	if len(records) == 0 {
		return nil
	}
	t := newTable(w)
	fmt.Fprintln(t, "ID\tNAME\tVERSION\tTYPE\tHELPERS\tINSTALLED\t")
	for _, r := range records {
		fmt.Fprintf(t, "%s\t%s\t%s\t%s\t%s\t%s\t\n",
			r.ID, truncate(r.Name, 40), r.Version, r.InstallerType,
			strings.Join(r.Helpers, ","), since(r.InstalledAt))
	}
	return t.Flush()
}

// since renders an install time the way a person reads it: recent installs
// relatively, older ones by date.
func since(at time.Time) string {
	if at.IsZero() {
		return "-"
	}
	d := time.Since(at)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
	return at.Format("2006-01-02")
}

func trimFloat(f float64) string {
	return strings.TrimSuffix(fmt.Sprintf("%.1f", f), ".0")
}

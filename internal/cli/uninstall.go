package cli

import (
	"fmt"
	"io"

	"github.com/viniget/viniget/internal/desktop"
	"github.com/viniget/viniget/internal/install"
)

// Uninstalled renders what removing one package took off disk.
//
// The prefix path is printed for the same reason the install prints it: it is
// the answer to "what exactly did that delete", and after the fact it is the
// only evidence left. The desktop files are summarised by kind rather than
// listed, because eight packages' worth of icon paths is not a report anybody
// reads -- `viniget desktop -remove` is still there when the list is wanted.
func Uninstalled(w io.Writer, r *install.Removed, dirs desktop.Dirs) error {
	name := r.Name
	if name == "" {
		name = r.ID
	}
	switch {
	case r.Orphan:
		fmt.Fprintf(w, "cleaned up %s (nothing recorded it; removed what was on disk)\n", r.ID)
	case r.Version != "":
		fmt.Fprintf(w, "uninstalled %s %s\n", name, r.Version)
	default:
		fmt.Fprintf(w, "uninstalled %s\n", name)
	}

	t := newTable(w)
	for i, path := range r.Prefixes.Paths {
		label := "Prefix"
		if i > 0 {
			label = "" // a staging or backup sibling, under the same heading
		}
		fmt.Fprintf(t, "  %s\t%s\n", label, tildify(path))
	}
	if r.Prefixes.Bytes > 0 {
		row(t, "Size", humanBytes(r.Prefixes.Bytes)+" apparent")
	}
	row(t, "Desktop", desktop.Summarize(dirs, r.Desktop))
	if err := t.Flush(); err != nil {
		return err
	}

	for _, n := range r.Notes {
		if _, err := fmt.Fprintf(w, "  · %s\n", n); err != nil {
			return err
		}
	}
	return nil
}

// UninstallTotal closes a run, and is where the size caveat is paid off.
//
// Every size above is apparent, and one line at the end is the honest version:
// prefixes are reflink clones of a baseline that is still there, so "freed 4.8
// GB" would be the reading for eight packages that between them held about a
// gigabyte.
//
// baseline names the surviving baseline prefix when nothing is installed any
// more, and is empty otherwise. It is worth a line only at that moment -- the
// one time the directory viniget will not delete is the only thing left.
func UninstallTotal(w io.Writer, removed []*install.Removed, baseline string) {
	var bytes int64
	for _, r := range removed {
		bytes += r.Prefixes.Bytes
	}
	if len(removed) > 1 {
		fmt.Fprintf(w, "\nuninstalled %d packages, %s apparent\n", len(removed), humanBytes(bytes))
	}

	var notes []string
	if bytes > 0 {
		notes = append(notes,
			"apparent size; prefixes are reflink clones, so the space reclaimed is at most that")
	}
	if baseline != "" {
		notes = append(notes,
			fmt.Sprintf("nothing is installed now; the baseline prefix stays at %s for the next install",
				tildify(baseline)))
	}
	if len(notes) == 0 {
		return
	}
	fmt.Fprintln(w)
	for _, n := range notes {
		fmt.Fprintf(w, "  \u00b7 %s\n", n)
	}
}

// humanBytes renders a byte count the way the download progress line does.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGT"[exp])
}

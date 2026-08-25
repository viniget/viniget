package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/viniget/viniget/internal/desktop"
)

// DesktopResult renders what desktop integration produced.
//
// The paths are printed because they are the answer to the two questions a
// generated menu entry provokes -- where did that come from, and how do I get
// rid of it -- and because an entry that a desktop environment has not indexed
// yet is otherwise invisible until the next login.
func DesktopResult(w io.Writer, r *desktop.Result) error {
	if len(r.Apps) == 0 {
		return desktopNotes(w, r)
	}

	if len(r.Apps) == 1 {
		app := r.Apps[0]
		fmt.Fprintf(w, "\ndesktop entry\n")
		t := newTable(w)
		row(t, "Name", app.Name)
		row(t, "Menu file", tildify(app.DesktopFile))
		row(t, "Icon", iconSummary(app))
		row(t, "Launcher", tildify(app.Launcher))
		row(t, "Found via", sourceLabel(app.Source))
		row(t, "File types", mimeSummary(r.MIMETypes))
		if err := t.Flush(); err != nil {
			return err
		}
		return desktopNotes(w, r)
	}

	fmt.Fprintf(w, "\ndesktop entries (%d)\n", len(r.Apps))
	t := newTable(w)
	for _, app := range r.Apps {
		fmt.Fprintf(t, "  %s\t%s\t%s\n",
			truncate(app.Name, 32), tildify(app.DesktopFile), iconSummary(app))
	}
	if err := t.Flush(); err != nil {
		return err
	}
	if s := mimeSummary(r.MIMETypes); s != "" {
		t := newTable(w)
		row(t, "File types", s)
		if err := t.Flush(); err != nil {
			return err
		}
	}
	return desktopNotes(w, r)
}

func desktopNotes(w io.Writer, r *desktop.Result) error {
	for _, n := range r.Notes {
		if _, err := fmt.Fprintf(w, "  · %s\n", n); err != nil {
			return err
		}
	}
	return nil
}

func iconSummary(app desktop.Written) string {
	switch n := len(app.IconSizes); {
	case n == 0:
		return "none"
	case n == 1:
		return fmt.Sprintf("%dpx", app.IconSizes[0])
	default:
		return fmt.Sprintf("%d sizes, %d-%dpx", n, app.IconSizes[0], app.IconSizes[n-1])
	}
}

// mimeSummary names the file types the entry now offers to open, and says in
// the same breath that offering is all it did. Nothing viniget writes changes
// which application a double-click opens.
func mimeSummary(types []desktop.MIMEType) string {
	if len(types) == 0 {
		return ""
	}
	exts := make([]string, 0, len(types))
	for _, t := range types {
		exts = append(exts, "."+t.Extension)
	}
	shown := strings.Join(exts, " ")
	if len(exts) > 8 {
		shown = fmt.Sprintf("%s and %d more", strings.Join(exts[:8], " "), len(exts)-8)
	}
	return shown + "  (offered; the default handler is unchanged)"
}

func sourceLabel(source string) string {
	switch source {
	case "start-menu":
		return "Start Menu shortcut"
	case "desktop":
		return "Desktop shortcut"
	case "manifest":
		return "manifest entry point"
	}
	return source
}

// tildify shortens a path under the home directory, which is where every path
// viniget writes lives.
func tildify(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" || path == "" {
		return path
	}
	if rel, err := filepath.Rel(home, path); err == nil &&
		rel != ".." && !strings.HasPrefix(rel, "../") {
		return filepath.Join("~", rel)
	}
	return path
}

// DesktopRemoved renders what a removal deleted.
func DesktopRemoved(w io.Writer, id string, files []string) {
	if len(files) == 0 {
		fmt.Fprintf(w, "%s had no desktop integration to remove\n", id)
		return
	}
	fmt.Fprintf(w, "removed %d file%s for %s\n", len(files), plural(len(files)), id)
	for _, f := range files {
		fmt.Fprintf(w, "  %s\n", tildify(f))
	}
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

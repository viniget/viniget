// Package cli renders index and manifest data for the terminal.
package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/viniget/viniget/internal/index"
	"github.com/viniget/viniget/internal/manifest"
)

// JSON writes v as indented JSON.
func JSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func newTable(w io.Writer) *tabwriter.Writer {
	return tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
}

// SearchResults renders ranked search hits as a table.
func SearchResults(w io.Writer, results []index.Result) error {
	if len(results) == 0 {
		return nil
	}
	t := newTable(w)
	fmt.Fprintln(t, "ID\tNAME\tVERSION\tMONIKER\t")
	for _, r := range results {
		note := ""
		if r.Locales > 0 {
			note = fmt.Sprintf("+%d locales", r.Locales)
		}
		fmt.Fprintf(t, "%s\t%s\t%s\t%s\t%s\n",
			r.ID, truncate(r.Name, 44), r.Version, r.Moniker, note)
	}
	return t.Flush()
}

// Package renders one resolved package version in detail.
func Package(w io.Writer, p *manifest.Package) error {
	loc := p.Locale
	name := p.ID
	if loc != nil && loc.PackageName != "" {
		name = loc.PackageName
	}
	fmt.Fprintf(w, "%s  %s\n", name, p.Version)
	fmt.Fprintf(w, "%s\n", p.ID)
	if loc != nil && loc.ShortDescription != "" {
		fmt.Fprintf(w, "\n%s\n", wrap(loc.ShortDescription, 76))
	}

	fmt.Fprintln(w)
	t := newTable(w)
	if loc != nil {
		row(t, "Publisher", loc.Publisher)
		row(t, "Author", differing(loc.Author, loc.Publisher))
		row(t, "License", loc.License)
		row(t, "Homepage", firstNonEmpty(loc.PackageURL, loc.PublisherURL))
		row(t, "Support", loc.PublisherSupportURL)
		row(t, "Moniker", loc.Moniker)
		row(t, "Locale", p.DefaultLocale)
		if len(loc.Tags) > 0 {
			row(t, "Tags", strings.Join(loc.Tags, ", "))
		}
	}
	if im := p.Installer; im != nil {
		row(t, "Release date", im.ReleaseDate)
		if len(im.FileExtensions) > 0 {
			row(t, "File types", strings.Join(im.FileExtensions, ", "))
		}
	}
	if p.Singleton {
		row(t, "Manifest", "singleton")
	}
	if err := t.Flush(); err != nil {
		return err
	}

	if loc != nil && loc.Description != "" && loc.Description != loc.ShortDescription {
		fmt.Fprintf(w, "\n%s\n", wrap(loc.Description, 76))
	}

	im := p.Installer
	if im == nil || len(im.Installers) == 0 {
		return nil
	}
	fmt.Fprintf(w, "\nInstallers (%d)\n", len(im.Installers))
	it := newTable(w)
	fmt.Fprintln(it, "  ARCH\tSCOPE\tTYPE\tPRODUCT CODE\tURL")
	for _, e := range im.Installers {
		fmt.Fprintf(it, "  %s\t%s\t%s\t%s\t%s\n",
			dash(e.Architecture),
			dash(im.EffectiveScope(e)),
			dash(im.EffectiveType(e)),
			dash(truncate(im.EffectiveProductCode(e), 40)),
			e.InstallerURL)
	}
	return it.Flush()
}

func row(t io.Writer, label, value string) {
	if value == "" {
		return
	}
	fmt.Fprintf(t, "  %s\t%s\n", label, value)
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func differing(candidate, other string) string {
	if candidate == other {
		return ""
	}
	return candidate
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return string(r[:n])
	}
	return string(r[:n-1]) + "…"
}

// wrap breaks text on whitespace at width columns.
func wrap(s string, width int) string {
	var out strings.Builder
	col := 0
	for i, word := range strings.Fields(s) {
		n := len([]rune(word))
		switch {
		case i == 0:
			out.WriteString(word)
			col = n
		case col+1+n > width:
			out.WriteString("\n")
			out.WriteString(word)
			col = n
		default:
			out.WriteString(" ")
			out.WriteString(word)
			col += 1 + n
		}
	}
	return out.String()
}

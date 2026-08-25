package desktop

import (
	"bufio"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// File associations are built from the FileExtensions a winget manifest
// declares. Two things have to happen for an extension to be usable: the
// system has to have a MIME type for it, and the .desktop entry has to list
// that type.
//
// Only the second is ever done unconditionally, and it is an offer rather than
// a claim. Listing a type in MimeType= makes an application *available* for
// that type -- it appears in "Open With" -- and does not make it the default;
// the default lives in mimeapps.list, which viniget does not write. Installing
// a Windows PDF viewer should not silently take .pdf away from the one already
// handling it.

// MIMEType pairs a declared file extension with the type it resolves to.
type MIMEType struct {
	// Extension is the manifest's own spelling, without the leading dot.
	Extension string `json:"extension"`
	// Type is the MIME type the entry will advertise.
	Type string `json:"type"`
	// Declared is true when viniget had to invent the type because the shared
	// MIME database had none, and so must ship a declaration for it.
	Declared bool `json:"declared,omitempty"`
}

// ResolveMIMETypes maps a manifest's file extensions onto MIME types.
//
// An extension the system already knows keeps the system's type: an installed
// Windows text editor should offer itself for text/plain, not for a private
// type nothing else on the machine has heard of. Only genuinely unknown
// extensions get a viniget-declared type.
func ResolveMIMETypes(extensions []string) []MIMEType {
	known := systemGlobs()
	seen := map[string]bool{}
	var out []MIMEType
	for _, raw := range extensions {
		ext := normalizeExt(raw)
		if ext == "" || seen[ext] {
			continue
		}
		seen[ext] = true
		if t, ok := known[ext]; ok {
			out = append(out, MIMEType{Extension: ext, Type: t})
			continue
		}
		out = append(out, MIMEType{
			Extension: ext,
			Type:      "application/x-viniget-" + ext,
			Declared:  true,
		})
	}
	return out
}

// normalizeExt reduces a manifest's extension spelling to bare lowercase
// letters. Manifests are inconsistent about the leading dot and about case,
// and the glob database is lowercase throughout.
func normalizeExt(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.TrimPrefix(s, "*")
	s = strings.TrimPrefix(s, ".")
	if s == "" || strings.ContainsAny(s, `/\*?"'<>| `) {
		return ""
	}
	return s
}

// systemGlobs reads the shared MIME database's own extension table.
//
// globs2 is parsed rather than xdg-mime being shelled out to, because
// xdg-mime classifies a *file* -- it needs one to exist, and it sniffs the
// contents, so an empty probe file called x.pdf comes back as
// application/x-zerosize. The glob table is the answer to the question
// actually being asked: which type owns this extension.
func systemGlobs() map[string]string {
	type weighted struct {
		weight int
		mime   string
	}
	best := map[string]weighted{}

	for _, dir := range xdgDataDirs() {
		f, err := os.Open(filepath.Join(dir, "mime", "globs2"))
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" || line[0] == '#' {
				continue
			}
			// weight:mime/type:*.ext[:flags]
			parts := strings.Split(line, ":")
			if len(parts) < 3 {
				continue
			}
			weight, err := strconv.Atoi(parts[0])
			if err != nil {
				continue
			}
			glob := parts[2]
			if !strings.HasPrefix(glob, "*.") || strings.ContainsAny(glob[2:], "*?[") {
				continue
			}
			ext := strings.ToLower(glob[2:])
			if cur, ok := best[ext]; ok && cur.weight >= weight {
				continue
			}
			best[ext] = weighted{weight: weight, mime: parts[1]}
		}
		f.Close()
	}

	out := make(map[string]string, len(best))
	for ext, w := range best {
		out[ext] = w.mime
	}
	return out
}

// mimeInfo is the shared-mime-info package document.
type mimeInfo struct {
	XMLName xml.Name       `xml:"mime-info"`
	NS      string         `xml:"xmlns,attr"`
	Types   []mimeTypeDecl `xml:"mime-type"`
}

type mimeTypeDecl struct {
	Type    string   `xml:"type,attr"`
	Comment string   `xml:"comment"`
	Glob    mimeGlob `xml:"glob"`
}

type mimeGlob struct {
	Pattern string `xml:"pattern,attr"`
}

// WriteMIMEPackage declares the types viniget had to invent, so the desktop
// can resolve a file with one of those extensions to the type the entry
// advertises. It returns "" when every extension was already known, which is
// the common case and leaves nothing behind to clean up.
func WriteMIMEPackage(mimeDir, id, packageName string, types []MIMEType) (string, error) {
	var declared []mimeTypeDecl
	for _, t := range types {
		if !t.Declared {
			continue
		}
		label := packageName
		if label == "" {
			label = id
		}
		declared = append(declared, mimeTypeDecl{
			Type:    t.Type,
			Comment: fmt.Sprintf("%s file (.%s)", label, t.Extension),
			Glob:    mimeGlob{Pattern: "*." + t.Extension},
		})
	}
	if len(declared) == 0 {
		return "", nil
	}
	sort.Slice(declared, func(i, j int) bool { return declared[i].Type < declared[j].Type })

	doc := mimeInfo{
		NS:    "http://www.freedesktop.org/standards/shared-mime-info",
		Types: declared,
	}
	body, err := xml.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", err
	}
	out := append([]byte(xml.Header), body...)
	out = append(out, '\n')

	path := filepath.Join(mimeDir, "packages", "viniget-"+id+".xml")
	return path, writeFile(path, out, 0o644)
}

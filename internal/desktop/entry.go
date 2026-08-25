package desktop

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Entry is one generated .desktop file.
//
// Reference: Desktop Entry Specification
// https://specifications.freedesktop.org/desktop-entry-spec/latest/
type Entry struct {
	Name        string
	GenericName string
	Comment     string
	// Exec is the command line, already quoted for the Exec key's own rules.
	Exec string
	// Path is the working directory the application is launched in.
	Path       string
	Icon       string
	Categories []string
	Keywords   []string
	MimeTypes  []string
	// Package and Prefix are recorded on the entry so that a stray .desktop
	// file can be traced back to what wrote it, which is the difference
	// between "delete this by hand" and "viniget desktop -remove".
	Package string
	Prefix  string
	Version string
}

// Render produces the file's contents.
func (e *Entry) Render() []byte {
	var b strings.Builder
	b.WriteString("[Desktop Entry]\n")
	b.WriteString("Type=Application\n")
	kv := func(key, value string) {
		if value != "" {
			fmt.Fprintf(&b, "%s=%s\n", key, escapeValue(value))
		}
	}
	kv("Name", e.Name)
	kv("GenericName", e.GenericName)
	kv("Comment", e.Comment)
	// Exec is escaped for the value's own rules but not re-quoted: the
	// quoting the Exec key needs was applied when the line was built, and the
	// field codes must survive as-is.
	if e.Exec != "" {
		fmt.Fprintf(&b, "Exec=%s\n", escapeValue(e.Exec))
	}
	kv("Path", e.Path)
	kv("Icon", e.Icon)
	b.WriteString("Terminal=false\n")
	kv("Categories", list(e.Categories))
	kv("Keywords", list(e.Keywords))
	kv("MimeType", list(e.MimeTypes))
	kv("X-Viniget-Package", e.Package)
	kv("X-Viniget-Prefix", e.Prefix)
	kv("X-Viniget-Version", e.Version)
	return []byte(b.String())
}

// Write renders the entry to path.
func (e *Entry) Write(path string) error {
	return writeFile(path, e.Render(), 0o644)
}

func list(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return strings.Join(values, ";") + ";"
}

// escapeValue applies the escape sequences a desktop entry value requires.
// Backslash goes first, or every escape this adds would be escaped again.
var valueEscaper = strings.NewReplacer(
	`\`, `\\`,
	"\n", `\n`,
	"\t", `\t`,
	"\r", `\r`,
)

func escapeValue(s string) string { return valueEscaper.Replace(s) }

// execArg quotes one argument of an Exec line.
//
// The Exec key has quoting rules of its own, applied before the value's escape
// sequences are, so a quoted argument's backslashes get doubled twice over --
// once here and once by escapeValue. That is correct, and it is why this
// returns a fragment rather than a finished value.
func execArg(s string) string {
	if s == "" {
		return `""`
	}
	if !strings.ContainsAny(s, " \t\n\"'\\<>~|&;$*?#()`") {
		return s
	}
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"', '`', '$', '\\':
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	b.WriteByte('"')
	return b.String()
}

// tagCategories maps winget tags onto freedesktop menu categories. The table
// is deliberately small: a wrong category buries an application in the wrong
// submenu, which is worse than the Utility fallback everything else gets.
var tagCategories = map[string][]string{
	"audio":         {"AudioVideo", "Audio"},
	"music":         {"AudioVideo", "Audio"},
	"player":        {"AudioVideo"},
	"video":         {"AudioVideo", "Video"},
	"media":         {"AudioVideo"},
	"browser":       {"Network", "WebBrowser"},
	"chat":          {"Network", "InstantMessaging"},
	"messaging":     {"Network", "InstantMessaging"},
	"messenger":     {"Network", "InstantMessaging"},
	"email":         {"Network", "Email"},
	"ftp":           {"Network", "FileTransfer"},
	"torrent":       {"Network", "P2P"},
	"download":      {"Network"},
	"network":       {"Network"},
	"vpn":           {"Network"},
	"ide":           {"Development", "IDE"},
	"editor":        {"Utility", "TextEditor"},
	"text-editor":   {"Utility", "TextEditor"},
	"developer":     {"Development"},
	"development":   {"Development"},
	"programming":   {"Development"},
	"git":           {"Development", "RevisionControl"},
	"debugger":      {"Development", "Debugger"},
	"database":      {"Development", "Database"},
	"graphics":      {"Graphics"},
	"image":         {"Graphics"},
	"image-editor":  {"Graphics", "RasterGraphics"},
	"photo":         {"Graphics", "Photography"},
	"screenshot":    {"Graphics"},
	"vector":        {"Graphics", "VectorGraphics"},
	"3d":            {"Graphics", "3DGraphics"},
	"cad":           {"Graphics", "Engineering"},
	"office":        {"Office"},
	"document":      {"Office"},
	"pdf":           {"Office", "Viewer"},
	"spreadsheet":   {"Office", "Spreadsheet"},
	"presentation":  {"Office", "Presentation"},
	"notes":         {"Office"},
	"game":          {"Game"},
	"emulator":      {"Game", "Emulator"},
	"science":       {"Science"},
	"math":          {"Science", "Math"},
	"education":     {"Education"},
	"archiver":      {"Utility", "Archiving"},
	"compression":   {"Utility", "Archiving"},
	"zip":           {"Utility", "Archiving"},
	"backup":        {"System"},
	"system":        {"System"},
	"monitor":       {"System", "Monitor"},
	"terminal":      {"System", "TerminalEmulator"},
	"file-manager":  {"System", "FileManager"},
	"filemanager":   {"System", "FileManager"},
	"security":      {"System", "Security"},
	"antivirus":     {"System", "Security"},
	"password":      {"Utility", "Security"},
	"encryption":    {"Utility", "Security"},
	"utility":       {"Utility"},
	"calculator":    {"Utility", "Calculator"},
	"clipboard":     {"Utility"},
	"search":        {"Utility"},
	"remote":        {"Network", "RemoteAccess"},
	"remote-access": {"Network", "RemoteAccess"},
	"ssh":           {"Network", "RemoteAccess"},
	"accessibility": {"Utility", "Accessibility"},
}

// mainCategories are the ones that decide which menu an entry appears in. A
// desktop entry needs at least one, and an entry with only subcategories is
// filed nowhere at all.
var mainCategories = map[string]bool{
	"AudioVideo": true, "Audio": true, "Video": true, "Development": true,
	"Education": true, "Game": true, "Graphics": true, "Network": true,
	"Office": true, "Science": true, "Settings": true, "System": true,
	"Utility": true,
}

// Categories derives menu categories from a package's winget tags, falling
// back to Utility so that every entry lands somewhere a menu will show it.
func Categories(tags []string) []string {
	seen := map[string]bool{}
	var out []string
	hasMain := false
	for _, tag := range tags {
		for _, c := range tagCategories[strings.ToLower(strings.TrimSpace(tag))] {
			if seen[c] {
				continue
			}
			seen[c] = true
			out = append(out, c)
			hasMain = hasMain || mainCategories[c]
		}
	}
	if !hasMain {
		out = append([]string{"Utility"}, out...)
	}
	return out
}

// Keywords turns tags into search terms, which is where a tag that named no
// category still earns its place.
func Keywords(tags []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag == "" || seen[strings.ToLower(tag)] {
			continue
		}
		seen[strings.ToLower(tag)] = true
		out = append(out, tag)
		if len(out) == 12 {
			break
		}
	}
	sort.Strings(out)
	return out
}

func writeFile(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	// Written through a temporary file so a desktop environment watching the
	// directory never indexes a half-written entry.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

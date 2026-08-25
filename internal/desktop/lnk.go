// Package desktop turns what an installer left inside a Wine prefix into
// native desktop integration: .desktop entries, icons in the hicolor theme,
// and file-type associations.
//
// Everything here starts from the Start Menu shortcut. A .lnk is the only
// honest source for a package's entry point -- matching the package name
// against executables on disk picks WinMerge32BitPluginProxy.exe over
// WinMergeU.exe and reports a working application as broken. The same parse
// feeds both consumers: what viniget launches, and what goes in the generated
// .desktop file.
package desktop

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf16"
)

// Reference: [MS-SHLLINK]
// https://learn.microsoft.com/openspecs/windows_protocols/ms-shllink/

const (
	lnkHeaderSize = 0x4C
	lnkMagic      = 0x0000004C
)

var lnkCLSID = [16]byte{
	0x01, 0x14, 0x02, 0x00, 0x00, 0x00, 0x00, 0x00,
	0xC0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x46,
}

// LinkFlags bits this parser acts on (MS-SHLLINK 2.1.1).
const (
	flagHasTargetIDList = 1 << 0
	flagHasLinkInfo     = 1 << 1
	flagHasName         = 1 << 2
	flagHasRelativePath = 1 << 3
	flagHasWorkingDir   = 1 << 4
	flagHasArguments    = 1 << 5
	flagHasIconLocation = 1 << 6
	flagIsUnicode       = 1 << 7
)

// sigEnvironmentProps is the ExtraData block that carries a plain target path.
// It is the fallback for shortcuts written without a LinkInfo block, which is
// most of what MSI-authored shortcuts look like.
const sigEnvironmentProps = 0xA0000001

// Shortcut is one parsed .lnk.
//
// The Windows-side strings are kept verbatim; Resolve is what maps them onto
// the host filesystem, because that mapping needs the prefix and the caller
// may well want the raw values for a note.
type Shortcut struct {
	// Path is the host path of the .lnk itself.
	Path string `json:"path"`
	// Target is the absolute Windows path from the LinkInfo block.
	Target string `json:"target,omitempty"`
	// RelativePath locates the target from the .lnk's own directory, and is
	// the only target some shortcuts carry.
	RelativePath string `json:"relative_path,omitempty"`
	WorkingDir   string `json:"working_dir,omitempty"`
	Arguments    string `json:"arguments,omitempty"`
	IconLocation string `json:"icon_location,omitempty"`
	// Name is the shortcut's description, which installers use for the human
	// label the Start Menu never shows.
	Name string `json:"name,omitempty"`
	// Desktop records that this shortcut came from a Desktop folder rather
	// than the Start Menu. Both are real, but the Start Menu is the catalogue.
	Desktop bool `json:"desktop,omitempty"`
}

// Label is the name a menu entry should carry: the shortcut's filename, which
// is what Windows itself displays.
func (s *Shortcut) Label() string {
	base := filepath.Base(s.Path)
	return strings.TrimSuffix(base, filepath.Ext(base))
}

// Resolve maps the shortcut's target onto a host path inside prefix, or
// returns "" when nothing it names exists.
func (s *Shortcut) Resolve(prefix string) string {
	if s.Target != "" {
		return resolveWindowsPath(prefix, s.Target)
	}
	if s.RelativePath != "" {
		// A relative target is relative to the .lnk on the host side too, so
		// this one never goes through the drive letters.
		joined := filepath.Join(filepath.Dir(s.Path), fromWindowsSep(s.RelativePath))
		return existingPath(joined)
	}
	return ""
}

// ResolveWorkingDir maps the shortcut's working directory into the prefix.
func (s *Shortcut) ResolveWorkingDir(prefix string) string {
	if s.WorkingDir == "" {
		return ""
	}
	return resolveWindowsPath(prefix, s.WorkingDir)
}

// resolveWindowsPath maps an absolute Windows path onto the host filesystem.
func resolveWindowsPath(prefix, win string) string {
	win = strings.TrimSpace(win)
	drive, rest, ok := strings.Cut(win, ":")
	if !ok || len(drive) != 1 {
		return ""
	}
	host := filepath.Join(prefix, "drive_"+strings.ToLower(drive), fromWindowsSep(rest))
	return existingPath(host)
}

func fromWindowsSep(p string) string {
	return strings.ReplaceAll(strings.TrimLeft(p, `\/`), `\`, "/")
}

// existingPath returns path if it exists, otherwise the case-insensitive match
// for it, otherwise "".
//
// Wine prefixes are case-sensitive on disk and .lnk targets are not: an
// installer that wrote "Program Files" and a shortcut that says "PROGRAM
// FILES" are naming the same directory, and Windows does not care.
func existingPath(path string) string {
	if _, err := os.Lstat(path); err == nil {
		return path
	}
	cur := "/"
	if !filepath.IsAbs(path) {
		cur = "."
	}
	for _, part := range strings.Split(path, string(filepath.Separator)) {
		if part == "" {
			continue
		}
		next := filepath.Join(cur, part)
		if _, err := os.Lstat(next); err == nil {
			cur = next
			continue
		}
		entries, err := os.ReadDir(cur)
		if err != nil {
			return ""
		}
		found := ""
		for _, e := range entries {
			if strings.EqualFold(e.Name(), part) {
				found = filepath.Join(cur, e.Name())
				break
			}
		}
		if found == "" {
			return ""
		}
		cur = found
	}
	if cur == "/" || cur == "." {
		return ""
	}
	return cur
}

// ParseLNK reads one shell link. A file that is not a shell link is not an
// error -- Start Menu folders hold .ini and .url files too -- so it comes back
// as a nil Shortcut.
func ParseLNK(path string) *Shortcut {
	buf, err := os.ReadFile(path)
	if err != nil || len(buf) < lnkHeaderSize {
		return nil
	}
	if binary.LittleEndian.Uint32(buf) != lnkMagic {
		return nil
	}
	if [16]byte(buf[4:20]) != lnkCLSID {
		return nil
	}

	flags := binary.LittleEndian.Uint32(buf[20:])
	unicode := flags&flagIsUnicode != 0
	sc := &Shortcut{Path: path}
	off := lnkHeaderSize

	if flags&flagHasTargetIDList != 0 {
		if off+2 > len(buf) {
			return sc
		}
		off += 2 + int(binary.LittleEndian.Uint16(buf[off:]))
	}

	if flags&flagHasLinkInfo != 0 {
		if off+4 > len(buf) {
			return sc
		}
		size := int(binary.LittleEndian.Uint32(buf[off:]))
		sc.Target = parseLinkInfo(buf, off)
		if size < 4 {
			return sc
		}
		off += size
	}

	// StringData follows in a fixed order, each entry present only if its flag
	// is set. A short read stops the walk: the remaining fields are positional,
	// so there is nothing to recover once one is truncated.
	for _, f := range []struct {
		bit   uint32
		field *string
	}{
		{flagHasName, &sc.Name},
		{flagHasRelativePath, &sc.RelativePath},
		{flagHasWorkingDir, &sc.WorkingDir},
		{flagHasArguments, &sc.Arguments},
		{flagHasIconLocation, &sc.IconLocation},
	} {
		if flags&f.bit == 0 {
			continue
		}
		value, next, ok := readLNKString(buf, off, unicode)
		if !ok {
			break
		}
		*f.field, off = value, next
	}

	if sc.Target == "" {
		sc.Target = parseExtraTarget(buf, off)
	}
	return sc
}

// readLNKString reads one counted StringData entry.
//
// CountCharacters excludes a terminating null per the specification, but real
// writers -- Wine's own shell32 among them -- include one anyway. Trimming is
// not cosmetic: a shortcut with no arguments comes back carrying a single NUL,
// which is a non-empty string that would be passed to the application.
func readLNKString(buf []byte, off int, unicode bool) (string, int, bool) {
	if off+2 > len(buf) {
		return "", off, false
	}
	count := int(binary.LittleEndian.Uint16(buf[off:]))
	off += 2
	width := 1
	if unicode {
		width = 2
	}
	end := off + count*width
	if end > len(buf) {
		return "", off, false
	}
	raw := buf[off:end]
	if unicode {
		return strings.TrimRight(decodeUTF16(raw), "\x00"), end, true
	}
	return strings.TrimRight(decodeCP1252(raw), "\x00"), end, true
}

// parseLinkInfo extracts LocalBasePath (plus CommonPathSuffix) from a LinkInfo
// block. A link to a network share carries no local path and yields "".
func parseLinkInfo(buf []byte, start int) string {
	if start+32 > len(buf) {
		return ""
	}
	headerSize := binary.LittleEndian.Uint32(buf[start+4:])
	infoFlags := binary.LittleEndian.Uint32(buf[start+8:])
	if infoFlags&0x1 == 0 { // VolumeIDAndLocalBasePath
		return ""
	}

	baseOff := int(binary.LittleEndian.Uint32(buf[start+16:]))
	suffixOff := int(binary.LittleEndian.Uint32(buf[start+24:]))
	wide := false
	// A header of 0x24 or more carries the unicode offsets, which are the
	// only ones that survive a path with non-ANSI characters in it.
	if headerSize >= 0x24 && start+32 <= len(buf) {
		if u := int(binary.LittleEndian.Uint32(buf[start+28:])); u != 0 {
			baseOff, wide = u, true
		}
	}

	base := readCString(buf, start+baseOff, wide)
	suffix := ""
	if suffixOff != 0 {
		suffix = readCString(buf, start+suffixOff, false)
	}
	return base + suffix
}

// parseExtraTarget walks the ExtraData blocks for an
// EnvironmentVariableDataBlock, which names the target directly.
func parseExtraTarget(buf []byte, off int) string {
	for off+8 <= len(buf) {
		size := int(binary.LittleEndian.Uint32(buf[off:]))
		if size < 8 || off+size > len(buf) {
			return ""
		}
		if binary.LittleEndian.Uint32(buf[off+4:]) == sigEnvironmentProps {
			block := buf[off:][:size]
			// TargetAnsi is 260 bytes at offset 8; TargetUnicode, when the
			// block is long enough to carry it, is 520 bytes at offset 268.
			if len(block) >= 788 {
				if s := strings.TrimRight(decodeUTF16(block[268:788]), "\x00"); s != "" {
					return trimAtNUL(s)
				}
			}
			if len(block) >= 268 {
				return trimAtNUL(decodeCP1252(block[8:268]))
			}
			return ""
		}
		off += size
	}
	return ""
}

func trimAtNUL(s string) string {
	if i := strings.IndexByte(s, 0); i >= 0 {
		return s[:i]
	}
	return s
}

func readCString(buf []byte, off int, wide bool) string {
	if off < 0 || off >= len(buf) {
		return ""
	}
	if wide {
		end := off
		for end+1 < len(buf) && !(buf[end] == 0 && buf[end+1] == 0) {
			end += 2
		}
		return decodeUTF16(buf[off:end])
	}
	end := off
	for end < len(buf) && buf[end] != 0 {
		end++
	}
	return decodeCP1252(buf[off:end])
}

func decodeUTF16(b []byte) string {
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = binary.LittleEndian.Uint16(b[i*2:])
	}
	return string(utf16.Decode(u))
}

// cp1252High maps the 0x80-0x9F range, which is where cp1252 and latin-1
// disagree. Above it the two are identical, so a byte is its own code point.
var cp1252High = [32]rune{
	'€', '�', '‚', 'ƒ', '„', '…', '†', '‡',
	'ˆ', '‰', 'Š', '‹', 'Œ', '�', 'Ž', '�',
	'�', '‘', '’', '“', '”', '•', '–', '—',
	'˜', '™', 'š', '›', 'œ', '�', 'ž', 'Ÿ',
}

func decodeCP1252(b []byte) string {
	var sb strings.Builder
	sb.Grow(len(b))
	for _, c := range b {
		switch {
		case c < 0x80:
			sb.WriteByte(c)
		case c < 0xA0:
			sb.WriteRune(cp1252High[c-0x80])
		default:
			sb.WriteRune(rune(c))
		}
	}
	return sb.String()
}

// shortcutRoots are the folders an installer writes shortcuts to, relative to
// the prefix root. The Start Menu is the catalogue of what a package installed;
// a Desktop shortcut is usually a duplicate of one, and occasionally the only
// one there is.
var shortcutRoots = []struct {
	glob    string
	desktop bool
}{
	{"drive_c/ProgramData/Microsoft/Windows/Start Menu", false},
	{"drive_c/users/*/AppData/Roaming/Microsoft/Windows/Start Menu", false},
	{"drive_c/users/*/Start Menu", false},
	{"drive_c/users/Public/Desktop", true},
	{"drive_c/users/*/Desktop", true},
}

// noisePhrases are shortcut names that always sit next to an application and
// are never the application. An uninstaller in the applications menu is worse
// than no entry at all.
//
// Written in the normalised form isNoise compares against: punctuation gone,
// so "what's new" is spelled "what s new".
var noisePhrases = []string{
	"readme", "read me", "license", "licence", "help", "website", "web site",
	"homepage", "home page", "documentation", "user s guide", "user guide",
	"manual", "what s new", "whatsnew", "about", "release notes", "changelog",
	"support", "donate", "faq", "safe mode", "troubleshoot", "repair", "modify",
}

// noiseWordPrefixes catch the spellings that are one word rather than two:
// Uninstall.lnk, UninstallFoo.lnk, unins000.lnk.
var noiseWordPrefixes = []string{"unins"}

// isNoise reports whether a shortcut name is one of those entries.
//
// Matched on word boundaries rather than as substrings, which is the
// difference between filtering "Demo Readme" and throwing away "Demo Helper"
// for containing "help".
func isNoise(name string) bool {
	words := nameWords(name)
	padded := " " + strings.Join(words, " ") + " "
	for _, p := range noisePhrases {
		if strings.Contains(padded, " "+p+" ") {
			return true
		}
	}
	for _, w := range words {
		for _, p := range noiseWordPrefixes {
			if strings.HasPrefix(w, p) {
				return true
			}
		}
	}
	return false
}

func nameWords(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9')
	})
}

// FindShortcuts returns every application shortcut in a prefix, best candidate
// first.
//
// "Best" is decided in this order: it resolves to a file that exists, it is an
// executable rather than a document, it came from the Start Menu, and its name
// looks like the package's. Only the first two are strong signals; the rest
// break ties within a suite that installed several real entry points.
func FindShortcuts(prefix, packageName string) []*Shortcut {
	var found []*Shortcut
	seen := map[string]bool{}

	for _, root := range shortcutRoots {
		matches, _ := filepath.Glob(filepath.Join(prefix, filepath.FromSlash(root.glob)))
		for _, dir := range matches {
			filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
				if err != nil || d.IsDir() {
					return nil
				}
				if !strings.EqualFold(filepath.Ext(path), ".lnk") || seen[path] {
					return nil
				}
				seen[path] = true
				if isNoise(filepath.Base(path)) {
					return nil
				}
				if sc := ParseLNK(path); sc != nil {
					sc.Desktop = root.desktop
					found = append(found, sc)
				}
				return nil
			})
		}
	}

	rank := func(s *Shortcut) [5]int {
		target := s.Resolve(prefix)
		score := [5]int{1, 1, 1, 1, len(s.Label())}
		if target != "" {
			score[0] = 0
		}
		// A shortcut to a .txt or a .chm is documentation that escaped the
		// noise filter, not the application.
		if strings.EqualFold(filepath.Ext(target), ".exe") {
			score[1] = 0
		}
		if !s.Desktop {
			score[2] = 0
		}
		if packageName != "" && namesMatch(s.Label(), packageName) {
			score[3] = 0
		}
		return score
	}
	sort.SliceStable(found, func(i, j int) bool {
		a, b := rank(found[i]), rank(found[j])
		for k := range a {
			if a[k] != b[k] {
				return a[k] < b[k]
			}
		}
		return found[i].Path < found[j].Path
	})
	return found
}

// namesMatch reports whether a shortcut label and a package name refer to the
// same thing, ignoring the punctuation and edition suffixes that differ
// between a manifest's PackageName and what an installer writes on disk.
func namesMatch(label, packageName string) bool {
	a, b := squash(label), squash(packageName)
	if a == "" || b == "" {
		return false
	}
	return strings.Contains(a, b) || strings.Contains(b, a)
}

func squash(s string) string {
	var sb strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			sb.WriteRune(r)
		}
	}
	return sb.String()
}

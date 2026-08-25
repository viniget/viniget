package desktop

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"unicode/utf16"
)

// lnkBuilder assembles a shell link byte for byte, which is the only way to
// test the parser against the shapes that matter: the ANSI/unicode split, a
// LinkInfo block with no local path, and the trailing NUL real writers add.
type lnkBuilder struct {
	flags   uint32
	unicode bool
	target  string
	strings []string // in StringData order, for the flags that are set
	extra   []byte
}

func (b *lnkBuilder) bytes() []byte {
	out := make([]byte, 0x4C)
	binary.LittleEndian.PutUint32(out, lnkMagic)
	copy(out[4:], lnkCLSID[:])
	binary.LittleEndian.PutUint32(out[20:], b.flags)

	if b.flags&flagHasTargetIDList != 0 {
		idlist := []byte{0xAA, 0xBB, 0xCC, 0xDD, 0x00, 0x00}
		var sz [2]byte
		binary.LittleEndian.PutUint16(sz[:], uint16(len(idlist)))
		out = append(out, sz[:]...)
		out = append(out, idlist...)
	}
	if b.flags&flagHasLinkInfo != 0 {
		out = append(out, linkInfoBlock(b.target)...)
	}
	for _, s := range b.strings {
		out = append(out, stringData(s, b.unicode)...)
	}
	out = append(out, b.extra...)
	out = append(out, 0, 0, 0, 0) // terminal ExtraData block
	return out
}

// linkInfoBlock builds the ANSI form: a 28-byte header, then the local base
// path and an empty common suffix.
func linkInfoBlock(target string) []byte {
	const headerSize = 28
	baseOff := headerSize
	suffixOff := baseOff + len(target) + 1
	total := suffixOff + 1

	b := make([]byte, total)
	binary.LittleEndian.PutUint32(b[0:], uint32(total))
	binary.LittleEndian.PutUint32(b[4:], headerSize)
	binary.LittleEndian.PutUint32(b[8:], 0x1) // VolumeIDAndLocalBasePath
	binary.LittleEndian.PutUint32(b[16:], uint32(baseOff))
	binary.LittleEndian.PutUint32(b[24:], uint32(suffixOff))
	copy(b[baseOff:], target)
	return b
}

// stringData writes a counted string whose count includes the terminating
// NUL, which is what Wine's own shell32 emits and what the specification says
// it should not.
func stringData(s string, unicode bool) []byte {
	if unicode {
		u := utf16.Encode([]rune(s + "\x00"))
		b := make([]byte, 2+len(u)*2)
		binary.LittleEndian.PutUint16(b, uint16(len(u)))
		for i, c := range u {
			binary.LittleEndian.PutUint16(b[2+i*2:], c)
		}
		return b
	}
	b := make([]byte, 2+len(s)+1)
	binary.LittleEndian.PutUint16(b, uint16(len(s)+1))
	copy(b[2:], s)
	return b
}

func writeLNK(t *testing.T, path string, b *lnkBuilder) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b.bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestParseLNKUnicodeStrings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "App.lnk")
	writeLNK(t, path, &lnkBuilder{
		flags: flagHasLinkInfo | flagHasName | flagHasWorkingDir |
			flagHasArguments | flagHasIconLocation | flagIsUnicode,
		unicode: true,
		target:  `C:\Program Files\App\app.exe`,
		strings: []string{
			"Nhắn gửi yêu thương",
			`C:\Program Files\App`,
			"", // no arguments, written as a lone NUL
			`C:\Program Files\App\app.exe,0`,
		},
	})

	sc := ParseLNK(path)
	if sc == nil {
		t.Fatal("ParseLNK returned nil for a valid shell link")
	}
	if sc.Target != `C:\Program Files\App\app.exe` {
		t.Errorf("Target = %q", sc.Target)
	}
	if sc.Name != "Nhắn gửi yêu thương" {
		t.Errorf("Name = %q", sc.Name)
	}
	// The whole point of trimming: an empty argument list must stay empty, or
	// a NUL byte gets passed to the application as its first argument.
	if sc.Arguments != "" {
		t.Errorf("Arguments = %q, want empty", sc.Arguments)
	}
	if sc.WorkingDir != `C:\Program Files\App` {
		t.Errorf("WorkingDir = %q", sc.WorkingDir)
	}
	if sc.IconLocation != `C:\Program Files\App\app.exe,0` {
		t.Errorf("IconLocation = %q", sc.IconLocation)
	}
	if sc.Label() != "App" {
		t.Errorf("Label = %q", sc.Label())
	}
}

func TestParseLNKSkipsIDList(t *testing.T) {
	path := filepath.Join(t.TempDir(), "Skip.lnk")
	writeLNK(t, path, &lnkBuilder{
		flags:   flagHasTargetIDList | flagHasLinkInfo | flagHasRelativePath,
		target:  `C:\app.exe`,
		strings: []string{`..\app.exe`},
	})
	sc := ParseLNK(path)
	if sc == nil {
		t.Fatal("nil shortcut")
	}
	// If the IDList were not skipped, RelativePath would be read out of the
	// middle of it and come back as garbage.
	if sc.RelativePath != `..\app.exe` {
		t.Errorf("RelativePath = %q", sc.RelativePath)
	}
}

func TestParseLNKRejectsNonLinks(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string][]byte{
		"short.lnk": []byte("nope"),
		"wrong.lnk": append([]byte{0x4C, 0, 0, 0}, make([]byte, 100)...),
		"empty.lnk": {},
	} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, content, 0o644); err != nil {
			t.Fatal(err)
		}
		if sc := ParseLNK(path); sc != nil {
			t.Errorf("%s: parsed as a shell link", name)
		}
	}
}

func TestParseExtraTargetFallback(t *testing.T) {
	// A shortcut with no LinkInfo at all: the target has to come out of the
	// EnvironmentVariableDataBlock instead.
	block := make([]byte, 788)
	binary.LittleEndian.PutUint32(block, 788)
	binary.LittleEndian.PutUint32(block[4:], sigEnvironmentProps)
	copy(block[8:], `C:\ansi\app.exe`)
	for i, c := range utf16.Encode([]rune(`C:\Games\App\app.exe`)) {
		binary.LittleEndian.PutUint16(block[268+i*2:], c)
	}

	path := filepath.Join(t.TempDir(), "Env.lnk")
	writeLNK(t, path, &lnkBuilder{flags: flagHasName, strings: []string{"App"}, extra: block})

	sc := ParseLNK(path)
	if sc == nil {
		t.Fatal("nil shortcut")
	}
	if sc.Target != `C:\Games\App\app.exe` {
		t.Errorf("Target = %q, want the unicode target from the extra block", sc.Target)
	}
}

func TestResolveIsCaseInsensitive(t *testing.T) {
	prefix := t.TempDir()
	real := filepath.Join(prefix, "drive_c", "Program Files", "App")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(real, "App.exe")
	if err := os.WriteFile(exe, []byte("MZ"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Wine prefixes are case-sensitive on disk; .lnk targets are not.
	sc := &Shortcut{Target: `C:\PROGRAM FILES\app\APP.EXE`}
	if got := sc.Resolve(prefix); got != exe {
		t.Errorf("Resolve = %q, want %q", got, exe)
	}

	missing := &Shortcut{Target: `C:\Program Files\App\nope.exe`}
	if got := missing.Resolve(prefix); got != "" {
		t.Errorf("Resolve of a missing target = %q, want empty", got)
	}
	if got := (&Shortcut{Target: `\\server\share\app.exe`}).Resolve(prefix); got != "" {
		t.Errorf("Resolve of a UNC target = %q, want empty", got)
	}
}

func TestFindShortcutsFiltersAndRanks(t *testing.T) {
	prefix := t.TempDir()
	appDir := filepath.Join(prefix, "drive_c", "Program Files", "Demo")
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Demo.exe", "DemoHelper.exe", "readme.txt"} {
		if err := os.WriteFile(filepath.Join(appDir, name), []byte("MZ"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	menu := filepath.Join(prefix, "drive_c", "ProgramData", "Microsoft", "Windows", "Start Menu", "Programs")
	link := func(dir, name, target string) {
		writeLNK(t, filepath.Join(dir, name+".lnk"), &lnkBuilder{
			flags: flagHasLinkInfo, target: target,
		})
	}
	link(menu, "Demo Helper Utility", `C:\Program Files\Demo\DemoHelper.exe`)
	link(menu, "Demo", `C:\Program Files\Demo\Demo.exe`)
	link(menu, "Demo Tutorial", `C:\Program Files\Demo\readme.txt`)
	link(menu, "Uninstall Demo", `C:\Program Files\Demo\Demo.exe`)
	link(filepath.Join(prefix, "drive_c", "users", "Public", "Desktop"), "Demo",
		`C:\Program Files\Demo\Demo.exe`)

	found := FindShortcuts(prefix, "Demo")
	var labels []string
	for _, sc := range found {
		labels = append(labels, sc.Label())
	}
	for _, sc := range found {
		if sc.Label() == "Uninstall Demo" {
			t.Errorf("uninstaller shortcut survived the noise filter: %v", labels)
		}
	}
	// The helper is not the application, but it is not noise either -- "help"
	// as a substring would have thrown it away.
	if len(found) != 4 {
		t.Fatalf("found %d shortcuts, want 4: %v", len(found), labels)
	}
	if len(found) == 0 {
		t.Fatal("no shortcuts found")
	}
	// The application itself outranks its helper, its readme and the Desktop
	// duplicate: name match beats the rest once both resolve to an .exe.
	if found[0].Label() != "Demo" || found[0].Desktop {
		t.Errorf("best candidate = %q (desktop=%v), want the Start Menu \"Demo\"; order: %v",
			found[0].Label(), found[0].Desktop, labels)
	}
	// A shortcut to a .txt is documentation whatever it is called, and ranks
	// below every shortcut that points at an executable.
	last := found[len(found)-1]
	if last.Label() != "Demo Tutorial" {
		t.Errorf("worst candidate = %q, want the .txt shortcut; order: %v", last.Label(), labels)
	}
}

func TestIsNoise(t *testing.T) {
	cases := map[string]bool{
		"Uninstall Notepad++.lnk": true,
		"unins000.lnk":            true,
		"UninstallGit.lnk":        true,
		"Everything Website.lnk":  true,
		"What's New.lnk":          true,
		"Demo Helper.lnk":         false,
		"HelpNDoc.lnk":            false,
		"Git Bash.lnk":            false,
		"7-Zip File Manager.lnk":  false,
	}
	for name, want := range cases {
		if got := isNoise(name); got != want {
			t.Errorf("isNoise(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestDecodeCP1252(t *testing.T) {
	// 0x80-0x9F is where cp1252 and latin-1 disagree, and it is exactly where
	// a smart quote in a shortcut description lands.
	if got := decodeCP1252([]byte{'A', 0x92, 0x80, 0xE9}); got != "A’€é" {
		t.Errorf("decodeCP1252 = %q", got)
	}
}

func TestNamesMatch(t *testing.T) {
	cases := []struct {
		label, name string
		want        bool
	}{
		{"Notepad++", "Notepad++", true},
		{"Git Bash", "Git", true},
		{"WinMerge", "WinMerge 2.16", true},
		{"7-Zip File Manager", "7-Zip", true},
		{"Everything", "Notepad++", false},
	}
	for _, c := range cases {
		if got := namesMatch(c.label, c.name); got != c.want {
			t.Errorf("namesMatch(%q, %q) = %v, want %v", c.label, c.name, got, c.want)
		}
	}
}

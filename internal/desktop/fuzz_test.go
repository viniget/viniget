package desktop

import (
	"os"
	"path/filepath"
	"testing"
)

// Both parsers read attacker-influenced bytes: a .lnk comes out of a
// downloaded installer, and so does the executable its icon is read from.
// Neither may panic on anything, however malformed.

func FuzzParseLNK(f *testing.F) {
	f.Add((&lnkBuilder{
		flags:   flagHasLinkInfo | flagHasName | flagIsUnicode,
		unicode: true,
		target:  `C:\Program Files\App\app.exe`,
		strings: []string{"App"},
	}).bytes())
	f.Add((&lnkBuilder{
		flags:   flagHasTargetIDList | flagHasLinkInfo | flagHasRelativePath,
		target:  `C:\app.exe`,
		strings: []string{`..\app.exe`},
	}).bytes())
	f.Add([]byte("MZ not a link at all"))

	dir := f.TempDir()
	path := filepath.Join(dir, "fuzz.lnk")
	f.Fuzz(func(t *testing.T, data []byte) {
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Skip()
		}
		sc := ParseLNK(path)
		if sc == nil {
			return
		}
		// Resolution walks the filesystem from whatever the parse produced,
		// which is the other half of the attack surface.
		sc.Resolve(dir)
		sc.ResolveWorkingDir(dir)
		ParseIconLocation(sc.IconLocation)
		SplitWindowsArgs(sc.Arguments)
	})
}

func FuzzDecodeIcon(f *testing.F) {
	f.Add(demoIconDIB())
	f.Add(ico(demoIconDIB()))
	f.Add([]byte{0, 0, 1, 0, 1, 0})

	f.Fuzz(func(t *testing.T, data []byte) {
		if isICO(data) {
			decodeICO(data)
			return
		}
		decodeDIB(data)
	})
}

func FuzzPEIcons(f *testing.F) {
	f.Add(buildPE(&testing.T{}, demoIconDIB(), 1, 1), 0)
	f.Add([]byte("MZ"), 0)

	f.Fuzz(func(t *testing.T, data []byte, index int) {
		peIcons(data, index)
	})
}

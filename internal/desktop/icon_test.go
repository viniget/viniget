package desktop

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

// dib assembles one icon image in the BITMAPINFOHEADER form: a colour bitmap
// with the 1-bit AND mask stacked underneath it, both stored bottom-up.
func dib(width, height, bits int, palette []color.RGBA, pixels []byte, mask []byte) []byte {
	header := make([]byte, 40)
	binary.LittleEndian.PutUint32(header[0:], 40)
	binary.LittleEndian.PutUint32(header[4:], uint32(width))
	binary.LittleEndian.PutUint32(header[8:], uint32(height*2)) // colour + mask
	binary.LittleEndian.PutUint16(header[12:], 1)
	binary.LittleEndian.PutUint16(header[14:], uint16(bits))
	binary.LittleEndian.PutUint32(header[32:], uint32(len(palette)))

	out := header
	for _, c := range palette {
		out = append(out, c.B, c.G, c.R, 0)
	}
	out = append(out, pixels...)
	return append(out, mask...)
}

func ico(images ...[]byte) []byte {
	var buf bytes.Buffer
	binary.Write(&buf, binary.LittleEndian, uint16(0))
	binary.Write(&buf, binary.LittleEndian, uint16(1))
	binary.Write(&buf, binary.LittleEndian, uint16(len(images)))
	offset := 6 + 16*len(images)
	for _, img := range images {
		entry := make([]byte, 16)
		binary.LittleEndian.PutUint32(entry[8:], uint32(len(img)))
		binary.LittleEndian.PutUint32(entry[12:], uint32(offset))
		buf.Write(entry)
		offset += len(img)
	}
	for _, img := range images {
		buf.Write(img)
	}
	return buf.Bytes()
}

// bgra32 builds a 2x2 32-bit image. Rows are bottom-up, so the first row of
// pixels given is the bottom one.
func bgra32(px [4]color.RGBA) []byte {
	out := make([]byte, 0, 16)
	for _, c := range px {
		out = append(out, c.B, c.G, c.R, c.A)
	}
	return out
}

func TestDecodeDIB32Bit(t *testing.T) {
	red := color.RGBA{R: 0xFF, A: 0xFF}
	blue := color.RGBA{B: 0xFF, A: 0xFF}
	// Bottom row first: blue, blue / then top row: red, red.
	pixels := bgra32([4]color.RGBA{blue, blue, red, red})
	img, err := decodeDIB(dib(2, 2, 32, nil, pixels, make([]byte, 8)))
	if err != nil {
		t.Fatal(err)
	}
	if got := img.Bounds(); got.Dx() != 2 || got.Dy() != 2 {
		t.Fatalf("bounds = %v, want 2x2 (the AND mask must not count toward height)", got)
	}
	if got := img.At(0, 0); got != color.NRGBA(red) {
		t.Errorf("top-left = %v, want red -- rows are stored bottom-up", got)
	}
	if got := img.At(0, 1); got != color.NRGBA(blue) {
		t.Errorf("bottom-left = %v, want blue", got)
	}
}

func TestDecodeDIB32BitEmptyAlphaFallsBackToMask(t *testing.T) {
	// A 32-bit icon that never filled its alpha channel in is not invisible.
	// The AND mask is where its transparency actually lives.
	opaque := color.RGBA{R: 0x10, G: 0x20, B: 0x30}
	pixels := bgra32([4]color.RGBA{opaque, opaque, opaque, opaque})
	// Mask rows are 4 bytes each; the top bit of the top row marks (0,0)
	// transparent.
	mask := []byte{0x00, 0, 0, 0, 0x80, 0, 0, 0}

	img, err := decodeDIB(dib(2, 2, 32, nil, pixels, mask))
	if err != nil {
		t.Fatal(err)
	}
	if a := img.(*image.NRGBA).NRGBAAt(0, 0).A; a != 0 {
		t.Errorf("masked pixel alpha = %d, want 0", a)
	}
	if a := img.(*image.NRGBA).NRGBAAt(1, 0).A; a != 0xFF {
		t.Errorf("unmasked pixel alpha = %d, want 255 -- an all-zero alpha channel means 'not filled in', not 'invisible'", a)
	}
}

func TestDecodeDIBPalettized(t *testing.T) {
	palette := []color.RGBA{
		{R: 0x11, G: 0x22, B: 0x33, A: 0xFF},
		{R: 0xAA, G: 0xBB, B: 0xCC, A: 0xFF},
	}
	// 4bpp, 2 pixels per row: one byte of pixel data, padded to 4.
	// Bottom row indices 1,0; top row 0,1.
	pixels := []byte{0x10, 0, 0, 0, 0x01, 0, 0, 0}
	img, err := decodeDIB(dib(2, 2, 4, palette, pixels, make([]byte, 8)))
	if err != nil {
		t.Fatal(err)
	}
	if got := img.At(0, 0); got != color.NRGBA(palette[0]) {
		t.Errorf("top-left = %v, want palette[0] %v", got, palette[0])
	}
	if got := img.At(1, 0); got != color.NRGBA(palette[1]) {
		t.Errorf("top-right = %v, want palette[1] %v", got, palette[1])
	}
}

func TestDecodeDIBRejectsGarbage(t *testing.T) {
	for name, b := range map[string][]byte{
		"too short":      {1, 2, 3},
		"zero width":     dib(0, 2, 32, nil, nil, nil),
		"truncated data": dib(16, 16, 32, nil, []byte{1, 2, 3}, nil),
	} {
		if _, err := decodeDIB(b); err == nil {
			t.Errorf("%s: decoded without error", name)
		}
	}
}

func TestDecodeDIBRejectsHostilePaletteSize(t *testing.T) {
	// biClrUsed comes out of a downloaded installer. Read as a length without
	// a bound, 0xFFFFFFFF is either an enormous allocation or, once cast,
	// a negative one.
	b := dib(2, 2, 8, nil, make([]byte, 16), make([]byte, 8))
	for _, clrUsed := range []uint32{0xFFFFFFFF, 0x7FFFFFFF, 1 << 20} {
		binary.LittleEndian.PutUint32(b[32:], clrUsed)
		if _, err := decodeDIB(b); err == nil {
			t.Errorf("biClrUsed=%#x decoded without error", clrUsed)
		}
	}
}

func TestDecodeICORejectsHostileOffsets(t *testing.T) {
	blue := color.RGBA{B: 0xFF, A: 0xFF}
	good := dib(2, 2, 32, nil, bgra32([4]color.RGBA{blue, blue, blue, blue}), make([]byte, 8))
	b := ico(good)
	// An entry whose size runs off the end of the file must be skipped, not
	// read past.
	binary.LittleEndian.PutUint32(b[6+8:], 0xFFFFFFFF)
	if got := len(decodeICO(b)); got != 0 {
		t.Errorf("decoded %d images from an out-of-range entry", got)
	}
}

func TestDecodeICOAcceptsPNGEntries(t *testing.T) {
	// Since Vista the 256x256 entry is usually a whole PNG rather than a DIB.
	src := image.NewNRGBA(image.Rect(0, 0, 4, 4))
	src.Set(0, 0, color.NRGBA{R: 0xFF, A: 0xFF})
	var pngBuf bytes.Buffer
	if err := png.Encode(&pngBuf, src); err != nil {
		t.Fatal(err)
	}

	blue := color.RGBA{B: 0xFF, A: 0xFF}
	small := dib(2, 2, 32, nil, bgra32([4]color.RGBA{blue, blue, blue, blue}), make([]byte, 8))

	images := decodeICO(ico(small, pngBuf.Bytes()))
	if len(images) != 2 {
		t.Fatalf("decoded %d images, want 2", len(images))
	}
	sizes := map[int]bool{images[0].Bounds().Dx(): true, images[1].Bounds().Dx(): true}
	if !sizes[2] || !sizes[4] {
		t.Errorf("sizes = %v, want a 2px DIB and a 4px PNG", sizes)
	}
}

func TestDecodeICOSkipsUnreadableEntries(t *testing.T) {
	blue := color.RGBA{B: 0xFF, A: 0xFF}
	good := dib(2, 2, 32, nil, bgra32([4]color.RGBA{blue, blue, blue, blue}), make([]byte, 8))
	// One broken size must not cost the file its working ones.
	if got := len(decodeICO(ico([]byte{0xDE, 0xAD}, good))); got != 1 {
		t.Errorf("decoded %d images, want 1", got)
	}
}

func TestParseIconLocation(t *testing.T) {
	cases := []struct {
		in    string
		path  string
		index int
	}{
		{`C:\app\app.exe,0`, `C:\app\app.exe`, 0},
		{`C:\app\app.exe,-101`, `C:\app\app.exe`, -101},
		{`C:\app\app.exe`, `C:\app\app.exe`, 0},
		{`C:\app\shell32.dll, 3`, `C:\app\shell32.dll`, 3},
		// A comma is a legal character in a filename; only a trailing number
		// after one is an index.
		{`C:\Photos, Music\icon.ico`, `C:\Photos, Music\icon.ico`, 0},
	}
	for _, c := range cases {
		path, index := ParseIconLocation(c.in)
		if path != c.path || index != c.index {
			t.Errorf("ParseIconLocation(%q) = %q, %d; want %q, %d", c.in, path, index, c.path, c.index)
		}
	}
}

func TestWriteIconsSkipsNonThemeSizes(t *testing.T) {
	dir := t.TempDir()
	images := []image.Image{
		image.NewNRGBA(image.Rect(0, 0, 48, 48)),
		image.NewNRGBA(image.Rect(0, 0, 20, 20)), // no hicolor directory
		image.NewNRGBA(image.Rect(0, 0, 16, 16)),
		image.NewNRGBA(image.Rect(0, 0, 16, 16)), // duplicate size
		image.NewNRGBA(image.Rect(0, 0, 32, 16)), // not square
	}
	files, sizes, err := WriteIcons(dir, "viniget-Demo", images)
	if err != nil {
		t.Fatal(err)
	}
	want := []int{16, 48}
	if len(sizes) != len(want) || sizes[0] != want[0] || sizes[1] != want[1] {
		t.Fatalf("sizes = %v, want %v", sizes, want)
	}
	for _, f := range files {
		if _, err := os.Stat(f); err != nil {
			t.Errorf("%v", err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "hicolor", "48x48", "apps", "viniget-Demo.png")); err != nil {
		t.Errorf("48x48 icon not written where the theme looks for it: %v", err)
	}
}

// --- PE resources ------------------------------------------------------

// buildPE assembles the smallest PE image that carries an icon: one .rsrc
// section holding an RT_GROUP_ICON directory that names a single RT_ICON.
//
// Worth the assembly. The resource tree is three levels of offsets relative to
// a section that is itself located through the section table, and every one of
// those indirections is a place to be off by a header.
func buildPE(t *testing.T, iconData []byte, groupID, iconID uint16) []byte {
	t.Helper()
	return buildPEGroup(t, iconData, groupID, "", iconID)
}

// buildPEGroup is buildPE with control over how the RT_GROUP_ICON entry is
// named. A non-empty name makes it a string-named entry -- IDI_ICON1 from
// windres, MAINICON from Delphi -- rather than a numbered one.
func buildPEGroup(t *testing.T, iconData []byte, groupID uint16, name string, iconID uint16) []byte {
	t.Helper()
	const (
		peOff      = 0x40
		optSize    = 112 + 16*8 // PE32+ with the full data directory
		sectionVA  = 0x1000
		sectionRaw = 0x400
	)

	// Resource section layout. Each directory is a 16-byte header followed by
	// 8-byte entries; each leaf is a 16-byte data entry.
	const (
		rootDir   = 0x00
		iconType  = 0x20
		iconName  = 0x40
		iconLeaf  = 0x60
		groupType = 0x80
		groupName = 0xA0
		groupLeaf = 0xC0
		groupData = 0xE0
		imageData = 0x100
	)

	// Sized to fit the image and, when there is one, the group's name string,
	// which is the whole reason the directory offsets above are fixed and the
	// data comes last.
	nameStr := imageData + len(iconData)
	total := nameStr
	if name != "" {
		total += 2 + 2*len([]rune(name))
	}
	rsrc := make([]byte, total)
	dir := func(off int, entries [][2]uint32) {
		binary.LittleEndian.PutUint16(rsrc[off+12:], 0)                    // named entries
		binary.LittleEndian.PutUint16(rsrc[off+14:], uint16(len(entries))) // id entries
		for i, e := range entries {
			p := off + 16 + i*8
			binary.LittleEndian.PutUint32(rsrc[p:], e[0])
			binary.LittleEndian.PutUint32(rsrc[p+4:], e[1])
		}
	}
	leaf := func(off int, dataOff, size int) {
		binary.LittleEndian.PutUint32(rsrc[off:], uint32(sectionVA+dataOff))
		binary.LittleEndian.PutUint32(rsrc[off+4:], uint32(size))
	}
	// dirNamed writes a directory whose one entry carries a string name rather
	// than an id, which is what NumberOfNamedEntries counts.
	dirNamed := func(off, nameOff int, sub uint32) {
		binary.LittleEndian.PutUint16(rsrc[off+12:], 1) // named entries
		binary.LittleEndian.PutUint16(rsrc[off+14:], 0) // id entries
		binary.LittleEndian.PutUint32(rsrc[off+16:], 0x80000000|uint32(nameOff))
		binary.LittleEndian.PutUint32(rsrc[off+20:], sub)
	}

	// Types must be sorted by id: RT_ICON (3) before RT_GROUP_ICON (14).
	dir(rootDir, [][2]uint32{
		{rtIcon, 0x80000000 | iconType},
		{rtGroupIcon, 0x80000000 | groupType},
	})
	dir(iconType, [][2]uint32{{uint32(iconID), 0x80000000 | iconName}})
	dir(iconName, [][2]uint32{{1033, iconLeaf}}) // language: en-US
	leaf(iconLeaf, imageData, len(iconData))

	if name == "" {
		dir(groupType, [][2]uint32{{uint32(groupID), 0x80000000 | groupName}})
	} else {
		dirNamed(groupType, nameStr, 0x80000000|groupName)
		binary.LittleEndian.PutUint16(rsrc[nameStr:], uint16(len([]rune(name))))
		for i, r := range []rune(name) {
			binary.LittleEndian.PutUint16(rsrc[nameStr+2+i*2:], uint16(r))
		}
	}
	dir(groupName, [][2]uint32{{1033, groupLeaf}})
	leaf(groupLeaf, groupData, 6+14)

	// GRPICONDIR: a header and one 14-byte entry ending in the RT_ICON id.
	binary.LittleEndian.PutUint16(rsrc[groupData+2:], 1) // type: icon
	binary.LittleEndian.PutUint16(rsrc[groupData+4:], 1) // count
	binary.LittleEndian.PutUint16(rsrc[groupData+6+12:], iconID)
	copy(rsrc[imageData:], iconData)

	out := make([]byte, sectionRaw)
	out[0], out[1] = 'M', 'Z'
	binary.LittleEndian.PutUint32(out[0x3C:], peOff)
	copy(out[peOff:], "PE\x00\x00")
	binary.LittleEndian.PutUint16(out[peOff+4:], 0x8664) // machine
	binary.LittleEndian.PutUint16(out[peOff+6:], 1)      // one section
	binary.LittleEndian.PutUint16(out[peOff+20:], optSize)

	opt := peOff + 24
	binary.LittleEndian.PutUint16(out[opt:], 0x20B) // PE32+
	dd := opt + 112
	binary.LittleEndian.PutUint32(out[dd+2*8:], sectionVA) // resource RVA
	binary.LittleEndian.PutUint32(out[dd+2*8+4:], uint32(len(rsrc)))

	sec := opt + optSize
	copy(out[sec:], ".rsrc\x00\x00\x00")
	binary.LittleEndian.PutUint32(out[sec+8:], uint32(len(rsrc)))  // virtual size
	binary.LittleEndian.PutUint32(out[sec+12:], sectionVA)         // virtual address
	binary.LittleEndian.PutUint32(out[sec+16:], uint32(len(rsrc))) // raw size
	binary.LittleEndian.PutUint32(out[sec+20:], sectionRaw)        // raw offset

	return append(out, rsrc...)
}

// solidIconDIB builds a square 32-bit icon of one colour, at a size the
// hicolor theme has a directory for.
func solidIconDIB(size int, c color.RGBA) []byte {
	stride := dibStride(size, 32)
	pixels := make([]byte, stride*size)
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			p := y*stride + x*4
			pixels[p], pixels[p+1], pixels[p+2], pixels[p+3] = c.B, c.G, c.R, c.A
		}
	}
	return dib(size, size, 32, nil, pixels, make([]byte, dibStride(size, 1)*size))
}

func demoIconDIB() []byte { return solidIconDIB(48, color.RGBA{G: 0xFF, A: 0xFF}) }

func TestPEIcons(t *testing.T) {
	exe := buildPE(t, demoIconDIB(), 1, 7)
	images, err := peIcons(exe, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(images) != 1 {
		t.Fatalf("got %d images, want 1", len(images))
	}
	if got := images[0].At(0, 0); got != (color.NRGBA{G: 0xFF, A: 0xFF}) {
		t.Errorf("pixel = %v, want green", got)
	}
}

func TestPEIconsByNegativeIndex(t *testing.T) {
	// A negative index names a resource id outright, which is how a shortcut
	// pins one specific icon out of a file full of them.
	exe := buildPE(t, demoIconDIB(), 42, 7)
	if _, err := peIcons(exe, -42); err != nil {
		t.Errorf("peIcons by resource id: %v", err)
	}
	if _, err := peIcons(exe, -99); err != nil {
		t.Errorf("a resource id that does not exist should fall back to the first group, got: %v", err)
	}
}

// TestPEIconsNamedGroupIcon covers the executables whose group icon carries a
// string name instead of an id. Skipping those reported "no icon resources"
// for an executable shipping eight perfectly good sizes, because a resource
// directory stores its named entries ahead of its numbered ones and the walk
// started past them.
func TestPEIconsNamedGroupIcon(t *testing.T) {
	for _, name := range []string{"IDI_ICON1", "MAINICON"} {
		exe := buildPEGroup(t, demoIconDIB(), 0, name, 7)
		images, err := peIcons(exe, 0)
		if err != nil {
			t.Fatalf("peIcons on a group named %s: %v", name, err)
		}
		if len(images) != 1 || images[0].Bounds().Dx() != 48 {
			t.Fatalf("group named %s: got %d images, want one 48px", name, len(images))
		}
	}
}

func TestPEIconsRejectsNonPE(t *testing.T) {
	if _, err := peIcons([]byte("not an executable at all"), 0); err == nil {
		t.Error("parsed a non-PE without error")
	}
	if _, err := peIcons(append([]byte("MZ"), make([]byte, 200)...), 0); err == nil {
		t.Error("parsed an MZ with no PE header without error")
	}
}

func TestIconImagesEndToEnd(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "app.exe")
	if err := os.WriteFile(exe, buildPE(t, demoIconDIB(), 1, 1), 0o755); err != nil {
		t.Fatal(err)
	}
	images, err := IconImages(IconSource{Path: exe})
	if err != nil {
		t.Fatal(err)
	}
	if len(images) != 1 || images[0].Bounds().Dx() != 48 {
		t.Fatalf("images = %v", images)
	}

	icoPath := filepath.Join(dir, "app.ico")
	if err := os.WriteFile(icoPath, ico(demoIconDIB()), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := IconImages(IconSource{Path: icoPath}); err != nil {
		t.Errorf("IconImages on a .ico: %v", err)
	}
}

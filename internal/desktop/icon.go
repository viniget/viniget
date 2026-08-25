package desktop

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Icons are read straight out of the payload the installer already wrote:
// either a .ico file, or the RT_GROUP_ICON/RT_ICON resources inside the
// executable itself. Nothing is fetched, nothing is rendered by Wine, and no
// external converter is required -- which is the point, because the icon has
// to work on a machine that has neither icoutils nor ImageMagick.
//
// References:
//   PE/COFF   https://learn.microsoft.com/windows/win32/debug/pe-format
//   ICO/DIB   https://learn.microsoft.com/previous-versions/ms997538(v=msdn.10)

// standardIconSizes are the hicolor theme's own directories. An icon at any
// other size has nowhere to go in the theme -- lookups only ever search the
// directories the theme declares -- so odd sizes are dropped and the caller
// falls back to an absolute Icon= path.
var standardIconSizes = map[int]bool{
	16: true, 22: true, 24: true, 32: true, 36: true, 48: true, 64: true,
	72: true, 96: true, 128: true, 160: true, 192: true, 256: true, 512: true,
}

var errNoIcons = errors.New("no icon resources")

// IconSource names where an icon should be read from: a file, and which icon
// within it.
type IconSource struct {
	// Path is the host path of the .ico, .exe or .dll holding the icon.
	Path string
	// Index selects an icon within a multi-icon file. A non-negative value is
	// positional; a negative value is the negated resource ID, which is how
	// Windows lets a shortcut pin one specific icon.
	Index int
}

// ParseIconLocation splits a shortcut's IconLocation into a path and an index.
//
// The trailing ",N" is optional and only counts as an index when it actually
// parses as one: a file called "Photos, Music.ico" is a path, not an index.
func ParseIconLocation(s string) (path string, index int) {
	s = strings.TrimSpace(s)
	if i := strings.LastIndex(s, ","); i >= 0 {
		if n, err := strconv.Atoi(strings.TrimSpace(s[i+1:])); err == nil {
			return strings.TrimSpace(s[:i]), n
		}
	}
	return s, 0
}

// IconImages extracts every size an icon source carries, largest first.
func IconImages(src IconSource) ([]image.Image, error) {
	buf, err := os.ReadFile(src.Path)
	if err != nil {
		return nil, err
	}
	var images []image.Image
	if isICO(buf) {
		images = decodeICO(buf)
	} else {
		images, err = peIcons(buf, src.Index)
		if err != nil {
			return nil, err
		}
	}
	if len(images) == 0 {
		return nil, errNoIcons
	}
	sort.SliceStable(images, func(i, j int) bool {
		return images[i].Bounds().Dx() > images[j].Bounds().Dx()
	})
	return images, nil
}

// WriteIcons installs an icon into the hicolor theme under name, returning the
// files written.
//
// Every size the source carries is written, not just the largest: the icon
// theme picks per context, and a 256x256 scaled down to a 22px panel is
// visibly worse than the 22px art the vendor already shipped in the same file.
func WriteIcons(iconsDir, name string, images []image.Image) ([]string, []int, error) {
	var (
		written []string
		sizes   []int
	)
	seen := map[int]bool{}
	for _, img := range images {
		b := img.Bounds()
		size := b.Dx()
		if b.Dx() != b.Dy() || !standardIconSizes[size] || seen[size] {
			continue
		}
		seen[size] = true
		dir := filepath.Join(iconsDir, "hicolor", fmt.Sprintf("%dx%d", size, size), "apps")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return written, sizes, err
		}
		path := filepath.Join(dir, name+".png")
		if err := writePNG(path, img); err != nil {
			return written, sizes, err
		}
		written = append(written, path)
		sizes = append(sizes, size)
	}
	sort.Ints(sizes)
	return written, sizes, nil
}

// WriteIconFile writes a single icon outside the theme, for the sizes the
// theme has no directory for. A .desktop file may name an icon by absolute
// path, which is exactly what this is for.
func WriteIconFile(dir, name string, img image.Image) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, name+".png")
	return path, writePNG(path, img)
}

func writePNG(path string, img image.Image) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if err := png.Encode(f, img); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// --- ICO ---------------------------------------------------------------

func isICO(b []byte) bool {
	return len(b) >= 6 && binary.LittleEndian.Uint16(b) == 0 && binary.LittleEndian.Uint16(b[2:]) == 1
}

// decodeICO reads every image out of an .ico. Entries that will not decode are
// skipped rather than failing the file: a single malformed size is common and
// costs nothing as long as one other size survives.
func decodeICO(b []byte) []image.Image {
	count := int(binary.LittleEndian.Uint16(b[4:]))
	var out []image.Image
	for i := 0; i < count; i++ {
		e := 6 + i*16
		if e+16 > len(b) {
			break
		}
		size := int(binary.LittleEndian.Uint32(b[e+8:]))
		off := int(binary.LittleEndian.Uint32(b[e+12:]))
		// Written as a subtraction so a size near the integer limit cannot
		// wrap the comparison it is supposed to fail.
		if off < 0 || size <= 0 || off > len(b) || size > len(b)-off {
			continue
		}
		if img, err := decodeIconImage(b[off : off+size]); err == nil {
			out = append(out, img)
		}
	}
	return out
}

// decodeIconImage decodes one icon image, which since Vista may be a whole PNG
// rather than a DIB. The 256x256 size is almost always the PNG one, because a
// 256x256 32-bit DIB is a quarter of a megabyte.
func decodeIconImage(b []byte) (image.Image, error) {
	if len(b) > 8 && string(b[:8]) == "\x89PNG\r\n\x1a\n" {
		return png.Decode(bytes.NewReader(b))
	}
	return decodeDIB(b)
}

// decodeDIB decodes the BITMAPINFOHEADER form of an icon image.
//
// An icon DIB is two stacked bitmaps: the colour image, and a 1-bit AND mask
// below it. biHeight covers both, so the real height is half of what the
// header says, and both are stored bottom-up.
func decodeDIB(b []byte) (image.Image, error) {
	if len(b) < 40 {
		return nil, errors.New("icon: DIB shorter than its header")
	}
	headerSize := int(binary.LittleEndian.Uint32(b))
	width := int(int32(binary.LittleEndian.Uint32(b[4:])))
	stored := int(int32(binary.LittleEndian.Uint32(b[8:])))
	bits := int(binary.LittleEndian.Uint16(b[14:]))
	compression := binary.LittleEndian.Uint32(b[16:])
	clrUsed := int(binary.LittleEndian.Uint32(b[32:]))

	if headerSize < 40 || headerSize > len(b) || width <= 0 || stored <= 0 {
		return nil, errors.New("icon: unusable DIB header")
	}
	// BI_RGB and BI_BITFIELDS are the only compressions an icon uses, and for
	// the bit depths that appear in icons the two are laid out identically.
	if compression != 0 && compression != 3 {
		return nil, fmt.Errorf("icon: DIB compression %d is not supported", compression)
	}
	if width > 1024 || stored > 2048 {
		return nil, errors.New("icon: implausible DIB dimensions")
	}

	entries := clrUsed
	if entries == 0 && bits <= 8 {
		entries = 1 << bits
	}
	// An icon palette is at most 256 entries. Bounded rather than trusted
	// because every number in this header comes out of a downloaded
	// installer, and biClrUsed is one cast away from a negative length.
	if entries < 0 || entries > 256 {
		return nil, errors.New("icon: implausible DIB palette size")
	}
	off := headerSize + entries*4
	if off > len(b) {
		return nil, errors.New("icon: DIB palette runs past the data")
	}
	palette := make([]color.RGBA, entries)
	for i := range palette {
		p := headerSize + i*4
		palette[i] = color.RGBA{R: b[p+2], G: b[p+1], B: b[p], A: 0xFF}
	}
	data := b[off:]

	// Prefer the stacked reading, and fall back to a mask-less bitmap only
	// when the data is too short to hold a mask.
	height := stored / 2
	xorStride := dibStride(width, bits)
	andStride := dibStride(width, 1)
	if height == 0 || xorStride*height+andStride*height > len(data) {
		height, andStride = stored, 0
	}
	if height <= 0 || xorStride*height > len(data) {
		return nil, errors.New("icon: DIB pixel data is truncated")
	}
	andData := data[xorStride*height:]

	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		row := data[(height-1-y)*xorStride:] // bottom-up
		for x := 0; x < width; x++ {
			c, err := dibPixel(row, x, bits, palette)
			if err != nil {
				return nil, err
			}
			img.SetNRGBA(x, y, color.NRGBA(c))
		}
	}

	// A 32-bit icon whose alpha channel is entirely zero is not invisible, it
	// is one of the many that never filled the channel in. Those carry their
	// transparency in the AND mask like every lower bit depth does, so the
	// channel is filled in opaque and the mask is believed instead.
	maskDecides := bits != 32
	if bits == 32 && !hasAlpha(img) {
		setOpaque(img)
		maskDecides = true
	}
	if andStride > 0 && maskDecides {
		applyANDMask(img, andData, andStride)
	}
	return img, nil
}

func dibStride(width, bits int) int { return ((width*bits + 31) / 32) * 4 }

func dibPixel(row []byte, x, bits int, palette []color.RGBA) (color.RGBA, error) {
	index := func(i int) (color.RGBA, error) {
		if i >= len(palette) {
			return color.RGBA{}, errors.New("icon: DIB palette index out of range")
		}
		return palette[i], nil
	}
	switch bits {
	case 32:
		if (x+1)*4 > len(row) {
			return color.RGBA{}, errors.New("icon: DIB row is short")
		}
		p := row[x*4:]
		return color.RGBA{R: p[2], G: p[1], B: p[0], A: p[3]}, nil
	case 24:
		if (x+1)*3 > len(row) {
			return color.RGBA{}, errors.New("icon: DIB row is short")
		}
		p := row[x*3:]
		return color.RGBA{R: p[2], G: p[1], B: p[0], A: 0xFF}, nil
	case 16:
		if (x+1)*2 > len(row) {
			return color.RGBA{}, errors.New("icon: DIB row is short")
		}
		v := binary.LittleEndian.Uint16(row[x*2:])
		// X1R5G5B5: the 5-bit channels are widened by repeating their high
		// bits, so full-scale stays full-scale.
		r5, g5, b5 := (v>>10)&0x1F, (v>>5)&0x1F, v&0x1F
		return color.RGBA{R: uint8(r5<<3 | r5>>2), G: uint8(g5<<3 | g5>>2), B: uint8(b5<<3 | b5>>2), A: 0xFF}, nil
	case 8:
		if x >= len(row) {
			return color.RGBA{}, errors.New("icon: DIB row is short")
		}
		return index(int(row[x]))
	case 4:
		if x/2 >= len(row) {
			return color.RGBA{}, errors.New("icon: DIB row is short")
		}
		b := row[x/2]
		if x%2 == 0 {
			return index(int(b >> 4))
		}
		return index(int(b & 0x0F))
	case 1:
		if x/8 >= len(row) {
			return color.RGBA{}, errors.New("icon: DIB row is short")
		}
		return index(int(row[x/8]>>(7-uint(x%8))) & 1)
	}
	return color.RGBA{}, fmt.Errorf("icon: %d-bit DIB is not supported", bits)
}

// applyANDMask makes the pixels the mask marks as transparent transparent. The
// mask is 1 bit per pixel, bottom-up, with 1 meaning "let the desktop through".
func applyANDMask(img *image.NRGBA, mask []byte, stride int) {
	h := img.Bounds().Dy()
	w := img.Bounds().Dx()
	for y := 0; y < h; y++ {
		start := (h - 1 - y) * stride
		if start+stride > len(mask) {
			return
		}
		row := mask[start:]
		for x := 0; x < w; x++ {
			if row[x/8]>>(7-uint(x%8))&1 == 1 {
				c := img.NRGBAAt(x, y)
				c.A = 0
				img.SetNRGBA(x, y, c)
			}
		}
	}
}

func hasAlpha(img *image.NRGBA) bool {
	for i := 3; i < len(img.Pix); i += 4 {
		if img.Pix[i] != 0 {
			return true
		}
	}
	return false
}

func setOpaque(img *image.NRGBA) {
	for i := 3; i < len(img.Pix); i += 4 {
		img.Pix[i] = 0xFF
	}
}

// --- PE resources ------------------------------------------------------

const (
	rtIcon      = 3
	rtGroupIcon = 14
)

// peIcons reconstructs .ico images from an executable's icon resources.
//
// An icon in a PE file is not stored whole: RT_GROUP_ICON holds a directory of
// sizes, each naming an RT_ICON resource that holds one image. Reassembling
// them is the whole job.
func peIcons(buf []byte, index int) ([]image.Image, error) {
	p, err := openPE(buf)
	if err != nil {
		return nil, err
	}
	groups := p.entries(0, rtGroupIcon)
	if len(groups) == 0 {
		return nil, errNoIcons
	}

	// A negative index names a resource ID outright; a non-negative one counts
	// from the front of the resource list. The resource directory is already
	// in that order -- names first, then ids, each sorted -- so nothing needs
	// sorting here. A named group has no id for the negative form to match.
	group := groups[0]
	if index < 0 {
		id := uint32(-index)
		for _, g := range groups {
			if !g.named && g.id == id {
				group = g
				break
			}
		}
	} else if index < len(groups) {
		group = groups[index]
	}

	dir, ok := p.leaf(group)
	if !ok || len(dir) < 6 {
		return nil, errNoIcons
	}
	count := int(binary.LittleEndian.Uint16(dir[4:]))

	icons := p.entries(0, rtIcon)
	byID := make(map[uint32]resEntry, len(icons))
	for _, e := range icons {
		// A group directory names its images by integer id, so a named
		// RT_ICON is unreachable from one and must not shadow a real id.
		if e.named {
			continue
		}
		byID[e.id] = e
	}

	var out []image.Image
	for i := 0; i < count; i++ {
		e := 6 + i*14
		if e+14 > len(dir) {
			break
		}
		id := uint32(binary.LittleEndian.Uint16(dir[e+12:]))
		entry, ok := byID[id]
		if !ok {
			continue
		}
		data, ok := p.leaf(entry)
		if !ok {
			continue
		}
		if img, err := decodeIconImage(data); err == nil {
			out = append(out, img)
		}
	}
	if len(out) == 0 {
		return nil, errNoIcons
	}
	return out, nil
}

// resEntry is one entry in a resource directory: either a subdirectory or a
// leaf, with an offset relative to the resource section.
type resEntry struct {
	id     uint32
	offset uint32
	isDir  bool
	// named marks an entry carrying a string name rather than an integer id,
	// which leaves id meaningless -- it holds the name's offset instead.
	named bool
}

type peFile struct {
	buf      []byte
	sections []peSection
	resBase  int
	resSize  int
}

type peSection struct {
	virtualAddress uint32
	virtualSize    uint32
	rawSize        uint32
	rawOffset      uint32
}

func openPE(buf []byte) (*peFile, error) {
	if len(buf) < 0x40 || buf[0] != 'M' || buf[1] != 'Z' {
		return nil, errors.New("icon: not a PE image")
	}
	pe := int(binary.LittleEndian.Uint32(buf[0x3C:]))
	if pe < 0 || pe+24 > len(buf) || string(buf[pe:pe+4]) != "PE\x00\x00" {
		return nil, errors.New("icon: not a PE image")
	}
	sectionCount := int(binary.LittleEndian.Uint16(buf[pe+6:]))
	optSize := int(binary.LittleEndian.Uint16(buf[pe+20:]))
	opt := pe + 24
	if opt+optSize > len(buf) || opt+2 > len(buf) {
		return nil, errors.New("icon: PE optional header is truncated")
	}

	// The data directories sit after the optional header's fixed part, which
	// is the only place PE32 and PE32+ differ for our purposes.
	var dirOffset int
	switch binary.LittleEndian.Uint16(buf[opt:]) {
	case 0x10B:
		dirOffset = opt + 96
	case 0x20B:
		dirOffset = opt + 112
	default:
		return nil, errors.New("icon: unrecognised PE optional header")
	}
	if dirOffset+3*8 > len(buf) {
		return nil, errors.New("icon: PE data directories are truncated")
	}
	resRVA := binary.LittleEndian.Uint32(buf[dirOffset+2*8:])
	resSize := binary.LittleEndian.Uint32(buf[dirOffset+2*8+4:])
	if resRVA == 0 || resSize == 0 {
		return nil, errNoIcons
	}

	p := &peFile{buf: buf, resSize: int(resSize)}
	secStart := opt + optSize
	for i := 0; i < sectionCount; i++ {
		s := secStart + i*40
		if s+40 > len(buf) {
			break
		}
		p.sections = append(p.sections, peSection{
			virtualSize:    binary.LittleEndian.Uint32(buf[s+8:]),
			virtualAddress: binary.LittleEndian.Uint32(buf[s+12:]),
			rawSize:        binary.LittleEndian.Uint32(buf[s+16:]),
			rawOffset:      binary.LittleEndian.Uint32(buf[s+20:]),
		})
	}

	base, ok := p.offsetOf(resRVA)
	if !ok {
		return nil, errNoIcons
	}
	p.resBase = base
	return p, nil
}

// offsetOf maps a virtual address to a file offset via the section table.
func (p *peFile) offsetOf(rva uint32) (int, bool) {
	for _, s := range p.sections {
		size := s.virtualSize
		if s.rawSize > size {
			size = s.rawSize
		}
		if rva >= s.virtualAddress && rva < s.virtualAddress+size {
			off := int(rva-s.virtualAddress) + int(s.rawOffset)
			if off >= 0 && off < len(p.buf) {
				return off, true
			}
			return 0, false
		}
	}
	return 0, false
}

// entries lists a resource directory's entries and, when want is non-zero,
// only that resource type.
//
// A directory stores its string-named entries first and its numbered ones
// after. Both are wanted below the type directory: a group icon called
// MAINICON -- which is what Delphi emits -- or IDI_ICON1, which is what
// windres defaults to, is as ordinary as a numbered one, and skipping it
// loses the only icon the executable has. Resource *types* are the one level
// where a name is impossible, RT_ICON and RT_GROUP_ICON being numbers by
// definition, so the search for a type starts past the names instead.
func (p *peFile) entries(dirOffset int, want uint32) []resEntry {
	base := p.resBase + dirOffset
	if base+16 > len(p.buf) {
		return nil
	}
	named := int(binary.LittleEndian.Uint16(p.buf[base+12:]))
	ids := int(binary.LittleEndian.Uint16(p.buf[base+14:]))
	var out []resEntry

	first := 0
	if want != 0 {
		first = named
	}
	for i := first; i < named+ids; i++ {
		e := base + 16 + i*8
		if e+8 > len(p.buf) {
			break
		}
		id := binary.LittleEndian.Uint32(p.buf[e:])
		off := binary.LittleEndian.Uint32(p.buf[e+4:])
		entry := resEntry{
			id:     id &^ 0x80000000,
			offset: off &^ 0x80000000,
			isDir:  off&0x80000000 != 0,
			named:  id&0x80000000 != 0,
		}
		if want == 0 {
			out = append(out, entry)
			continue
		}
		if entry.id != want || !entry.isDir {
			continue
		}
		// One level down is the per-resource directory: its entries are the
		// individual icons, each of which is itself a directory of languages.
		return p.entries(int(entry.offset), 0)
	}
	if want != 0 {
		return nil
	}
	return out
}

// leaf follows an entry down to its data, taking the first language when the
// resource is localised. Icons are not translated in practice, and picking the
// first is what a resource loader does when no language matches.
func (p *peFile) leaf(e resEntry) ([]byte, bool) {
	off := e.offset
	for depth := 0; e.isDir && depth < 4; depth++ {
		sub := p.entries(int(off), 0)
		if len(sub) == 0 {
			return nil, false
		}
		e, off = sub[0], sub[0].offset
	}
	if e.isDir {
		return nil, false
	}
	data := p.resBase + int(off)
	if data+16 > len(p.buf) {
		return nil, false
	}
	rva := binary.LittleEndian.Uint32(p.buf[data:])
	size := int(binary.LittleEndian.Uint32(p.buf[data+4:]))
	start, ok := p.offsetOf(rva)
	if !ok || size <= 0 || start > len(p.buf) || size > len(p.buf)-start {
		return nil, false
	}
	return p.buf[start : start+size], true
}

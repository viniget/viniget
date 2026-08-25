"""Minimal MS-SHLLINK (.lnk) parser.

Why this exists: every one of the six packages validated during design dropped
a Start Menu shortcut naming its real entry point. Name-matching heuristics do
not -- they picked `WinMerge32BitPluginProxy.exe` over `WinMergeU.exe` and
reported a working app as broken.

This is the harness's copy, and answers one question: what do I launch to
probe this package? The shipping client carries its own implementation in Go
(internal/desktop/lnk.go), which the generated .desktop files come from.

Reference: [MS-SHLLINK] https://learn.microsoft.com/openspecs/windows_protocols/ms-shllink/
"""

import os
import struct
from dataclasses import dataclass, field

_MAGIC = 0x0000004C
_CLSID = b"\x01\x14\x02\x00\x00\x00\x00\x00\xc0\x00\x00\x00\x00\x00\x00\x46"

# LinkFlags bits we care about (MS-SHLLINK 2.1.1)
_HAS_TARGET_IDLIST = 1 << 0
_HAS_LINK_INFO = 1 << 1
_HAS_NAME = 1 << 2
_HAS_RELATIVE_PATH = 1 << 3
_HAS_WORKING_DIR = 1 << 4
_HAS_ARGUMENTS = 1 << 5
_HAS_ICON_LOCATION = 1 << 6
_IS_UNICODE = 1 << 7


@dataclass
class Shortcut:
    path: str
    target: str | None = None
    relative_path: str | None = None
    working_dir: str | None = None
    arguments: str | None = None
    icon_location: str | None = None
    name: str | None = None

    @property
    def basename(self) -> str:
        return os.path.splitext(os.path.basename(self.path))[0]

    def resolve(self, prefix: str) -> str | None:
        """Map the Windows target to a host path inside `prefix`."""
        win = self.target
        if not win and self.relative_path:
            base = os.path.dirname(self.path)
            win = os.path.normpath(os.path.join(base, self.relative_path))
            if os.path.exists(win):
                return win
            return None
        if not win:
            return None
        drive, _, rest = win.partition(":")
        if len(drive) != 1:
            return None
        host = os.path.join(prefix, f"drive_{drive.lower()}", rest.lstrip("\\/").replace("\\", "/"))
        return host if os.path.exists(host) else _ci_lookup(host)


def _ci_lookup(path: str) -> str | None:
    """Wine prefixes are case-sensitive on disk; .lnk targets are not."""
    parts = path.split(os.sep)
    cur = os.sep if path.startswith(os.sep) else ""
    for part in parts:
        if not part:
            continue
        nxt = os.path.join(cur, part)
        if os.path.exists(nxt):
            cur = nxt
            continue
        try:
            entries = os.listdir(cur or "/")
        except OSError:
            return None
        match = next((e for e in entries if e.lower() == part.lower()), None)
        if match is None:
            return None
        cur = os.path.join(cur, match)
    return cur or None


def _read_string(buf: bytes, off: int, unicode_: bool) -> tuple[str, int]:
    (count,) = struct.unpack_from("<H", buf, off)
    off += 2
    if unicode_:
        raw = buf[off : off + count * 2]
        return raw.decode("utf-16-le", "replace"), off + count * 2
    raw = buf[off : off + count]
    return raw.decode("mbcs" if os.name == "nt" else "cp1252", "replace"), off + count


def parse(path: str) -> Shortcut | None:
    """Parse a .lnk. Returns None if the file is not a shell link."""
    try:
        with open(path, "rb") as fh:
            buf = fh.read()
    except OSError:
        return None
    if len(buf) < 0x4C:
        return None
    header_size, = struct.unpack_from("<I", buf, 0)
    if header_size != _MAGIC or buf[4:20] != _CLSID:
        return None

    (flags,) = struct.unpack_from("<I", buf, 20)
    unicode_ = bool(flags & _IS_UNICODE)
    sc = Shortcut(path=path)
    off = 0x4C

    if flags & _HAS_TARGET_IDLIST:
        (idlist_size,) = struct.unpack_from("<H", buf, off)
        off += 2 + idlist_size

    if flags & _HAS_LINK_INFO:
        sc.target = _parse_link_info(buf, off)
        (info_size,) = struct.unpack_from("<I", buf, off)
        off += info_size

    for bit, attr in (
        (_HAS_NAME, "name"),
        (_HAS_RELATIVE_PATH, "relative_path"),
        (_HAS_WORKING_DIR, "working_dir"),
        (_HAS_ARGUMENTS, "arguments"),
        (_HAS_ICON_LOCATION, "icon_location"),
    ):
        if flags & bit:
            try:
                value, off = _read_string(buf, off, unicode_)
            except struct.error:
                break
            setattr(sc, attr, value)
    return sc


def _parse_link_info(buf: bytes, start: int) -> str | None:
    """Extract LocalBasePath (+ CommonPathSuffix) from a LinkInfo block."""
    try:
        info_size, header_size, info_flags = struct.unpack_from("<III", buf, start)
    except struct.error:
        return None
    if not info_flags & 0x1:  # VolumeIDAndLocalBasePath
        return None

    base_off, = struct.unpack_from("<I", buf, start + 16)
    suffix_off, = struct.unpack_from("<I", buf, start + 24)
    enc = "cp1252"
    if header_size >= 0x24:
        u_base, = struct.unpack_from("<I", buf, start + 28)
        if u_base:
            base_off, enc = u_base, "utf-16-le"

    base = _cstr(buf, start + base_off, enc)
    suffix = _cstr(buf, start + suffix_off, "cp1252") if suffix_off else ""
    return (base + suffix) or None


def _cstr(buf: bytes, off: int, enc: str) -> str:
    if enc == "utf-16-le":
        end = off
        while end + 1 < len(buf) and buf[end : end + 2] != b"\x00\x00":
            end += 2
        return buf[off:end].decode(enc, "replace")
    end = buf.index(b"\x00", off) if b"\x00" in buf[off:] else len(buf)
    return buf[off:end].decode(enc, "replace")


# Start Menu locations an installer may write to, relative to the prefix root.
_START_MENU_GLOBS = (
    "drive_c/ProgramData/Microsoft/Windows/Start Menu",
    "drive_c/users/*/AppData/Roaming/Microsoft/Windows/Start Menu",
)

# Shortcuts that are never the app itself.
_NOISE = ("uninstall", "unins", "readme", "license", "help", "website",
          "homepage", "documentation", "user's guide", "what's new", "about")


def find_shortcuts(prefix: str) -> list[Shortcut]:
    """All app shortcuts an installer created, best candidate first."""
    import glob as _glob

    found: list[Shortcut] = []
    for pattern in _START_MENU_GLOBS:
        for root in _glob.glob(os.path.join(prefix, pattern)):
            for dirpath, _, files in os.walk(root):
                for f in files:
                    if not f.lower().endswith(".lnk"):
                        continue
                    if any(n in f.lower() for n in _NOISE):
                        continue
                    sc = parse(os.path.join(dirpath, f))
                    if sc:
                        found.append(sc)

    def rank(sc: Shortcut) -> tuple:
        resolved = sc.resolve(prefix)
        # A shortcut to a .txt or .chm is documentation, not the app.
        is_exe = bool(resolved and resolved.lower().endswith(".exe"))
        return (0 if is_exe else 1, 0 if resolved else 1, len(sc.basename))

    found.sort(key=rank)
    return found

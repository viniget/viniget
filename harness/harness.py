"""Install + launch probe. Produces one compat verdict per package.

Design notes earned by running this against real packages, not by reasoning
about it:

1. Exit code is not the success signal. WinMerge's Inno installer ran past a
   240s timeout having already written 337 files and 8 executables correctly.
   Score on the filesystem delta; treat the return code as a weak hint.

2. The launch target comes from the Start Menu .lnk, never from matching the
   package name against filenames on disk. See lnk.py.

3. Outcomes are three-way. Greenshot maps zero windows and is perfectly
   healthy -- it is a tray application. Collapsing to pass/fail writes off
   every background utility in the catalog.
"""

import hashlib
import json
import os
import shutil
import subprocess
import time
from dataclasses import dataclass, asdict, field

import lnk
from switches import (
    ARCHIVE_TYPES,
    SUPPORTED_TYPES,
    UNSUPPORTED_TYPES,
    is_supported,
    silent_args,
)

INSTALL_TIMEOUT = 240
PROBE_TIMEOUT = 25


@dataclass
class Verdict:
    package_id: str
    version: str = ""
    installer_type: str = ""
    # install stage
    install: str = "not-attempted"      # ok | timeout-but-installed | no-files | download-failed | unsupported
    install_seconds: float = 0.0
    install_rc: str = ""
    new_files: int = 0
    new_executables: int = 0
    # launch stage
    launch: str = "not-attempted"       # window-mapped | alive-headless | died | no-target
    launch_target: str = ""
    window_title: str = ""
    window_geometry: str = ""
    seconds_to_window: float | None = None
    notes: list[str] = field(default_factory=list)

    @property
    def works(self) -> bool:
        return self.launch in ("window-mapped", "alive-headless")


def pick_installer(manifest: dict) -> tuple[str, dict] | tuple[None, None]:
    """Wine-aware installer selection.

    Differs from winget's own logic by hard-skipping MSIX/APPX, which Wine
    cannot support at all.

    We deliberately do NOT prefer archive variants even though extracting is
    more reliable than executing an installer. The installer is what registers
    Start Menu shortcuts, file associations and icons -- the metadata that
    desktop integration and launch-target discovery both depend on. Extraction
    is the fallback when no supported installer exists, not the happy path.
    """
    top = (manifest.get("InstallerType") or "").lower()
    candidates = []
    for entry in manifest.get("Installers") or []:
        itype = (entry.get("InstallerType") or top or "").lower()
        if itype == "zip":
            itype = (entry.get("NestedInstallerType") or "zip").lower()
        entry_switches = entry.get("InstallerSwitches") or manifest.get("InstallerSwitches")
        if itype in UNSUPPORTED_TYPES or not is_supported(itype, entry_switches):
            continue
        arch = (entry.get("Architecture") or "").lower()
        if arch not in ("x64", "x86", "neutral"):
            continue
        score = 0
        score -= 4 if itype in ARCHIVE_TYPES else 0   # fallback only
        score += 2 if arch == "x64" else 1
        score += 1 if (entry.get("Scope") or manifest.get("Scope")) == "machine" else 0
        candidates.append((score, itype, entry))
    if not candidates:
        return None, None
    candidates.sort(key=lambda c: -c[0])
    return candidates[0][1], candidates[0][2]


def _run(cmd, env=None, timeout=120, cwd=None):
    merged = dict(os.environ)
    merged.update(env or {})
    try:
        proc = subprocess.run(
            cmd, env=merged, cwd=cwd, timeout=timeout,
            stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
        )
        return proc.returncode, proc.stdout.decode("utf-8", "replace")
    except subprocess.TimeoutExpired:
        return "TIMEOUT", ""


def clone_prefix(base: str, dest: str) -> None:
    """Reflink the base prefix. On btrfs/xfs this is ~0.003s and zero extents;
    elsewhere cp falls back to a real copy and it still works, just slower."""
    shutil.rmtree(dest, ignore_errors=True)
    os.makedirs(os.path.dirname(os.path.abspath(dest)), exist_ok=True)
    subprocess.run(["cp", "-a", "--reflink=auto", base, dest], check=True)


def _tree(prefix: str) -> set[str]:
    seen = set()
    for sub in ("drive_c/Program Files", "drive_c/Program Files (x86)", "drive_c/users"):
        for dirpath, _, files in os.walk(os.path.join(prefix, sub)):
            for f in files:
                seen.add(os.path.join(dirpath, f)[len(prefix):])
    return seen


def download(url: str, sha256: str | None, cache: str) -> tuple[str | None, str]:
    os.makedirs(cache, exist_ok=True)
    name = hashlib.sha256(url.encode()).hexdigest()[:16]
    ext = os.path.splitext(url.split("?")[0])[1] or ".bin"
    path = os.path.join(cache, name + ext)
    if not os.path.exists(path):
        rc, _ = _run(["curl", "-sfL", "--max-time", "300", "-o", path, url], timeout=320)
        if rc != 0:
            return None, f"download failed (rc={rc})"
    if sha256:
        digest = hashlib.sha256(open(path, "rb").read()).hexdigest().lower()
        if digest != sha256.lower():
            return None, "sha256 mismatch"
    return path, "verified"


def _archive_entry_point(entry: dict, target_dir: str) -> str | None:
    """Archive packages declare their executable in the manifest itself."""
    for nested in entry.get("NestedInstallerFiles") or []:
        rel = (nested.get("RelativeFilePath") or "").replace("\\", "/")
        if not rel:
            continue
        candidate = os.path.join(target_dir, rel)
        if os.path.exists(candidate):
            return candidate
        found = lnk._ci_lookup(candidate)
        if found:
            return found
    return None


def install(manifest: dict, prefix: str, base: str, cache: str, display: str = ":97") -> Verdict:
    pid = manifest.get("PackageIdentifier", "?")
    v = Verdict(package_id=pid, version=str(manifest.get("PackageVersion", "")))

    itype, entry = pick_installer(manifest)
    if not entry:
        v.installer_type = (manifest.get("InstallerType") or "unknown").lower()
        v.install = "unsupported"
        v.notes.append(f"no installable variant; v1 supports {sorted(SUPPORTED_TYPES)}")
        return v
    v.installer_type = itype

    payload, msg = download(entry["InstallerUrl"], entry.get("InstallerSha256"), cache)
    v.notes.append(msg)
    if not payload:
        v.install = "download-failed"
        return v

    clone_prefix(base, prefix)
    before = _tree(prefix)
    env = {"WINEPREFIX": prefix, "WINEDEBUG": "-all", "DISPLAY": display}
    switches = entry.get("InstallerSwitches") or manifest.get("InstallerSwitches") or {}

    if itype in ARCHIVE_TYPES:
        target = os.path.join(prefix, "drive_c", "Program Files", pid.split(".")[-1])
        os.makedirs(target, exist_ok=True)
        rc, _ = _run(["7z", "x", "-y", f"-o{target}", payload], timeout=180) \
            if payload.lower().endswith(".zip") else (0, "")
        if not payload.lower().endswith(".zip"):
            shutil.copy2(payload, target)
        v.launch_target = _archive_entry_point(entry, target) or ""
    elif itype in ("msi", "wix"):
        rc, _ = _run(["xvfb-run", "-a", "-s", "-screen 0 1280x1024x24",
                      "wine", "msiexec", "/i", payload, "/qn"],
                     env=env, timeout=INSTALL_TIMEOUT)
    else:
        args = silent_args(itype, switches)
        if args is None:
            v.install = "unsupported"
            return v
        v.notes.append("switches: " + (" ".join(args) or "(none)")
                       + (" [manifest]" if switches.get("Silent") else " [viniget default]"))
        rc, _ = _run(["xvfb-run", "-a", "-s", "-screen 0 1280x1024x24", "wine", payload] + args,
                     env=env, timeout=INSTALL_TIMEOUT)

    started = time.time()
    _run(["wineserver", "-w"], env=env, timeout=60)
    v.install_seconds = round(time.time() - started, 1)
    v.install_rc = str(rc)

    delta = sorted(_tree(prefix) - before)
    executables = [f for f in delta if f.lower().endswith(".exe")]
    v.new_files = len(delta)
    v.new_executables = len(executables)

    # Lesson 1: the filesystem, not the return code, decides.
    if executables:
        v.install = "timeout-but-installed" if rc == "TIMEOUT" else "ok"
    elif delta:
        v.install = "files-but-no-exe"
    else:
        v.install = "no-files"
    return v


def probe(v: Verdict, prefix: str, display: str = ":98") -> Verdict:
    """Launch the app and classify what happens. Three-way, per lesson 3."""
    if v.install not in ("ok", "timeout-but-installed"):
        return v

    target = v.launch_target if os.path.isabs(v.launch_target or "") else None
    if not target:
        shortcuts = lnk.find_shortcuts(prefix)
        target = next((s.resolve(prefix) for s in shortcuts
                       if (s.resolve(prefix) or "").lower().endswith(".exe")), None)
    if not target:
        v.launch = "no-target"
        v.notes.append("no Start Menu shortcut resolved to an executable")
        return v
    v.launch_target = os.path.basename(target)

    xvfb = subprocess.Popen(["Xvfb", display, "-screen", "0", "1280x1024x24"],
                            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    time.sleep(1.5)
    env = dict(os.environ)
    env.update({"WINEPREFIX": prefix, "WINEDEBUG": "-all", "DISPLAY": display})
    app = subprocess.Popen(["wine", target], env=env, cwd=os.path.dirname(target),
                           stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)

    started = time.time()
    try:
        while time.time() - started < PROBE_TIMEOUT:
            time.sleep(1.5)
            found = subprocess.run(["xdotool", "search", "--onlyvisible", "--name", "."],
                                   env=env, capture_output=True)
            for wid in found.stdout.decode().split():
                title = subprocess.run(["xdotool", "getwindowname", wid],
                                       env=env, capture_output=True).stdout.decode().strip()
                geom = subprocess.run(["xdotool", "getwindowgeometry", wid],
                                      env=env, capture_output=True).stdout.decode()
                import re as _re
                m = _re.search(r"Geometry: (\d+x\d+)", geom)
                if title and m and m.group(1) not in ("1x1", "0x0"):
                    v.launch = "window-mapped"
                    v.window_title = title
                    v.window_geometry = m.group(1)
                    v.seconds_to_window = round(time.time() - started, 1)
                    return v
        # No window, but is the process still healthy? Tray apps live here.
        v.launch = "alive-headless" if app.poll() is None else "died"
        if v.launch == "alive-headless":
            v.notes.append("no window mapped but process healthy -- likely tray app")
    finally:
        try:
            app.kill()
        except Exception:
            pass
        subprocess.run(["wineserver", "-k"], env=env, capture_output=True)
        xvfb.terminate()
        xvfb.wait()
    return v


def to_json(verdicts: list[Verdict]) -> str:
    return json.dumps([asdict(v) for v in verdicts], indent=1)

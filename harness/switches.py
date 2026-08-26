"""Default silent-install switches, keyed by winget InstallerType.

This table is load-bearing. Measured against a random sample of 199 winget
manifests, only 29 declare an `InstallerSwitches.Silent` value -- the rest
assume the client already knows how to drive their installer type. winget
hardcodes the same knowledge; so must we.

Generic `exe` installers are included only when their manifest declares a
silent mode. The interactive fallback in the user-facing Go client cannot be
reproduced by a headless compatibility sweep, so the harness deliberately
skips GUI-only executables rather than guessing switches or hiding their UI in
Xvfb.
"""

DEFAULT_SILENT = {
    "nullsoft": ["/S"],
    "inno": ["/VERYSILENT", "/SUPPRESSMSGBOXES", "/NORESTART", "/SP-"],
    "burn": ["/quiet", "/norestart"],
    # msi/wix are driven through msiexec rather than switches on the payload.
    "msi": None,
    "wix": None,
}

# Handled by extracting rather than executing.
ARCHIVE_TYPES = {"zip", "portable"}

# Structurally unsupportable under Wine.
UNSUPPORTED_TYPES = {"msix", "appx", "msstore", "pwa"}

# Installer types the product supports. `exe` remains conditional in this
# headless harness -- see is_supported.
SUPPORTED_TYPES = set(DEFAULT_SILENT) | ARCHIVE_TYPES | {"exe"}


def is_supported(installer_type: str, switches: dict | None = None) -> bool:
    t = (installer_type or "").lower()
    if t == "exe":
        sw = switches or {}
        return bool(sw.get("Silent") or sw.get("SilentWithProgress"))
    return t in SUPPORTED_TYPES


def silent_args(installer_type: str, switches: dict | None) -> list[str] | None:
    """Resolve the argv for a silent install.

    Returns None when the type is driven some other way (msiexec, extraction)
    or when we have no safe way to run it unattended.
    """
    t = (installer_type or "").lower()
    switches = switches or {}

    if t in ARCHIVE_TYPES or t in UNSUPPORTED_TYPES:
        return None

    args: list[str] = []
    declared = switches.get("Silent") or switches.get("SilentWithProgress")
    if declared:
        args += declared.split()
    elif t == "exe":
        return None            # never guess switches for a bare exe
    elif DEFAULT_SILENT.get(t):
        args += DEFAULT_SILENT[t]
    elif t not in ("msi", "wix"):
        return None

    if switches.get("Custom"):
        args += switches["Custom"].split()
    return args

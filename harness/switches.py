"""Default silent-install switches, keyed by winget InstallerType.

This table is load-bearing. Measured against a random sample of 199 winget
manifests, only 29 declare an `InstallerSwitches.Silent` value -- the rest
assume the client already knows how to drive their installer type. winget
hardcodes the same knowledge; so must we.

v1 deliberately omits `exe`. With no declared switches there is no safe guess,
and a wrong guess runs an interactive installer to a timeout in a prefix the
user then has to clean up.

Measured caveat, from the same 199-manifest sample: 13 packages ship an `exe`
as their only variant, and 11 of those (85%) DO declare a Silent switch. So a
blanket exclusion costs 6.5% of the catalog, whereas excluding only the
undeclared ones costs 1.0% -- at identical risk, since a declared switch is no
less trustworthy on `exe` than on `inno`. Flip ALLOW_DECLARED_EXE to recover
them; we never guess switches for `exe` either way.
"""

# v1 default: off, per project decision. Costs ~6.5% of the catalog vs ~1.0%.
ALLOW_DECLARED_EXE = False

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

# Supported in v1. `exe` is excluded on purpose -- see module docstring.
SUPPORTED_TYPES = set(DEFAULT_SILENT) | ARCHIVE_TYPES


def is_supported(installer_type: str, switches: dict | None = None) -> bool:
    t = (installer_type or "").lower()
    if t in SUPPORTED_TYPES:
        return True
    if t == "exe" and ALLOW_DECLARED_EXE:
        sw = switches or {}
        return bool(sw.get("Silent") or sw.get("SilentWithProgress"))
    return False


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

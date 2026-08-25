# Viniget

Viniget brings Winget packages to Linux. It manages WINE prefixes, fetches
Winget package manifests, and installs them under WINE — unattended.

Winget ships **14,586 packages** whose manifests already encode how to drive
each installer silently. That metadata generalises to Wine almost perfectly:
~86% of the catalog uses an installer type Wine can run headlessly, and only
~2% (MSIX/APPX) is structurally impossible.

## Scope

**Productivity software on system Wine.** No games, no Proton, no umu-launcher,
no DXVK. One runner means reproducible compatibility results instead of a
matrix of host configurations. Zalo is currently the one runner exception: a
Wine 11 regression requires the known-good Wine 10 build described below.

## Status

The index client is built — `search`, `show` and `update` against the public
winget index, with no Wine involved.

```
$ viniget search firefox --limit 3
ID                    NAME                         VERSION  MONIKER
Mozilla.Firefox       Mozilla Firefox (en-US)      153.0.4  firefox      +100 locales
Mozilla.Firefox.MSIX  Mozilla Firefox (MSIX)       154.0    firefox
Mozilla.Firefox.ESR   Mozilla Firefox ESR (en-US)  153.0.0  firefox-esr  +101 locales
```

So is the installer. `viniget install` picks the variant Wine can drive,
verifies the download against the manifest's SHA-256, reflinks the baseline
prefix, runs the installer unattended under a private Xvfb, and scores what
landed on disk. It takes a package identifier or the moniker in search's
`MONIKER` column — `viniget install firefox` is `Mozilla.Firefox`.

So is desktop integration. The install ends by reading the Start Menu shortcut
the installer wrote, which is where the launch target comes from — and turning
it into a menu entry, an icon in the hicolor theme, and a launcher.

```
$ viniget install 7zip.7zip
  7z2602-x64.msi  2.2 MB / 2.2 MB  100%
cloning prefix ...
installing (wix, msiexec) ...
installed 7-Zip 26.02
  Type          wix
  Architecture  x64
  Scope         machine
  Prefix        ~/.local/share/viniget/prefixes/7zip.7zip
  Entry point   ~/.local/share/viniget/prefixes/7zip.7zip/drive_c/Program Files/7-Zip/7zFM.exe
  Wrote         108 files, 3 executables
  Took          7.5s (exit 0)
  Product code  {23170F69-40C1-2702-2602-000001000000}
  Wine          wine-11.16

  · sha256 verified

desktop entry
  Name        7-Zip File Manager
  Menu file   ~/.local/share/applications/viniget-7zip.7zip.desktop
  Icon        3 sizes, 16-48px
  Launcher    ~/.local/share/viniget/launchers/7zip.7zip
  Found via   Start Menu shortcut
  File types  .001 .7z .apfs .arj .bz2 .bzip2 .cab .cpio and 32 more  (offered; the default handler is unchanged)

run it with:  viniget run 7zip.7zip
```

The entry appears in the applications menu with the vendor's own icon, and
opening a `.7z` from a file manager offers 7-Zip alongside whatever already
handles it. `viniget desktop <id>` regenerates all of that for a package
installed before any of it existed, and `-remove` takes it away again.

So is uninstalling, and one prefix per package is what turns it into three
deletions rather than a negotiation. Nothing else shares the prefix, so there
is no vendor uninstaller to run — the record even carries the `ProductCode`
that `msiexec /x` would want — no registry to unpick and no `%APPDATA%` left
behind, because all of it is inside the directory being removed.

```
$ viniget uninstall Notepad++.Notepad++
uninstalled Notepad++ 8.9.8
  Prefix   ~/.local/share/viniget/prefixes/Notepad++.Notepad++
  Size     398.9 MB apparent
  Desktop  1 menu entry, 6 icons, 1 launcher

  · apparent size; prefixes are reflink clones, so the space reclaimed is at most that
```

That is 1,970 files in **0.065s**. It sweeps up the abandoned `.installing`
prefix a failed install left beside the real one, and a package whose record
never got written — the one state `viniget list` cannot show — comes out the
same way, off what is on disk. `-all` clears everything, and asks first.

Measured across every supported installer type on Wine 11.16, x86_64, btrfs:

```
7zip.7zip                wix       ok             108 files,   3 exe     7.7s
SumatraPDF.SumatraPDF    portable  ok               1 file,    1 exe     3.3s
voidtools.Everything     wix       ok               4 files,   1 exe    10.8s
Notepad++.Notepad++      nullsoft  ok             127 files,   3 exe    11.8s
Git.Git                  inno      ok            9575 files, 351 exe    43.6s
WinMerge.WinMerge        inno      ok             337 files,   8 exe    68.9s
Microsoft.PowerToys      burn      ok            1317 files,  36 exe     152s  exit 67
Greenshot.Greenshot      inno      no-files                              7.7s
IrfanSkiljan.IrfanView   exe       unsupported                                 declined
```

Three of those rows are the design decisions earning their keep. PowerToys
exits **67** and is installed correctly anyway — the filesystem is what is
scored. Greenshot 1.3.315 is a real Wine incompatibility, not a driving error:
its Inno setup dies in `InitializeSetup` with `Unknown custom message name
"lcid"` whatever switches it is given, and `no-files` is the honest verdict.
IrfanView is a bare `exe` with no declared switches, which viniget declines to
guess at.

Eight installs share **~1 GB** of actual disk between them, against 4.8 GB
apparent: the per-package prefixes are reflink clones of one baseline.

See [docs/design.html](docs/design.html) for the architecture and
[harness/](harness/) for the compatibility harness.

## Build

```sh
go build -o viniget ./cmd/viniget
```

Installing needs system Wine on `PATH`. `xvfb-run` is optional but strongly
recommended: without it the installer runs on your own display, where an Inno
splash or a hidden MSI custom-action window will flash across your desktop.

```sh
pacman -S wine xorg-server-xvfb        # or your distribution's equivalents
```

State lives in `$XDG_DATA_HOME/viniget` (prefixes, `installed.json` and the
generated launchers); payloads and the index cache live in
`$XDG_CACHE_HOME/viniget`. Both are overridable per command with `-data` and
`-cache`. `viniget uninstall` empties the first of
a package and leaves the second alone: a payload is keyed by its digest and
may be the same file another package installs from.

Desktop integration writes to the standard per-user locations —
`$XDG_DATA_HOME/applications`, `.../icons/hicolor` and `.../mime/packages` —
and nowhere else. Nothing is installed system-wide, because an application
inside one user's Wine prefix is not launchable by anybody else. Turn it off
per install with `-no-desktop`, or keep the menu entry without the file types
with `-no-associations`.

## Build order

1. ~~**Index client** — `search` / `show` against the public winget SQLite index. No Wine involved.~~ **Done.**
2. ~~**Install** — nullsoft, inno, burn, msi, wix, zip, portable. ~86% of the catalog.~~ **Done.**
3. ~~**Desktop integration** — `.lnk` → `.desktop`, icons, file associations. This is also where
   the launch target comes from.~~ **Done.**
4. **Compat harness in CI** — verdicts bundled into the index download.

## Design notes

- The winget source index is public and unauthenticated: `source2.msix` is 3.5 MB and is all the client downloads. The CDN does **not** serve manifests — those come from `raw.githubusercontent.com`.
- Manifest paths are derivable from `PackageIdentifier` + `PackageVersion`, so the 19.5 MB `source.msix` is never fetched. Verified against all 164,828 paths in the v1 `pathparts` tree — every one matched. It stays wired as a fallback because the layout is a repo convention, not a contract.
- Searching folds locale forks only. `Mozilla.Firefox.af` collapses into `Mozilla.Firefox`; `Microsoft.DotNet.SDK.10` and `JetBrains.PyCharm.EAP` stay their own rows. 14,586 packages, 14,155 families.
- Prefixes are copy-on-write. Cloning the 444 MB baseline takes 0.003s on btrfs and consumes no new extents, which makes rollback-before-install free. A failed install never touches the previous prefix: the clone is installed into and swapped in only once it has been scored.
- Install success is scored on the filesystem delta, never the exit code. PowerToys is the live example — its burn bundle exits 67 having correctly written 1,317 files and 36 executables.
- **Never read an installer's output through a pipe.** `wineserver` inherits the write end and outlives the installer by design, so the wait blocks on a pipe nobody will ever close. Measured: WinMerge's Inno setup with `stdout=PIPE` "times out" at 240s; the identical install writing to `/dev/null` exits 0 in **8.9s**, and both leave the same 11 executables on disk. That artifact — not a slow installer — is where the harness's `timeout-but-installed` verdict for WinMerge came from. `harness/README.md` and `docs/v1-design.html` still cite it as a measurement.
- Launch targets come from Start Menu `.lnk` files, never from filename matching. Filename matching picks `WinMerge32BitPluginProxy.exe` over `WinMergeU.exe`; the shortcut names the real one. Archive packages have no shortcut and fall back to `NestedInstallerFiles`.
- Icons are read straight out of the payload — `RT_GROUP_ICON`/`RT_ICON` resources in the executable, or an `.ico` on disk — and decoded to PNG in-process. No `icoutils`, no ImageMagick, no Wine involved. Every size the vendor shipped is installed, because a 256×256 scaled down to a 22px panel looks worse than the 22px art already in the same file.
- A group icon is as often string-named — `MAINICON` from Delphi, `IDI_ICON1` from windres — as it is numbered, and a PE resource directory stores its named entries *ahead* of its numbered ones. Walking from the first numbered entry therefore finds nothing at all in those executables: Icecream Ebook Reader ships eight sizes under `IDI_ICON1` and reported `no icon resources`. Only the resource *type* is guaranteed to be a number.
- A few applications need a `WINEDLLOVERRIDES` to start at all, so the generated launcher carries one where viniget knows of it. Icecream Ebook Reader is the measured case: it enumerates the SAPI voice tokens' attributes at startup and loops until told there are no more, and Wine's `ISpDataKey::EnumValues` is a stub returning `E_NOTIMPL` rather than `SPERR_NO_MORE_ITEMS` — so it spins at 100% of a core on its splash screen and never opens a window. `sapi=d` costs the read-aloud feature that same stub was never going to provide. Set your own with `-dll-overrides` on `install` or `desktop`, pass it empty to drop viniget's, or take it off for a single launch with `WINEDLLOVERRIDES= viniget run <id>`.
- Zalo 26.8.10 embeds Electron 22.3.9/Chromium 108 and hangs at its white splash screen under Wine 11.15 and 11.16: the login renderer stops responding while the notification, SQLite and shared-worker contexts remain alive. `libglesv2=d`, disabling the GPU, and changing NTSync/ESync/FSync do not fix Wine 11. The identical prefix reaches the QR login under `wine-10.0-20260425`, with either server sync or NTSync, so this is a Wine 11 compatibility regression rather than a Zalo install or graphics failure. Its launcher quirk clears host Electron/MangoHud injection, defaults `WINENTSYNC`, `WINEESYNC` and `WINEFSYNC` to `0`, and prepends `/opt/wine-cachyos/bin` when that Wine 10 build is installed. Those defaults remain overridable from the environment. Do not alternate that prefix between Wine 10 and Wine 11 after Wine has updated it; keep Zalo on Wine 10 until the regression is fixed, then regenerate the launcher with `viniget desktop VNGCorp.Zalo` after removing the quirk.
- File associations are an offer, never a claim. An extension the shared MIME database already knows keeps the system's type; only genuinely unknown ones get a viniget-declared one. `MimeType=` makes an application *available* for a type — `mimeapps.list`, which decides the default, is never written. Installing a Windows PDF viewer must not silently take `.pdf` from the one already handling it.
- The `.desktop` entry runs a generated launcher script rather than `wine` directly. It converts host paths in the arguments to the prefix's drive letters — a Windows application handed `/home/you/report.pdf` cannot open it — launches from the shortcut's working directory, and addresses the executable as `C:\...` so the application does not see itself installed on a network drive.
- The prefix's `Desktop` has to be a real directory, or desktop integration fails from both ends at once. `wineboot` points `drive_c/users/<you>/Desktop` at `$HOME/Desktop`, so an installer that offers to create a desktop shortcut writes its `.lnk` onto the actual desktop — a Windows shortcut file no Linux file manager can launch — while viniget never sees it, because `filepath.WalkDir` reports a symlinked root as a single non-directory entry and never descends it. A package whose only shortcut is that one therefore produces **no menu entry at all**, which is how one bug shows up as two unrelated-looking symptoms. Reading through the link instead would be worse: every prefix shares that one directory, so each install would be scanning every other package's shortcuts. Only `Desktop` is contained — `Documents`, `Downloads`, `Pictures` and the rest stay linked, because an application that cannot reach the files it was installed to open is a regression and none of them feed the menu. The registry keeps naming `C:\users\<you>\Desktop` either way, so the installer still writes exactly where it meant to. Real directories survive `wineboot -u`: wine only creates the symlink when the folder is missing.
- Wine's own menu builder has to be turned off, or every install writes its menu entry twice. `winemenubuilder.exe` runs from inside the prefix and writes a `.desktop` file and an icon into `~/.local/share/` for every Start Menu shortcut an installer creates, naming whatever `WINEPREFIX` was current — which during an install is the `.installing` staging prefix, renamed away seconds later. What lands in the menu is therefore a near-duplicate of viniget's own entry that was already broken when it appeared, and nothing ever comes back to correct it: wine only revisits those files while running against the prefix they name. On this machine the sweep found 9 such files from one Icecream Ebook Reader install and 7 from Notepad++. `winemenubuilder.exe=d` now goes into every command viniget runs and into the launcher scripts, and the baseline prefix carries the same override in its registry so that it survives the escape hatches — `WINEDLLOVERRIDES= viniget run <id>` drops viniget's launcher override for one launch without handing the menu back to wine. Integration and uninstall sweep what earlier versions left behind, matched on the prefix path so a stale `Mozilla.Firefox` entry cannot take `Mozilla.Firefox.ESR` with it.
- Uninstalling is three deletions in a fixed order, because the prefix *is* the package. The menu entry goes first, so a removal interrupted halfway leaves an application that cannot be launched rather than a menu entry pointing into a prefix that is being deleted. The record goes last, and only once the prefix has actually gone — a record dropped over a surviving prefix is an orphan nothing lists, which is the state the command exists to get people out of. Running the vendor's own uninstaller would be strictly worse than skipping it: it is the least tested path in any Windows installer, it fails differently under Wine than on Windows, and everything it missed is inside the directory being deleted a moment later.
- Sizes are reported as apparent, never as space freed. `du` charges every reflinked clone the baseline's full 437 MB, so eight packages holding about a gigabyte between them would report 4.8 GB "freed" — and what the filesystem really gives back depends on which extents the baseline still holds after the delete. Apparent size is an upper bound that does not change depending on what else happens to be installed, which is the only honest number available without asking the filesystem about extent ownership.
- A manifest's declared installer type is a hint, not a promise. SumatraPDF declares `portable` and ships a zip; the payload's magic number is believed over the manifest.
- A moniker is a nickname, not a name: 108 of them in the index are shared, so `install` only accepts one when it points somewhere. Locale forks collapse first — 102 packages answer to `firefox` — and then the release channels, architecture variants and repackagings that hang off a base package collapse onto it, because `Mozilla.Firefox.MSIX` extends `Mozilla.Firefox` and `Hashicorp.Terraform.{Alpha,Beta,RC}` extend `Hashicorp.Terraform`. That resolves 39 of the 108 and picks the stable channel every time. The other 69 are listed for the user to choose from rather than guessed at: `postgresql` is the moniker of PostgreSQL 17, 18 and four Postgres Pro builds, and `mc` means Midnight Commander or the MinIO client depending on who is asking. Identifiers are matched first, so a moniker can never shadow a package's real name.

## Layout

| path | role |
|---|---|
| `cmd/viniget/` | CLI — `search`, `show`, `install`, `uninstall`, `run`, `list`, `desktop`, `update` |
| `internal/index/` | index fetch, search, locale fold, path resolution |
| `internal/manifest/` | manifest types and fetching |
| `internal/install/` | installer selection, silent switches, download, extraction, delta scoring, installed-package state |
| `internal/wine/` | prefix store, reflink clone, headless display, command execution |
| `internal/desktop/` | `.lnk` parsing, PE/ICO icon extraction, `.desktop` entries, MIME associations, launchers |
| `internal/cli/` | terminal rendering |
| `harness/` | compatibility harness — install, probe, verdict |
| `docs/v1-design.html` | design document |

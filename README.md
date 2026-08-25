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
matrix of host configurations.

## Status

The compatibility harness is validated end to end. Six packages spanning every
supported installer type, fully headless on Wine 11.16:

```
Notepad++.Notepad++      nullsoft  ok                     window-mapped   notepad++.exe
Greenshot.Greenshot      inno      ok                     alive-headless  Greenshot.exe
voidtools.Everything     wix       ok                     window-mapped   Everything.exe
WinMerge.WinMerge        inno      timeout-but-installed  window-mapped   WinMergeU.exe
SumatraPDF.SumatraPDF    portable  ok                     window-mapped   SumatraPDF-3.6.1-64.exe
IrfanSkiljan.IrfanView   exe       unsupported            not-attempted
```

The index client is built: `search`, `show` and `update` against the public
winget index, with no Wine involved.

```
$ viniget search firefox --limit 3
ID                    NAME                         VERSION  MONIKER
Mozilla.Firefox       Mozilla Firefox (en-US)      153.0.4  firefox      +100 locales
Mozilla.Firefox.MSIX  Mozilla Firefox (MSIX)       154.0    firefox
Mozilla.Firefox.ESR   Mozilla Firefox ESR (en-US)  153.0.0  firefox-esr  +101 locales
```

See [docs/design.html](docs/design.html) for the architecture and
[harness/](harness/) for the compatibility harness.

## Build

```sh
go build -o viniget ./cmd/viniget
```

## Build order

1. ~~**Index client** — `search` / `show` against the public winget SQLite index. No Wine involved.~~ **Done.**
2. **Install** — nullsoft, inno, msi, wix, portable. ~74% of the catalog.
3. **Desktop integration** — `.lnk` → `.desktop`, icons, file associations.
4. **Compat harness in CI** — verdicts bundled into the index download.

## Design notes

- The winget source index is public and unauthenticated: `source2.msix` is 3.5 MB and is all the client downloads. The CDN does **not** serve manifests — those come from `raw.githubusercontent.com`.
- Manifest paths are derivable from `PackageIdentifier` + `PackageVersion`, so the 19.5 MB `source.msix` is never fetched. Verified against all 164,828 paths in the v1 `pathparts` tree — every one matched. It stays wired as a fallback because the layout is a repo convention, not a contract.
- Searching folds locale forks only. `Mozilla.Firefox.af` collapses into `Mozilla.Firefox`; `Microsoft.DotNet.SDK.10` and `JetBrains.PyCharm.EAP` stay their own rows. 14,586 packages, 14,155 families.
- Prefixes are copy-on-write. Cloning the 444 MB baseline takes 0.003s on btrfs and consumes no new extents, which makes rollback-before-install free.
- Install success is scored on the filesystem delta, never the exit code.
- Launch targets come from Start Menu `.lnk` files, never from filename matching.

## Layout

| path | role |
|---|---|
| `cmd/viniget/` | CLI — `search`, `show`, `update` |
| `internal/index/` | index fetch, search, locale fold, path resolution |
| `internal/manifest/` | manifest types and fetching |
| `internal/cli/` | terminal rendering |
| `harness/` | compatibility harness — install, probe, verdict |
| `docs/design.html` | design document |

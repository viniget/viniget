# viniget compat harness

Installs a winget package into a throwaway Wine prefix, launches it, and
records what happened. The output is the compatibility database that ships
bundled with the index.

## Setup

```sh
pacman -S wine winetricks xorg-server-xvfb xdotool p7zip imagemagick
pip install pyyaml

WINEPREFIX=$PWD/pfx-base wineboot -u     # ~444 MB baseline, created once
```

## Run

```sh
python3 run.py --base ./pfx-base Notepad++.Notepad++ WinMerge.WinMerge
python3 run.py --base ./pfx-base --from-index --limit 500
```

Each package is scored in two stages and written to `compat.json`.

| stage | values |
|---|---|
| `install` | `ok`, `timeout-but-installed`, `files-but-no-exe`, `no-files`, `download-failed`, `unsupported` |
| `launch` | `window-mapped`, `alive-headless`, `died`, `no-target`, `not-attempted` |

## Three rules the code exists to enforce

**Score on the filesystem, not the exit code.** WinMerge's Inno installer runs
past a 240s timeout having already written 337 files and 8 executables
correctly. `timeout-but-installed` is a pass, not a failure.

**Take the launch target from the Start Menu `.lnk`.** Matching the package
name against filenames on disk picks `WinMerge32BitPluginProxy.exe` over
`WinMergeU.exe` and reports a working app as broken. Archive packages have no
shortcut; their entry point comes from `NestedInstallerFiles` in the manifest
instead.

**Outcomes are three-way.** Greenshot maps zero windows and is entirely
healthy — it is a tray application. `alive-headless` is a pass. Collapsing to
pass/fail writes off every background utility in the catalog.

## Files

| file | role |
|---|---|
| `switches.py` | default silent switches per InstallerType; the `exe` policy |
| `lnk.py` | MS-SHLLINK parser — launch targets (the client has its own in Go) |
| `index.py` | winget source index client (search + manifest path resolution) |
| `harness.py` | install, probe, verdict |
| `run.py` | CLI |

## Validation

Six packages spanning every supported installer type, on Wine 11.16:

```
Notepad++.Notepad++      nullsoft  ok                     window-mapped   notepad++.exe
Greenshot.Greenshot      inno      ok                     alive-headless  Greenshot.exe
voidtools.Everything     wix       ok                     window-mapped   Everything.exe
WinMerge.WinMerge        inno      timeout-but-installed  window-mapped   WinMergeU.exe
SumatraPDF.SumatraPDF    portable  ok                     window-mapped   SumatraPDF-3.6.1-64.exe
IrfanSkiljan.IrfanView   exe       unsupported            not-attempted
```

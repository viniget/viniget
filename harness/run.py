#!/usr/bin/env python3
"""viniget compat harness runner.

    python3 run.py --base ./pfx-base Notepad++.Notepad++ WinMerge.WinMerge
    python3 run.py --base ./pfx-base --from-index --limit 200

Creating the base prefix once, up front:

    WINEPREFIX=$PWD/pfx-base wineboot -u      # ~444 MB, one time
"""

import argparse
import os
import sys
import urllib.request

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

import yaml

import harness
import index


def load_manifest(url: str) -> dict | None:
    try:
        with urllib.request.urlopen(url, timeout=60) as r:
            return yaml.safe_load(r.read().decode("utf-8", "replace"))
    except Exception:
        return None


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("packages", nargs="*")
    ap.add_argument("--base", required=True, help="baseline wine prefix to reflink from")
    ap.add_argument("--work", default="./runs")
    ap.add_argument("--cache", default="./dl")
    ap.add_argument("--index-dir", default="./idx")
    ap.add_argument("--from-index", action="store_true", help="sweep the whole catalog")
    ap.add_argument("--limit", type=int, default=50)
    ap.add_argument("--out", default="compat.json")
    ap.add_argument("--keep", action="store_true", help="keep prefixes after probing")
    args = ap.parse_args()

    if not os.path.isdir(args.base):
        print(f"base prefix missing: {args.base}\n"
              f"create it with:  WINEPREFIX={os.path.abspath(args.base)} wineboot -u", file=sys.stderr)
        return 2

    os.makedirs(args.index_dir, exist_ok=True)
    v1 = os.path.join(args.index_dir, "index-v1.db")
    if not os.path.exists(v1):
        print("fetching winget source index ...", file=sys.stderr)
        index.fetch_index(index.SOURCE_V1, v1)
    paths = index.PathIndex(v1)

    targets = list(args.packages)
    if args.from_index:
        v2 = os.path.join(args.index_dir, "index-v2.db")
        if not os.path.exists(v2):
            index.fetch_index(index.SOURCE_V2, v2)
        cat = index.Catalog(v2)
        seen: set[str] = set()
        for pid, *_ in cat.search("", limit=100000):
            fam = ".".join(pid.split(".")[:2])
            if fam in seen:            # collapse locale/version forks
                continue
            seen.add(fam)
            targets.append(pid)
            if len(targets) >= args.limit:
                break

    verdicts = []
    for pid in targets:
        url = paths.installer_url(pid)
        if not url:
            print(f"{pid:<38} SKIP  not in index", flush=True)
            continue
        manifest = load_manifest(url)
        if not manifest:
            print(f"{pid:<38} SKIP  manifest fetch failed", flush=True)
            continue

        prefix = os.path.join(args.work, pid)
        v = harness.install(manifest, prefix, args.base, args.cache)
        v = harness.probe(v, prefix)
        verdicts.append(v)
        print(f"{pid:<38} {v.installer_type:<9} {v.install:<22} "
              f"{v.launch:<15} {v.launch_target}", flush=True)
        if not args.keep:
            import shutil
            shutil.rmtree(prefix, ignore_errors=True)

    with open(args.out, "w") as fh:
        fh.write(harness.to_json(verdicts))
    ok = sum(1 for v in verdicts if v.works)
    print(f"\n{ok}/{len(verdicts)} usable under Wine -> {args.out}", file=sys.stderr)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

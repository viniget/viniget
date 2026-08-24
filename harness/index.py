"""Client for the public winget source index.

Microsoft publishes the catalog as an unauthenticated MSIX (a zip) containing
a SQLite database, refreshed roughly hourly:

    source2.msix   3.5 MB   v2 schema, 14,586 packages, fast search
    source.msix     20 MB   v1 schema, adds the `pathparts` tree

Only the v1 index carries explicit manifest paths, so we need both: v2 for
search, v1 to resolve a package to a manifest URL.

One sharp edge: v1 pathparts end in a 4-hex-character shard segment
(`.../1.134.0/b90d`) that does not exist in the GitHub tree. It must be
stripped before building a raw.githubusercontent.com URL. The CDN does not
serve manifests at all -- every path shape returns BlobNotFound.
"""

import io
import os
import re
import sqlite3
import urllib.request
import zipfile

CDN = "https://cdn.winget.microsoft.com/cache"
RAW = "https://raw.githubusercontent.com/microsoft/winget-pkgs/master"

SOURCE_V2 = f"{CDN}/source2.msix"
SOURCE_V1 = f"{CDN}/source.msix"

_SHARD = re.compile(r"^[0-9a-f]{4}$")


def fetch_index(url: str, dest: str) -> str:
    """Download a source msix and extract Public/index.db to `dest`."""
    os.makedirs(os.path.dirname(dest) or ".", exist_ok=True)
    with urllib.request.urlopen(url, timeout=300) as resp:
        blob = resp.read()
    with zipfile.ZipFile(io.BytesIO(blob)) as zf:
        with zf.open("Public/index.db") as src, open(dest, "wb") as out:
            out.write(src.read())
    return dest


class Catalog:
    """Search over the v2 index."""

    def __init__(self, db_path: str):
        self.db = sqlite3.connect(db_path)

    def count(self) -> int:
        return self.db.execute("select count(*) from packages").fetchone()[0]

    def search(self, term: str, limit: int = 25) -> list[tuple]:
        like = f"%{term}%"
        return self.db.execute(
            "select id, name, moniker, latest_version from packages "
            "where id like ? or name like ? or moniker like ? "
            "order by length(id) limit ?",
            (like, like, like, limit),
        ).fetchall()

    def get(self, package_id: str) -> tuple | None:
        return self.db.execute(
            "select id, name, moniker, latest_version from packages where id = ? collate nocase",
            (package_id,),
        ).fetchone()

    def families(self) -> dict[str, int]:
        """Collapse locale/version forks: Mozilla.Firefox ships 310 packages.

        14,586 raw packages become 12,401 families.
        """
        fam: dict[str, int] = {}
        for (pid,) in self.db.execute("select id from packages"):
            key = ".".join(pid.split(".")[:2])
            fam[key] = fam.get(key, 0) + 1
        return fam


class PathIndex:
    """Resolve package id -> manifest directory, using the v1 index."""

    def __init__(self, db_path: str):
        self.db = sqlite3.connect(db_path)
        self._parts = {
            rid: (parent, part)
            for rid, parent, part in self.db.execute(
                "select rowid, parent, pathpart from pathparts"
            )
        }

    def _path(self, leaf: int) -> str:
        out = []
        while leaf:
            parent, part = self._parts[leaf]
            out.append(part)
            leaf = parent
        return "/".join(reversed(out))

    def manifest_dir(self, package_id: str) -> str | None:
        row = self.db.execute(
            "select rowid from ids where id = ? collate nocase", (package_id,)
        ).fetchone()
        if not row:
            return None
        man = self.db.execute(
            "select pathpart from manifest where id = ? order by rowid desc limit 1",
            (row[0],),
        ).fetchone()
        if not man:
            return None
        path = self._path(man[0])
        head, _, tail = path.rpartition("/")
        return head if _SHARD.match(tail) else path

    def installer_url(self, package_id: str) -> str | None:
        d = self.manifest_dir(package_id)
        return f"{RAW}/{d}/{package_id}.installer.yaml" if d else None

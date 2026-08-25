package index

import (
	"database/sql"
	"regexp"
	"strings"

	_ "modernc.org/sqlite"
)

// shard matches the four-hex-character segment the v1 index appends to every
// manifest path (".../1.134.0/b90d"). It exists in the index but not in the
// GitHub tree, so it must be stripped before building a URL.
var shard = regexp.MustCompile(`^[0-9a-f]{4}$`)

// PathIndex resolves a package identifier to its manifest directory using the
// v1 index's pathparts tree.
//
// This is the fallback for ManifestBase, which derives the same path from the
// identifier alone. Derivation matched all 164,828 v1 paths when it was
// verified, so this code should never run; it exists because the layout is a
// repository convention rather than a guaranteed contract.
type PathIndex struct {
	db    *sql.DB
	parts map[int64]pathPart
}

type pathPart struct {
	parent sql.NullInt64
	name   string
}

// OpenPathIndex loads the v1 pathparts tree into memory. The tree is walked
// once per lookup and is far too chatty to leave in SQLite.
func OpenPathIndex(path string) (*PathIndex, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	rows, err := db.Query("select rowid, parent, pathpart from pathparts")
	if err != nil {
		db.Close()
		return nil, err
	}
	defer rows.Close()

	parts := map[int64]pathPart{}
	for rows.Next() {
		var id int64
		var p pathPart
		if err := rows.Scan(&id, &p.parent, &p.name); err != nil {
			db.Close()
			return nil, err
		}
		parts[id] = p
	}
	if err := rows.Err(); err != nil {
		db.Close()
		return nil, err
	}
	return &PathIndex{db: db, parts: parts}, nil
}

func (p *PathIndex) Close() error { return p.db.Close() }

// path walks parent links from a leaf up to the root and joins them.
func (p *PathIndex) path(leaf int64) string {
	var segs []string
	for {
		part, ok := p.parts[leaf]
		if !ok {
			break
		}
		segs = append(segs, part.name)
		if !part.parent.Valid || part.parent.Int64 == 0 {
			break
		}
		leaf = part.parent.Int64
	}
	for i, j := 0, len(segs)-1; i < j; i, j = i+1, j-1 {
		segs[i], segs[j] = segs[j], segs[i]
	}
	return strings.Join(segs, "/")
}

// ManifestDir returns the repo-relative manifest directory for a package's
// newest indexed version, or "" when the package is not present.
func (p *PathIndex) ManifestDir(id string) (string, error) {
	var idRow int64
	err := p.db.QueryRow("select rowid from ids where id = ? collate nocase", id).Scan(&idRow)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var leaf int64
	err = p.db.QueryRow(
		"select pathpart from manifest where id = ? order by rowid desc limit 1", idRow).Scan(&leaf)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	full := p.path(leaf)
	if i := strings.LastIndex(full, "/"); i >= 0 && shard.MatchString(full[i+1:]) {
		return full[:i], nil
	}
	return full, nil
}

package index

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"

	_ "modernc.org/sqlite"
)

// Package is one row of the v2 index.
type Package struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Moniker string `json:"moniker,omitempty"`
	Version string `json:"version"`
}

// Result is a search hit after locale folding.
type Result struct {
	Package
	// Locales counts the locale forks folded into this row, 0 when none.
	Locales int `json:"locales,omitempty"`
	tier    int
}

// Catalog is a read-only handle on the v2 index database.
//
// Schema (verified against the live index, not assumed):
//
//	packages(rowid, id, name, moniker, latest_version, arp_min_version,
//	         arp_max_version, hash)
//	tags2(rowid, tag)          tags2_map(tag, package)
//	commands2(rowid, command)  commands2_map(command, package)
//	metadata(name, value)
//
// norm_names2 exists but holds empty strings for many rows, so it is not a
// usable ranking signal and is ignored.
type Catalog struct {
	db     *sql.DB
	all    []Package
	byID   map[string]Package
	folder *Folder
}

// Open opens an extracted index.db read-only.
func Open(path string) (*Catalog, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("open index %s: %w", path, err)
	}
	return &Catalog{db: db}, nil
}

func (c *Catalog) Close() error { return c.db.Close() }

// Count returns the number of packages in the index.
func (c *Catalog) Count() (int, error) {
	var n int
	err := c.db.QueryRow("select count(*) from packages").Scan(&n)
	return n, err
}

// load reads the whole packages table once. The fold rule needs the complete
// identifier set to decide whether a candidate parent is a real package, so
// there is no cheaper way to do this correctly.
func (c *Catalog) load() error {
	if c.all != nil {
		return nil
	}
	rows, err := c.db.Query("select id, name, coalesce(moniker,''), latest_version from packages")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var p Package
		if err := rows.Scan(&p.ID, &p.Name, &p.Moniker, &p.Version); err != nil {
			return err
		}
		c.all = append(c.all, p)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	c.byID = make(map[string]Package, len(c.all))
	for _, p := range c.all {
		c.byID[p.ID] = p
	}
	c.folder = NewFolder(c.all)
	return nil
}

// Get looks up one package by exact identifier, case-insensitively.
func (c *Catalog) Get(id string) (*Package, error) {
	var p Package
	err := c.db.QueryRow(
		"select id, name, coalesce(moniker,''), latest_version from packages "+
			"where id = ? collate nocase", id).
		Scan(&p.ID, &p.Name, &p.Moniker, &p.Version)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// likeEscape neutralises LIKE wildcards in user input.
func likeEscape(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

// tierQueries returns the ranked match passes, best tier first. Splitting the
// ranking across targeted queries keeps each one able to use an index, which a
// single OR-heavy statement could not.
func tierQueries() []struct {
	sql  string
	args func(term, esc string) []any
} {
	type q = struct {
		sql  string
		args func(term, esc string) []any
	}
	const sel = "select p.id, p.name, coalesce(p.moniker,''), p.latest_version from packages p "
	return []q{
		// 0: exact identifier
		{sel + "where p.id = ? collate nocase",
			func(t, e string) []any { return []any{t} }},
		// 1: exact moniker -- "vscode" -> Microsoft.VisualStudioCode
		{sel + "where p.moniker = ? collate nocase",
			func(t, e string) []any { return []any{t} }},
		// 2: exact command name
		{sel + "join commands2_map m on m.package = p.rowid " +
			"join commands2 c on c.rowid = m.command where c.command = ? collate nocase",
			func(t, e string) []any { return []any{t} }},
		// 3: identifier or name prefix
		{sel + `where p.id like ? escape '\' or p.name like ? escape '\'`,
			func(t, e string) []any { return []any{e + "%", e + "%"} }},
		// 4: substring anywhere
		{sel + `where p.id like ? escape '\' or p.name like ? escape '\' or p.moniker like ? escape '\'`,
			func(t, e string) []any { return []any{"%" + e + "%", "%" + e + "%", "%" + e + "%"} }},
		// 5: exact tag
		{sel + "join tags2_map m on m.package = p.rowid " +
			"join tags2 t on t.rowid = m.tag where t.tag = ? collate nocase",
			func(t, e string) []any { return []any{t} }},
	}
}

// Search returns ranked, locale-folded results for a term.
func (c *Catalog) Search(term string, limit int) ([]Result, error) {
	if err := c.load(); err != nil {
		return nil, err
	}
	esc := likeEscape(term)

	best := map[string]int{} // package id -> best tier seen
	for tier, q := range tierQueries() {
		rows, err := c.db.Query(q.sql, q.args(term, esc)...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var p Package
			if err := rows.Scan(&p.ID, &p.Name, &p.Moniker, &p.Version); err != nil {
				rows.Close()
				return nil, err
			}
			if old, seen := best[p.ID]; !seen || tier < old {
				best[p.ID] = tier
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}

	// Collapse locale forks onto their base package, keeping the best tier any
	// member matched at and counting what was folded away.
	type agg struct {
		tier    int
		locales int
	}
	fams := map[string]*agg{}
	for id, tier := range best {
		fam := c.folder.Fold(id)
		a := fams[fam]
		if a == nil {
			a = &agg{tier: tier}
			fams[fam] = a
		} else if tier < a.tier {
			a.tier = tier
		}
		if fam != id {
			a.locales++
		}
	}

	out := make([]Result, 0, len(fams))
	for fam, a := range fams {
		p, ok := c.byID[fam]
		if !ok {
			continue
		}
		out = append(out, Result{Package: p, Locales: a.locales, tier: a.tier})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].tier != out[j].tier {
			return out[i].tier < out[j].tier
		}
		if len(out[i].ID) != len(out[j].ID) {
			return len(out[i].ID) < len(out[j].ID)
		}
		return out[i].ID < out[j].ID
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// Families returns the total number of packages and locale-folded families.
func (c *Catalog) Families() (packages, families int, err error) {
	if err := c.load(); err != nil {
		return 0, 0, err
	}
	seen := map[string]bool{}
	for _, p := range c.all {
		seen[c.folder.Fold(p.ID)] = true
	}
	return len(c.all), len(seen), nil
}

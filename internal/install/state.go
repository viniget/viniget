package install

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Record is what viniget remembers about an installed package.
//
// Deliberately not derived by scanning prefixes at read time. A prefix says
// what files exist, not which package put them there or which manifest
// version they came from, and that provenance is what upgrade and uninstall
// will both need.
type Record struct {
	ID             string    `json:"id"`
	Version        string    `json:"version"`
	Name           string    `json:"name,omitempty"`
	InstallerType  string    `json:"installer_type,omitempty"`
	Status         Status    `json:"status"`
	Prefix         string    `json:"prefix"`
	Entry          string    `json:"entry,omitempty"`
	ProductCode    string    `json:"product_code,omitempty"`
	Scope          string    `json:"scope,omitempty"`
	Architecture   string    `json:"architecture,omitempty"`
	NewFiles       int       `json:"new_files,omitempty"`
	NewExecutables int       `json:"new_executables,omitempty"`
	WineVersion    string    `json:"wine_version,omitempty"`
	InstalledAt    time.Time `json:"installed_at"`
}

// RecordOf converts a successful install into its durable record.
func RecordOf(r *Result) Record {
	return Record{
		ID:             r.ID,
		Version:        r.Version,
		Name:           r.Name,
		InstallerType:  r.InstallerType,
		Status:         r.Status,
		Prefix:         r.Prefix,
		Entry:          r.Entry,
		ProductCode:    r.ProductCode,
		Scope:          r.Scope,
		Architecture:   r.Architecture,
		NewFiles:       r.NewFiles,
		NewExecutables: r.NewExecutables,
		WineVersion:    r.WineVersion,
		InstalledAt:    r.InstalledAt,
	}
}

// DB is the list of installed packages, held as one JSON file.
//
// JSON rather than SQLite because this table is tens of rows, is read by
// humans when something goes wrong, and must never be the reason an install
// cannot be recorded. The index database earns SQLite; this does not.
type DB struct {
	path    string
	records map[string]Record
}

// StatePath is where the installed-package list lives under a data dir.
func StatePath(dataDir string) string { return filepath.Join(dataDir, "installed.json") }

// OpenDB loads the installed-package list, returning an empty one if the file
// does not exist yet.
func OpenDB(path string) (*DB, error) {
	db := &DB{path: path, records: map[string]Record{}}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return db, nil
	}
	if err != nil {
		return nil, err
	}
	var list []Record
	if err := json.Unmarshal(b, &list); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	for _, r := range list {
		db.records[r.ID] = r
	}
	return db, nil
}

// Get returns the record for a package, if it is installed.
func (db *DB) Get(id string) (Record, bool) {
	r, ok := db.records[id]
	return r, ok
}

// Put adds or replaces a record.
func (db *DB) Put(r Record) { db.records[r.ID] = r }

// Remove drops a record.
func (db *DB) Remove(id string) { delete(db.records, id) }

// All returns every record, most recently installed first.
func (db *DB) All() []Record {
	list := make([]Record, 0, len(db.records))
	for _, r := range db.records {
		list = append(list, r)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].InstalledAt.Equal(list[j].InstalledAt) {
			return list[i].ID < list[j].ID
		}
		return list[i].InstalledAt.After(list[j].InstalledAt)
	})
	return list
}

// Save writes the list back atomically. A crash mid-write leaves the previous
// list intact rather than a truncated one, which matters because a lost record
// orphans a prefix that nothing then knows how to remove.
func (db *DB) Save() error {
	if err := os.MkdirAll(filepath.Dir(db.path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(db.All(), "", "  ")
	if err != nil {
		return err
	}
	tmp := db.path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, db.path)
}

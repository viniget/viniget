package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/viniget/viniget/internal/index"
	"github.com/viniget/viniget/internal/install"
	_ "modernc.org/sqlite"
)

func TestUpdateAlreadyCurrentNeedsNeitherWineNorManifest(t *testing.T) {
	data, cache := t.TempDir(), t.TempDir()
	seedUpdateIndex(t, cache, "Vendor.Demo", "Demo", "2.0")
	db, err := install.OpenDB(install.StatePath(data))
	if err != nil {
		t.Fatal(err)
	}
	db.Put(install.Record{ID: "Vendor.Demo", Name: "Demo", Version: "2.0", Prefix: "/not-needed"})
	if err := db.Save(); err != nil {
		t.Fatal(err)
	}

	// An already-current check must finish before Wine availability or a
	// manifest download matters.
	t.Setenv("PATH", t.TempDir())
	out := captureCommandOutput(t)
	if code := cmdUpdate([]string{"Vendor.Demo", "-data", data, "-cache", cache, "-no-refresh"}); code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if err := out.Sync(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(out.Name())
	if err != nil {
		t.Fatal(err)
	}
	if got := string(b); !strings.Contains(got, "Demo 2.0 is already current") {
		t.Errorf("output = %q", got)
	}
}

func TestUpdateRequiresAnInstalledPackageBeforeIndexOrWine(t *testing.T) {
	data, cache := t.TempDir(), filepath.Join(t.TempDir(), "missing-cache")
	t.Setenv("PATH", t.TempDir())
	out := captureCommandOutput(t)
	if code := cmdUpdate([]string{"Missing.Package", "-data", data, "-cache", cache}); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if err := out.Sync(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(out.Name())
	if err != nil {
		t.Fatal(err)
	}
	if got := string(b); !strings.Contains(got, "Missing.Package is not installed") {
		t.Errorf("output = %q", got)
	}
	if _, err := os.Stat(cache); !os.IsNotExist(err) {
		t.Errorf("missing-package check touched the index cache: %v", err)
	}
}

func TestUpdateOutdatedPackageContinuesToInstallerChecks(t *testing.T) {
	data, cache := t.TempDir(), t.TempDir()
	seedUpdateIndex(t, cache, "Vendor.Demo", "Demo", "2.0")
	db, err := install.OpenDB(install.StatePath(data))
	if err != nil {
		t.Fatal(err)
	}
	db.Put(install.Record{ID: "Vendor.Demo", Name: "Demo", Version: "1.0", Prefix: "/old"})
	if err := db.Save(); err != nil {
		t.Fatal(err)
	}

	t.Setenv("PATH", t.TempDir())
	out := captureCommandOutput(t)
	if code := cmdUpdate([]string{"Vendor.Demo", "-data", data, "-cache", cache, "-no-refresh"}); code != 1 {
		t.Fatalf("exit = %d, want 1 without Wine", code)
	}
	if err := out.Sync(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(out.Name())
	if err != nil {
		t.Fatal(err)
	}
	if got := string(b); !strings.Contains(got, "wine is not installed") {
		t.Errorf("outdated package did not continue to installer checks: %q", got)
	}
}

func seedUpdateIndex(t *testing.T, cache, id, name, version string) {
	t.Helper()
	store := index.NewStore(cache)
	if err := os.MkdirAll(store.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", store.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
CREATE TABLE packages(
  rowid INTEGER PRIMARY KEY,
  id TEXT NOT NULL,
  name TEXT NOT NULL,
  moniker TEXT,
  latest_version TEXT NOT NULL,
  arp_min_version TEXT,
  arp_max_version TEXT,
  hash BLOB
);
INSERT INTO packages(id, name, latest_version) VALUES(?, ?, ?);`, id, name, version); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func captureCommandOutput(t *testing.T) *os.File {
	t.Helper()
	oldOut, oldErr := os.Stdout, os.Stderr
	out, err := os.CreateTemp(t.TempDir(), "update-output")
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout, os.Stderr = out, out
	t.Cleanup(func() {
		os.Stdout, os.Stderr = oldOut, oldErr
		_ = out.Close()
	})
	return out
}

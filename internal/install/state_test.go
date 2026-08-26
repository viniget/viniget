package install

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/viniget/viniget/internal/helper"
)

func TestDBRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "installed.json")

	db, err := OpenDB(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(db.All()) != 0 {
		t.Fatal("a missing state file should open as an empty list, not an error")
	}

	db.Put(Record{ID: "A.One", Version: "1", Status: StatusOK, InstalledAt: time.Now().Add(-time.Hour)})
	db.Put(Record{ID: "B.Two", Version: "2", Status: StatusTimeoutInstalled, InstalledAt: time.Now()})
	if err := db.Save(); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenDB(path)
	if err != nil {
		t.Fatal(err)
	}
	all := reopened.All()
	if len(all) != 2 {
		t.Fatalf("records = %v", all)
	}
	// Most recently installed first: that is the order a user scans for
	// "what did I just do".
	if all[0].ID != "B.Two" {
		t.Errorf("order = %s, %s", all[0].ID, all[1].ID)
	}
	if _, ok := reopened.Get("A.One"); !ok {
		t.Error("A.One went missing")
	}

	reopened.Remove("A.One")
	if _, ok := reopened.Get("A.One"); ok {
		t.Error("Remove did not remove")
	}
}

func TestDBSaveIsAtomic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "installed.json")
	db, _ := OpenDB(path)
	db.Put(Record{ID: "A.One", InstalledAt: time.Now()})
	if err := db.Save(); err != nil {
		t.Fatal(err)
	}
	// No temp file may survive a successful save; a stale one would be read
	// by nothing but would suggest a crash that never happened.
	if _, err := os.Stat(path + ".tmp"); err == nil {
		t.Error("the temp file survived the save")
	}
}

func TestRecordOf(t *testing.T) {
	res := &Result{
		ID: "X.Y", Version: "3", Name: "Ex Why", InstallerType: "inno",
		Status: StatusOK, Prefix: "/p", Entry: "/p/app.exe", NewExecutables: 2,
		Helpers: []helper.Result{{Verb: "dotnet48", Status: helper.StatusInstalled}},
	}
	r := RecordOf(res)
	if r.ID != "X.Y" || r.InstallerType != "inno" || r.Entry != "/p/app.exe" || r.NewExecutables != 2 {
		t.Errorf("RecordOf lost fields: %+v", r)
	}
	if len(r.Helpers) != 1 || r.Helpers[0] != "dotnet48" {
		t.Errorf("RecordOf lost helpers: %+v", r.Helpers)
	}
}

func TestOldRecordWithoutHelpersIsBackwardCompatible(t *testing.T) {
	path := filepath.Join(t.TempDir(), "installed.json")
	if err := os.WriteFile(path, []byte(`[{"id":"Old.App","version":"1","status":"ok","prefix":"/p","installed_at":"2026-01-01T00:00:00Z"}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	db, err := OpenDB(path)
	if err != nil {
		t.Fatal(err)
	}
	rec, ok := db.Get("Old.App")
	if !ok || rec.Helpers != nil {
		t.Errorf("old record = %+v, %v", rec, ok)
	}
}

func TestStatusInstalled(t *testing.T) {
	// The whole point of scoring on the filesystem: a timeout is a pass.
	if !StatusTimeoutInstalled.Installed() {
		t.Error("timeout-but-installed is a successful install")
	}
	if !StatusOK.Installed() {
		t.Error("ok is a successful install")
	}
	for _, s := range []Status{StatusFilesNoExe, StatusNoFiles, StatusDownloadFailed, StatusUnsupported, StatusError} {
		if s.Installed() {
			t.Errorf("%s should not count as installed", s)
		}
	}
}

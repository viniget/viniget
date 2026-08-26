package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/viniget/viniget/internal/install"
	"github.com/viniget/viniget/internal/wine"
)

func TestWithFlagIsRepeatable(t *testing.T) {
	var got stringList
	if err := got.Set("dotnet48"); err != nil {
		t.Fatal(err)
	}
	if err := got.Set("dotnetdesktop8"); err != nil {
		t.Fatal(err)
	}
	want := stringList{"dotnet48", "dotnetdesktop8"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("with flags = %v, want %v", got, want)
	}
}

func TestResetHelpersRequiresForce(t *testing.T) {
	if got := cmdInstall([]string{"Vendor.Demo", "-reset-helpers"}); got != 2 {
		t.Errorf("exit = %d, want usage error 2", got)
	}
}

func TestHelperCommandUpdatesStateAndRollsBackFailure(t *testing.T) {
	bin := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cp, err := exec.LookPath("cp")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(cp, filepath.Join(bin, "cp")); err != nil {
		t.Fatal(err)
	}
	write("wine", `
if [ "$1" = --version ]; then printf '%s\n' wine-test; fi
exit 0
`)
	write("wineserver", "exit 0\n")
	write("xvfb-run", `
while [ "$#" -gt 0 ]; do
  case "$1" in
    -a) shift ;;
    -s) shift 2 ;;
    *) exec "$@" ;;
  esac
done
`)
	write("winetricks", `
if [ "$1" = list-all ]; then
  printf '%s\n' 'dotnet48 Framework' 'dotnet8 Runtime'
  exit 0
fi
for arg in "$@"; do verb="$arg"; done
/usr/bin/mkdir -p "$WINEPREFIX/drive_c/Program Files/Runtime"
printf partial > "$WINEPREFIX/drive_c/Program Files/Runtime/$verb.exe"
if [ "$verb" = dotnet8 ]; then exit 9; fi
exit 0
`)
	t.Setenv("PATH", bin)

	data := t.TempDir()
	store := wine.NewStore(data)
	prefix, err := store.For("Vendor.Demo")
	if err != nil {
		t.Fatal(err)
	}
	app := filepath.Join(prefix, "drive_c", "Program Files", "Demo", "app.exe")
	if err := os.MkdirAll(filepath.Dir(app), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(app, []byte("app"), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := install.OpenDB(install.StatePath(data))
	if err != nil {
		t.Fatal(err)
	}
	db.Put(install.Record{
		ID: "Vendor.Demo", Version: "1.0", Status: install.StatusOK,
		Prefix: prefix, Architecture: "x64", InstalledAt: time.Now(),
	})
	if err := db.Save(); err != nil {
		t.Fatal(err)
	}

	oldOut, oldErr := os.Stdout, os.Stderr
	commandOutput, err := os.CreateTemp(t.TempDir(), "command-output")
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout, os.Stderr = commandOutput, commandOutput
	t.Cleanup(func() {
		os.Stdout, os.Stderr = oldOut, oldErr
		_ = commandOutput.Close()
	})

	if code := cmdHelper([]string{"Vendor.Demo", "dotnet48", "-data", data, "-timeout", "1s"}); code != 0 {
		t.Fatalf("successful helper exit = %d", code)
	}
	if _, err := os.Stat(app); err != nil {
		t.Errorf("helper transaction lost application: %v", err)
	}
	if _, err := os.Stat(filepath.Join(prefix, "drive_c", "Program Files", "Runtime", "dotnet48.exe")); err != nil {
		t.Errorf("successful helper did not reach live prefix: %v", err)
	}
	db, err = install.OpenDB(install.StatePath(data))
	if err != nil {
		t.Fatal(err)
	}
	rec, ok := db.Get("Vendor.Demo")
	if !ok || !reflect.DeepEqual(rec.Helpers, []string{"dotnet48"}) {
		t.Fatalf("recorded helpers = %v, found=%v", rec.Helpers, ok)
	}

	if code := cmdHelper([]string{"Vendor.Demo", "dotnet48", "-data", data}); code != 0 {
		t.Errorf("already-recorded helper exit = %d", code)
	}
	if code := cmdHelper([]string{"Vendor.Demo", "dotnet8", "-data", data, "-timeout", "1s"}); code != 1 {
		t.Errorf("failed helper exit = %d", code)
	}
	if _, err := os.Stat(filepath.Join(prefix, "drive_c", "Program Files", "Runtime", "dotnet8.exe")); !os.IsNotExist(err) {
		t.Errorf("failed helper leaked into live prefix: %v", err)
	}
	db, err = install.OpenDB(install.StatePath(data))
	if err != nil {
		t.Fatal(err)
	}
	rec, _ = db.Get("Vendor.Demo")
	if !reflect.DeepEqual(rec.Helpers, []string{"dotnet48"}) {
		t.Errorf("failed helper changed state: %v", rec.Helpers)
	}
	if code := cmdHelperList([]string{"-json"}); code != 0 {
		t.Errorf("helper list exit = %d", code)
	}
	if err := commandOutput.Sync(); err != nil {
		t.Fatal(err)
	}
	listed, err := os.ReadFile(commandOutput.Name())
	if err != nil {
		t.Fatal(err)
	}
	if !containsAll(string(listed), `"verb": "dotnet48"`, `"available": true`) {
		t.Errorf("helper list JSON missing catalog availability: %s", listed)
	}

	if code := cmdHelper([]string{"Missing.Package", "dotnet48", "-data", data}); code != 1 {
		t.Errorf("missing package exit = %d", code)
	}
	db.Put(install.Record{ID: "Missing.Prefix", Prefix: filepath.Join(data, "not-there")})
	if err := db.Save(); err != nil {
		t.Fatal(err)
	}
	if code := cmdHelper([]string{"Missing.Prefix", "dotnet48", "-data", data}); code != 1 {
		t.Errorf("missing prefix exit = %d", code)
	}
}

func containsAll(s string, values ...string) bool {
	for _, value := range values {
		if !strings.Contains(s, value) {
			return false
		}
	}
	return true
}

func TestHelperListRejectsUnexpectedArguments(t *testing.T) {
	if got := cmdHelperList([]string{"extra"}); got != 2 {
		t.Errorf("exit = %d, want usage error 2", got)
	}
}

package install

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/viniget/viniget/internal/helper"
	"github.com/viniget/viniget/internal/manifest"
	"github.com/viniget/viniget/internal/wine"
)

func TestTimeoutDefaultsByInstallMode(t *testing.T) {
	opt := Options{}
	if got := opt.timeout(false); got != DefaultTimeout {
		t.Errorf("unattended timeout = %s, want %s", got, DefaultTimeout)
	}
	if got := opt.timeout(true); got != DefaultInteractiveTimeout {
		t.Errorf("interactive timeout = %s, want %s", got, DefaultInteractiveTimeout)
	}

	opt.Timeout = 37 * time.Second
	for _, interactive := range []bool{false, true} {
		if got := opt.timeout(interactive); got != opt.Timeout {
			t.Errorf("explicit timeout for interactive=%v = %s, want %s", interactive, got, opt.Timeout)
		}
	}
}

func TestDriveInteractiveExeUsesManifestArgs(t *testing.T) {
	bin := t.TempDir()
	argvPath := filepath.Join(t.TempDir(), "argv")
	winePath := filepath.Join(bin, "wine")
	if err := os.WriteFile(winePath, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$VINIGET_TEST_ARGV\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("VINIGET_TEST_ARGV", argvPath)

	prefix := t.TempDir()
	choice := &Choice{
		Type:        "exe",
		Interactive: true,
		Switches: &manifest.Switches{
			Interactive: `/MODE="guided install" /DIR=<INSTALLPATH>`,
			Custom:      `/LOG=<LOGPATH>`,
		},
	}
	res := &Result{ID: "Vendor.Demo"}
	runner := &wine.Runner{Prefix: prefix, Display: wine.Display{Kind: "host"}}
	outcome, err := drive(context.Background(), runner, choice, "/downloads/demo.exe", prefix,
		Options{Timeout: time.Second}, res)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.ExitCode != 0 || outcome.TimedOut {
		t.Fatalf("outcome = %+v", outcome)
	}

	b, err := os.ReadFile(argvPath)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSpace(string(b)), "\n")
	want := []string{
		"/downloads/demo.exe",
		"/MODE=guided install",
		"/DIR=" + programFiles(prefix, res.ID),
		"/LOG=" + filepath.Join(prefix, "drive_c", "windows", "temp", "viniget-install.log"),
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("wine argv = %q, want %q", got, want)
	}
	if len(res.Notes) != 2 || !strings.Contains(res.Notes[1], "host display") {
		t.Errorf("notes = %v, want the interactive mode to be visible", res.Notes)
	}
}

func TestInteractiveExeForcesHostDisplay(t *testing.T) {
	t.Setenv("DISPLAY", ":77")
	configured := wine.Display{Wrapper: []string{"xvfb-run", "-a"}, Kind: "xvfb"}
	got := installDisplay(&Choice{Type: "exe", Interactive: true}, configured)
	if got.Kind != "host" || got.Value != ":77" || len(got.Wrapper) != 0 {
		t.Errorf("interactive display = %+v, want the unwrapped host display", got)
	}

	got = installDisplay(&Choice{Type: "exe"}, configured)
	if got.Kind != "xvfb" || len(got.Wrapper) == 0 {
		t.Errorf("unattended display = %+v, want the configured Xvfb", got)
	}
}

func TestInteractiveTimeoutKillsWineSession(t *testing.T) {
	bin := t.TempDir()
	argvPath := filepath.Join(t.TempDir(), "argv")
	wineserverPath := filepath.Join(bin, "wineserver")
	if err := os.WriteFile(wineserverPath,
		[]byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$VINIGET_TEST_ARGV\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("VINIGET_TEST_ARGV", argvPath)

	res := &Result{}
	runner := &wine.Runner{Prefix: t.TempDir(), Display: wine.Display{Kind: "host"}}
	finishWine(context.Background(), runner, true, wine.Result{TimedOut: true}, res)

	b, err := os.ReadFile(argvPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(b)); got != "-k" {
		t.Errorf("wineserver args = %q, want -k", got)
	}
	if len(res.Notes) != 1 || !strings.Contains(res.Notes[0], "closed its Wine session") {
		t.Errorf("notes = %v", res.Notes)
	}
}

func TestInteractiveExeRequiresDisplayBeforeWineOrDownload(t *testing.T) {
	t.Setenv("DISPLAY", "")
	pkg := &manifest.Package{
		ID:      "Vendor.Demo",
		Version: "1.0",
		Installer: &manifest.InstallerManifest{
			InstallerType: "exe",
			Installers: []manifest.Installer{{
				Architecture: "x64",
				InstallerURL: "https://example.test/demo.exe",
			}},
		},
	}
	res, err := Run(context.Background(), pkg, Options{})
	if err == nil || !strings.Contains(err.Error(), "DISPLAY is unset") {
		t.Fatalf("Run error = %v, want an actionable display error", err)
	}
	if res == nil || res.Status != StatusError || !res.Interactive {
		t.Errorf("result = %+v, want an interactive install-error", res)
	}
}

func helperInstallFixture(t *testing.T, failVerb string) (*wine.Store, *helper.Executor, *httptest.Server) {
	t.Helper()
	bin := t.TempDir()
	write := func(name, body string) string {
		path := filepath.Join(bin, name)
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
			t.Fatal(err)
		}
		return path
	}
	write("wine", `
if [ "$1" = --version ]; then echo wine-test; fi
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
	winetricks := write("winetricks", `
if [ "$1" = list-all ]; then
  printf '%s\n' 'dotnet35sp1 Framework' 'dotnet48 Framework'
  exit 0
fi
for arg in "$@"; do verb="$arg"; done
mkdir -p "$WINEPREFIX/drive_c/Program Files/Runtime"
printf MZ > "$WINEPREFIX/drive_c/Program Files/Runtime/$verb.exe"
if [ "$verb" = "`+failVerb+`" ]; then exit 9; fi
exit 0
`)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	executor, err := helper.NewExecutor(context.Background(), winetricks)
	if err != nil {
		t.Fatal(err)
	}

	store := wine.NewStore(t.TempDir())
	if err := os.MkdirAll(filepath.Join(store.Base(), "drive_c", "Program Files"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.Base(), "system.reg"), []byte("REGEDIT4\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write([]byte("MZ portable application"))
	}))
	t.Cleanup(srv.Close)
	return store, executor, srv
}

func portablePackage(url string, dependencies *manifest.Dependencies) *manifest.Package {
	return &manifest.Package{
		ID: "Vendor.Demo", Version: "1.0",
		Installer: &manifest.InstallerManifest{
			InstallerType: "portable", Dependencies: dependencies,
			Installers: []manifest.Installer{{Architecture: "x64", InstallerURL: url}},
		},
	}
}

func TestRequiredHelperRunsBeforeScoring(t *testing.T) {
	store, executor, srv := helperInstallFixture(t, "")
	res, err := Run(context.Background(), portablePackage(srv.URL+"/demo.exe", nil), Options{
		Prefixes: store, Cache: t.TempDir(), Display: wine.Display{Kind: "test"},
		ExplicitHelpers: []string{"dotnet48"}, HelperExecutor: executor,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != StatusOK || res.NewExecutables != 1 {
		t.Errorf("result = %+v; helper executable must be excluded from app scoring", res)
	}
	if len(res.Helpers) != 1 || res.Helpers[0].Status != helper.StatusInstalled {
		t.Errorf("helpers = %+v", res.Helpers)
	}
	for _, rel := range []string{
		"drive_c/Program Files/Runtime/dotnet48.exe",
		"drive_c/Program Files/Demo/demo.exe",
	} {
		if _, err := os.Stat(filepath.Join(res.Prefix, rel)); err != nil {
			t.Errorf("installed prefix missing %s: %v", rel, err)
		}
	}
}

func TestAutomaticHelperFailureRollsBackAndContinues(t *testing.T) {
	store, executor, srv := helperInstallFixture(t, "dotnet35sp1")
	deps := &manifest.Dependencies{WindowsFeatures: []string{"NetFx3"}}
	res, err := Run(context.Background(), portablePackage(srv.URL+"/demo.exe", deps), Options{
		Prefixes: store, Cache: t.TempDir(), Display: wine.Display{Kind: "test"},
		AutoHelpers: true, HelperExecutor: executor,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != StatusOK || len(res.Helpers) != 1 || res.Helpers[0].Status != helper.StatusFailed {
		t.Fatalf("result = %+v", res)
	}
	partial := filepath.Join(res.Prefix, "drive_c", "Program Files", "Runtime", "dotnet35sp1.exe")
	if _, err := os.Stat(partial); !os.IsNotExist(err) {
		t.Errorf("failed automatic helper leaked into app prefix: %v", err)
	}
	if _, err := os.Stat(filepath.Join(res.Prefix, "drive_c", "Program Files", "Demo", "demo.exe")); err != nil {
		t.Errorf("app installer did not continue: %v", err)
	}
}

func TestMissingWinetricksDoesNotBlockAutomaticHelper(t *testing.T) {
	store, executor, srv := helperInstallFixture(t, "")
	bin := filepath.Dir(executor.Binary)
	if err := os.Remove(executor.Binary); err != nil {
		t.Fatal(err)
	}
	cp, err := exec.LookPath("cp")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(cp, filepath.Join(bin, "cp")); err != nil {
		t.Fatal(err)
	}
	// Do not let the system Winetricks later in the original PATH satisfy the
	// lookup; every other executable this install needs is in the fake bin.
	t.Setenv("PATH", bin)
	deps := &manifest.Dependencies{WindowsFeatures: []string{"NetFx3"}}
	res, err := Run(context.Background(), portablePackage(srv.URL+"/demo.exe", deps), Options{
		Prefixes: store, Cache: t.TempDir(), Display: wine.Display{Kind: "test"}, AutoHelpers: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != StatusOK || len(res.Helpers) != 1 || res.Helpers[0].Status != helper.StatusUnavailable {
		t.Fatalf("result = %+v", res)
	}
}

func TestDisabledAutomaticHelpersSkipsDependencyInference(t *testing.T) {
	store, executor, srv := helperInstallFixture(t, "")
	deps := &manifest.Dependencies{WindowsFeatures: []string{"NetFx3"}}
	res, err := Run(context.Background(), portablePackage(srv.URL+"/demo.exe", deps), Options{
		Prefixes: store, Cache: t.TempDir(), Display: wine.Display{Kind: "test"},
		AutoHelpers: false, HelperExecutor: executor,
	})
	if err != nil || res.Status != StatusOK {
		t.Fatalf("result = %+v, err = %v", res, err)
	}
	if len(res.Helpers) != 0 {
		t.Errorf("disabled automatic helpers ran: %+v", res.Helpers)
	}
	runtime := filepath.Join(res.Prefix, "drive_c", "Program Files", "Runtime", "dotnet35sp1.exe")
	if _, err := os.Stat(runtime); !os.IsNotExist(err) {
		t.Errorf("disabled helper wrote runtime: %v", err)
	}
}

func TestExplicitHelperFailureAbortsBeforeAppInstall(t *testing.T) {
	store, executor, srv := helperInstallFixture(t, "dotnet35sp1")
	res, err := Run(context.Background(), portablePackage(srv.URL+"/demo.exe", nil), Options{
		Prefixes: store, Cache: t.TempDir(), Display: wine.Display{Kind: "test"},
		ExplicitHelpers: []string{"dotnet35sp1"}, HelperExecutor: executor,
	})
	if err == nil || res.Status != StatusError {
		t.Fatalf("result = %+v, err = %v", res, err)
	}
	final, _ := store.For("Vendor.Demo")
	if _, err := os.Stat(final); !os.IsNotExist(err) {
		t.Errorf("failed required helper installed the app prefix: %v", err)
	}
}

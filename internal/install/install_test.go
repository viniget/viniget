package install

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

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

package helper

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/viniget/viniget/internal/wine"
)

func writeTool(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func fakeTools(t *testing.T) (string, string) {
	t.Helper()
	bin := t.TempDir()
	writeTool(t, bin, "wineserver", "exit 0\n")
	writeTool(t, bin, "wine", "printf '%s\\n' 'Microsoft.NETCore.App 8.0.30 [C:\\dotnet]' 'Microsoft.WindowsDesktop.App 8.0.30 [C:\\dotnet]'\n")
	writeTool(t, bin, "xvfb-run", `
if [ -n "$VINIGET_TEST_XVFB" ]; then printf used > "$VINIGET_TEST_XVFB"; fi
while [ "$#" -gt 0 ]; do
  case "$1" in
    -a) shift ;;
    -s) shift 2 ;;
    *) exec "$@" ;;
  esac
done
`)
	winetricks := writeTool(t, bin, "winetricks", `
if [ "$1" = list-all ]; then
  printf '%s\n' 'dotnet48 Framework' 'dotnet8 Runtime' 'dotnet9 Runtime' 'dotnetdesktop8 Desktop'
  exit 0
fi
printf '%s\n' fake-helper-output
for arg in "$@"; do verb="$arg"; done
if [ -n "$VINIGET_TEST_ARGS" ]; then printf '%s|%s\n' "$*" "$DISPLAY" > "$VINIGET_TEST_ARGS"; fi
mkdir -p "$WINEPREFIX/drive_c/Program Files/Runtime"
printf partial > "$WINEPREFIX/drive_c/Program Files/Runtime/$verb.exe"
if [ "$verb" = dotnet8 ]; then exit 7; fi
if [ "$verb" = dotnet9 ]; then trap 'exit 0' TERM; while :; do :; done; fi
if [ "$verb" = dotnetdesktop8 ]; then
  mkdir -p "$WINEPREFIX/drive_c/Program Files/dotnet"
  printf MZ > "$WINEPREFIX/drive_c/Program Files/dotnet/dotnet.exe"
fi
exit 0
`)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return bin, winetricks
}

func TestExecutorTimeoutForceAndDisplay(t *testing.T) {
	_, binary := fakeTools(t)
	executor, err := NewExecutor(context.Background(), binary)
	if err != nil {
		t.Fatal(err)
	}
	argsPath := filepath.Join(t.TempDir(), "args")
	t.Setenv("VINIGET_TEST_ARGS", argsPath)
	var output bytes.Buffer
	got, err := executor.Apply(context.Background(), Request{Verb: "dotnet48", Source: SourceExplicit}, Options{
		Prefix: t.TempDir(), Display: wine.Display{Value: ":91", Kind: "test"},
		Timeout: time.Second, Force: true, Output: &output,
	})
	if err != nil || got.Status != StatusInstalled {
		t.Fatalf("forced helper = %+v, %v", got, err)
	}
	body, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	if text := string(body); !strings.Contains(text, "--force dotnet48") || !strings.Contains(text, "|:91") {
		t.Errorf("invocation = %q", text)
	}
	if !strings.Contains(output.String(), "fake-helper-output") {
		t.Errorf("helper output was not captured: %q", output.String())
	}

	xvfbPath := filepath.Join(t.TempDir(), "xvfb")
	t.Setenv("VINIGET_TEST_XVFB", xvfbPath)
	defaulted, err := executor.Apply(context.Background(), Request{Verb: "dotnet48", Source: SourceExplicit}, Options{
		Prefix: t.TempDir(), Timeout: time.Second,
	})
	if err != nil || defaulted.Status != StatusInstalled {
		t.Fatalf("default-display helper = %+v, %v", defaulted, err)
	}
	if _, err := os.Stat(xvfbPath); err != nil {
		t.Errorf("default helper did not use private Xvfb: %v", err)
	}

	timedOut, err := executor.Apply(context.Background(), Request{Verb: "dotnet9", Source: SourceExplicit}, Options{
		Prefix: t.TempDir(), Display: wine.Display{Kind: "test"}, Timeout: 30 * time.Millisecond,
	})
	if err == nil || timedOut.Status != StatusFailed || timedOut.ExitCode != "TIMEOUT" {
		t.Fatalf("timeout = %+v, %v", timedOut, err)
	}
}

func TestExecutorSuccessFailureAndRuntimeProbe(t *testing.T) {
	_, binary := fakeTools(t)
	executor, err := NewExecutor(context.Background(), binary)
	if err != nil {
		t.Fatal(err)
	}
	prefix := t.TempDir()
	opt := Options{Prefix: prefix, Display: wine.Display{Kind: "test"}, Timeout: time.Second}

	got, err := executor.Apply(context.Background(), Request{Verb: "dotnet48", Source: SourceExplicit}, opt)
	if err != nil || got.Status != StatusInstalled || got.ExitCode != "0" {
		t.Fatalf("dotnet48 = %+v, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(prefix, "drive_c", "Program Files", "Runtime", "dotnet48.exe")); err != nil {
		t.Errorf("helper did not write into the selected prefix: %v", err)
	}

	failed, err := executor.Apply(context.Background(), Request{Verb: "dotnet8", Source: SourceExplicit}, opt)
	if err == nil || failed.Status != StatusFailed || failed.ExitCode != "7" {
		t.Fatalf("dotnet8 = %+v, %v", failed, err)
	}

	probed, err := executor.Apply(context.Background(), Request{
		Verb: "dotnetdesktop8", Source: SourceDependency,
		RuntimeFamily: "Microsoft.WindowsDesktop.App", RuntimeMajor: 8,
		MinimumVersion: "8.0.21", Architecture: "x64",
	}, opt)
	if err != nil || probed.DetectedVersion != "8.0.30" {
		t.Fatalf("runtime probe = %+v, %v", probed, err)
	}

	insufficient, err := executor.Apply(context.Background(), Request{
		Verb: "dotnetdesktop8", Source: SourceDependency,
		RuntimeFamily: "Microsoft.WindowsDesktop.App", RuntimeMajor: 8,
		MinimumVersion: "8.0.31", Architecture: "x64",
	}, opt)
	if err == nil || insufficient.Status != StatusFailed || !strings.Contains(err.Error(), "required runtime") {
		t.Fatalf("insufficient runtime = %+v, %v", insufficient, err)
	}
	if insufficient.DetectedVersion != "8.0.30" {
		t.Errorf("insufficient runtime did not report detected version: %+v", insufficient)
	}
}

func TestExecutorRejectsVerbMissingFromHostWinetricks(t *testing.T) {
	_, binary := fakeTools(t)
	executor, err := NewExecutor(context.Background(), binary)
	if err != nil {
		t.Fatal(err)
	}
	got, err := executor.Apply(context.Background(), Request{Verb: "dotnet10"}, Options{Prefix: t.TempDir()})
	if err == nil || got.Status != StatusUnavailable || !strings.Contains(err.Error(), "update winetricks") {
		t.Fatalf("result = %+v, err = %v", got, err)
	}
}

func TestApplyExistingIsAtomic(t *testing.T) {
	_, binary := fakeTools(t)
	executor, _ := NewExecutor(context.Background(), binary)
	store := wine.NewStore(t.TempDir())
	prefix, _ := store.For("Vendor.Demo")
	if err := os.MkdirAll(filepath.Join(prefix, "drive_c", "Program Files", "Demo"), 0o755); err != nil {
		t.Fatal(err)
	}
	app := filepath.Join(prefix, "drive_c", "Program Files", "Demo", "app.exe")
	if err := os.WriteFile(app, []byte("app"), 0o644); err != nil {
		t.Fatal(err)
	}
	opt := TransactionOptions{Options: Options{Display: wine.Display{Kind: "test"}, Timeout: time.Second}}
	if _, err := ApplyExisting(context.Background(), executor, store, "Vendor.Demo", []Request{{Verb: "dotnet48", Source: SourceExplicit}}, opt); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{app, filepath.Join(prefix, "drive_c", "Program Files", "Runtime", "dotnet48.exe")} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("successful transaction lost %s: %v", path, err)
		}
	}

	if _, err := ApplyExisting(context.Background(), executor, store, "Vendor.Demo", []Request{{Verb: "dotnet8", Source: SourceExplicit}}, opt); err == nil {
		t.Fatal("failing helper transaction succeeded")
	}
	if _, err := os.Stat(app); err != nil {
		t.Errorf("failed transaction damaged live app: %v", err)
	}
	if _, err := os.Stat(filepath.Join(prefix, "drive_c", "Program Files", "Runtime", "dotnet8.exe")); !os.IsNotExist(err) {
		t.Errorf("partial failed helper reached live prefix: %v", err)
	}
	if _, err := os.Stat(prefix + ".installing"); !os.IsNotExist(err) {
		t.Errorf("failed staging prefix survived: %v", err)
	}
}

package desktop

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/viniget/viniget/internal/wine"
)

func TestSplitWindowsArgs(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"--flag", []string{"--flag"}},
		{"  --a   --b  ", []string{"--a", "--b"}},
		{`"C:\Program Files\App\x.ini" --quiet`, []string{`C:\Program Files\App\x.ini`, "--quiet"}},
		// A backslash is a path separator here, not an escape. Reading it the
		// strict way would mangle every path a shortcut carries.
		{`C:\Users\me\file.txt`, []string{`C:\Users\me\file.txt`}},
		{`""`, []string{""}},
	}
	for _, c := range cases {
		got := SplitWindowsArgs(c.in)
		if len(got) != len(c.want) {
			t.Errorf("SplitWindowsArgs(%q) = %q, want %q", c.in, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("SplitWindowsArgs(%q)[%d] = %q, want %q", c.in, i, got[i], c.want[i])
			}
		}
	}
}

func TestShellQuote(t *testing.T) {
	cases := map[string]string{
		"/simple/path":     "'/simple/path'",
		"/with space/path": "'/with space/path'",
		"it's":             `'it'\''s'`,
	}
	for in, want := range cases {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLauncherScript(t *testing.T) {
	prefix := t.TempDir()
	exe := filepath.Join(prefix, "drive_c", "Program Files", "Demo's App", "demo.exe")
	if err := os.MkdirAll(filepath.Dir(exe), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, []byte("MZ"), 0o755); err != nil {
		t.Fatal(err)
	}
	// dosdevices is what lets the executable be addressed as C:\... rather
	// than through Z:, which is how the application sees itself installed.
	if err := os.MkdirAll(filepath.Join(prefix, "dosdevices"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../drive_c", filepath.Join(prefix, "dosdevices", "c:")); err != nil {
		t.Fatal(err)
	}

	l := &Launcher{ID: "Vendor.Demo", Prefix: prefix, Target: exe, Args: []string{"--kiosk"}}
	script := string(l.Script())

	for _, want := range []string{
		"#!/bin/sh\n",
		"WINEPREFIX=" + shellQuote(prefix) + "\n",
		"WINEDEBUG=${WINEDEBUG--all}\n",
		`exec wine 'C:\Program Files\Demo'\''s App\demo.exe' '--kiosk' "$@"`,
		"winepath -w",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("script missing %q:\n%s", want, script)
		}
	}

	path, err := l.Write(filepath.Join(t.TempDir(), "launchers"), LauncherName("Vendor.Demo", ""))
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Errorf("launcher is not executable: %v", info.Mode())
	}

	// The script has to be a valid one, and a syntax error in it would only
	// show up as a menu entry that silently does nothing.
	if _, err := exec.LookPath("sh"); err == nil {
		if out, err := exec.Command("sh", "-n", path).CombinedOutput(); err != nil {
			t.Errorf("sh -n rejected the launcher: %v: %s", err, out)
		}
	}
}

func TestLauncherConvertsArguments(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	dir := t.TempDir()
	// Stub wine and winepath so the conversion loop can be observed without
	// a wine installation.
	for name, body := range map[string]string{
		"winepath": "#!/bin/sh\necho 'Z:\\converted'\n",
		"wine":     "#!/bin/sh\nfor a in \"$@\"; do echo \"ARG:$a\"; done\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	real := filepath.Join(dir, "existing.txt")
	if err := os.WriteFile(real, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	l := &Launcher{ID: "Vendor.Demo", Prefix: dir, Target: filepath.Join(dir, "demo.exe"),
		Args: []string{"--flag"}, WorkingDir: dir}
	script, err := l.Write(dir, "launcher")
	if err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(script, real, "not-a-path")
	cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	got := string(out)
	if !strings.Contains(got, `ARG:Z:\converted`) {
		t.Errorf("an existing host path was not converted:\n%s", got)
	}
	if !strings.Contains(got, "ARG:not-a-path") {
		t.Errorf("a non-path argument was not passed through:\n%s", got)
	}
	if !strings.Contains(got, "ARG:--flag") {
		t.Errorf("the shortcut's own arguments were dropped:\n%s", got)
	}
	// Order matters: the shortcut's arguments come before the files.
	if strings.Index(got, "ARG:--flag") > strings.Index(got, "ARG:Z:") {
		t.Errorf("argument order is wrong:\n%s", got)
	}
}

// TestLauncherDLLOverrides covers the escape hatch for the few applications
// that will not start under wine without one. The value has to reach wine on
// every launch, and the environment has to be able to take it back off for a
// single run -- checking whether the wine bug behind it is still there is the
// whole reason somebody would want to.
func TestLauncherDLLOverrides(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	dir := t.TempDir()
	stub := "#!/bin/sh\necho \"OVERRIDES:[$WINEDLLOVERRIDES]\"\n"
	if err := os.WriteFile(filepath.Join(dir, "wine"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}

	l := &Launcher{ID: "Vendor.Demo", Prefix: dir, Target: filepath.Join(dir, "demo.exe"),
		WorkingDir: dir, DLLOverrides: "sapi=d"}
	script, err := l.Write(dir, "launcher")
	if err != nil {
		t.Fatal(err)
	}

	// A hermetic environment rather than the ambient one: whether this passes
	// must not depend on the WINEDLLOVERRIDES of whoever runs the tests.
	run := func(env ...string) string {
		cmd := exec.Command(script)
		cmd.Env = append([]string{"PATH=" + dir + ":" + os.Getenv("PATH")}, env...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %s", err, out)
		}
		return string(out)
	}

	if got := run(); !strings.Contains(got, "sapi=d") {
		t.Errorf("the override did not reach wine:\n%s", got)
	}
	if got := run("WINEDLLOVERRIDES=other=n"); !strings.Contains(got, "OVERRIDES:[other=n]") {
		t.Errorf("the environment did not win over the default:\n%s", got)
	}
	if got := run("WINEDLLOVERRIDES="); !strings.Contains(got, "OVERRIDES:[]") {
		t.Errorf("an empty environment value did not drop the override:\n%s", got)
	}

	// The menu builder goes off on every launch, package override or not: an
	// application that writes its own Start Menu shortcut at first run would
	// otherwise put a second, wine-generated entry beside viniget's.
	if got := run(); !strings.Contains(got, wine.NoMenuBuilder) {
		t.Errorf("the menu builder was not disabled:\n%s", got)
	}
	plain := &Launcher{ID: "Vendor.Demo", Prefix: dir, Target: filepath.Join(dir, "demo.exe")}
	if got := string(plain.Script()); !strings.Contains(got, wine.NoMenuBuilder) {
		t.Errorf("a launcher with no package override still disables the menu builder:\n%s", got)
	}
}

func TestLauncherEnvironmentQuirk(t *testing.T) {
	dir := t.TempDir()
	l := &Launcher{
		ID:         "VNGCorp.Zalo",
		Prefix:     dir,
		Target:     filepath.Join(dir, "Zalo.exe"),
		WorkingDir: dir,
	}
	script := string(l.Script())

	for _, want := range []string{
		"unset MANGOHUD ELECTRON_RUN_AS_NODE ELECTRON_NO_ATTACH_CONSOLE\n",
		"WINENTSYNC=${WINENTSYNC-'0'}\nexport WINENTSYNC\n",
		"WINEESYNC=${WINEESYNC-'0'}\nexport WINEESYNC\n",
		"WINEFSYNC=${WINEFSYNC-'0'}\nexport WINEFSYNC\n",
		"if [ -x '/opt/wine-cachyos/bin/wine' ]; then\n",
		"\tPATH='/opt/wine-cachyos/bin':$PATH\n",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("Zalo launcher missing %q:\n%s", want, script)
		}
	}

	if _, err := exec.LookPath("sh"); err == nil {
		path, err := l.Write(dir, "zalo-launcher")
		if err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command("sh", "-n", path).CombinedOutput(); err != nil {
			t.Errorf("sh -n rejected the Zalo launcher: %v: %s", err, out)
		}
	}
}

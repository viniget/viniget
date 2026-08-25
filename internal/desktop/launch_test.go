package desktop

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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

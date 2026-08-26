package install

import (
	"reflect"
	"testing"

	"github.com/viniget/viniget/internal/manifest"
)

func TestSplitArgs(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"/S", []string{"/S"}},
		{"/VERYSILENT /NORESTART", []string{"/VERYSILENT", "/NORESTART"}},
		{"  /q   /norestart  ", []string{"/q", "/norestart"}},
		{"", nil},
		// The prototype's str.split() broke this one apart.
		{`/DIR="C:\Program Files\App"`, []string{`/DIR=C:\Program Files\App`}},
		{`/S /D="C:\Tools"`, []string{"/S", `/D=C:\Tools`}},
		// An empty quoted argument is still an argument.
		{`/L=""`, []string{"/L="}},
	}
	for _, c := range cases {
		if got := splitArgs(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("splitArgs(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSilentArgsDefaults(t *testing.T) {
	// The whole point of the table: most manifests declare nothing.
	args, ok := SilentArgs("nullsoft", nil)
	if !ok || !reflect.DeepEqual(args, []string{"/S"}) {
		t.Errorf("nullsoft default = %q, %v", args, ok)
	}
	args, ok = SilentArgs("inno", nil)
	if !ok || len(args) != 4 || args[0] != "/VERYSILENT" {
		t.Errorf("inno default = %q, %v", args, ok)
	}
	args, ok = SilentArgs("burn", nil)
	if !ok || !reflect.DeepEqual(args, []string{"/quiet", "/norestart"}) {
		t.Errorf("burn default = %q, %v", args, ok)
	}
}

func TestSilentArgsDeclaredWins(t *testing.T) {
	sw := &manifest.Switches{Silent: "/quiet /norestart"}
	args, ok := SilentArgs("inno", sw)
	if !ok || !reflect.DeepEqual(args, []string{"/quiet", "/norestart"}) {
		t.Fatalf("declared switches should override the table, got %q", args)
	}
	if !Declared(sw) {
		t.Error("Declared should report a manifest-supplied switch")
	}
	if Declared(nil) {
		t.Error("Declared should be false with no switches")
	}
}

func TestSilentArgsFallsBackToSilentWithProgress(t *testing.T) {
	args, ok := SilentArgs("exe", &manifest.Switches{SilentWithProgress: "/verysilent"})
	if !ok || !reflect.DeepEqual(args, []string{"/verysilent"}) {
		t.Fatalf("SilentWithProgress should resolve, got %q %v", args, ok)
	}
}

func TestCustomSwitchesAppend(t *testing.T) {
	args, ok := SilentArgs("nullsoft", &manifest.Switches{Custom: "/ALLUSERS"})
	if !ok || !reflect.DeepEqual(args, []string{"/S", "/ALLUSERS"}) {
		t.Errorf("custom switches append to the default, got %q", args)
	}
	// msi carries no payload switches of its own but still takes Custom.
	args, ok = SilentArgs("msi", &manifest.Switches{Custom: "ALLUSERS=1"})
	if !ok || !reflect.DeepEqual(args, []string{"ALLUSERS=1"}) {
		t.Errorf("msi custom = %q, %v", args, ok)
	}
}

func TestNeverGuessesForBareExe(t *testing.T) {
	if _, ok := SilentArgs("exe", nil); ok {
		t.Error("viniget must never guess switches for an undeclared exe")
	}
	if !Supported("exe", nil) {
		t.Error("an undeclared exe should be supported through its interactive UI")
	}
	if !IsInteractive("exe", nil) {
		t.Error("an undeclared exe should be classified as interactive")
	}
	if IsInteractive("exe", &manifest.Switches{Silent: "/S"}) {
		t.Error("a declared silent exe should remain unattended")
	}
}

func TestInteractiveArgs(t *testing.T) {
	sw := &manifest.Switches{
		Interactive: `/MODE="guided install" /DIR=<INSTALLPATH>`,
		Custom:      `/LOG=<LOGPATH>`,
	}
	args, ok := InteractiveArgs("exe", sw)
	if !ok {
		t.Fatal("generic exe without silent switches should have an interactive mode")
	}
	args = expandPlaceholders(args, `C:\temp\install.log`, `C:\Program Files\Demo`)
	want := []string{
		`/MODE=guided install`,
		`/DIR=C:\Program Files\Demo`,
		`/LOG=C:\temp\install.log`,
	}
	if !reflect.DeepEqual(args, want) {
		t.Errorf("interactive args = %q, want %q", args, want)
	}

	args, ok = InteractiveArgs("exe", nil)
	if !ok || len(args) != 0 {
		t.Errorf("bare exe interactive args = %q, %v; want an empty successful result", args, ok)
	}
	if _, ok := InteractiveArgs("exe", &manifest.Switches{Silent: "/S"}); ok {
		t.Error("a silent exe must not resolve to interactive arguments")
	}
}

func TestSupportedTypes(t *testing.T) {
	for _, tt := range []string{"exe", "nullsoft", "inno", "burn", "msi", "wix", "portable", "zip"} {
		if !Supported(tt, nil) {
			t.Errorf("%s should be supported", tt)
		}
	}
	for _, tt := range []string{"msix", "appx", "msstore", "pwa"} {
		if Supported(tt, nil) {
			t.Errorf("%s is structurally unsupportable under Wine", tt)
		}
		if !IsUnsupported(tt) {
			t.Errorf("%s should be flagged unsupported", tt)
		}
	}
	if _, ok := SilentArgs("zip", nil); ok {
		t.Error("archives are extracted, not driven with switches")
	}
	if _, ok := SilentArgs("msix", nil); ok {
		t.Error("msix must not resolve to switches")
	}
}

func TestExpandPlaceholders(t *testing.T) {
	got := expandPlaceholders([]string{"/log=<LOGPATH>", "/dir=<INSTALLPATH>", "/S"}, "/tmp/i.log", "/opt/app")
	want := []string{"/log=/tmp/i.log", "/dir=/opt/app", "/S"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("expandPlaceholders = %q, want %q", got, want)
	}
}

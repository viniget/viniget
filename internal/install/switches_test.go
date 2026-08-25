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
	if AllowDeclaredExe {
		if !ok || !reflect.DeepEqual(args, []string{"/verysilent"}) {
			t.Fatalf("with the exe policy on, a declared switch should be used, got %q %v", args, ok)
		}
		return
	}
	// With the v1 policy, a declared exe switch is still resolvable -- the
	// refusal lives in Supported, which is what Select consults.
	if !ok {
		t.Fatalf("SilentWithProgress should resolve, got %v", ok)
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
	if Supported("exe", nil) {
		t.Error("an undeclared exe is not supported")
	}
	if !AllowDeclaredExe && Supported("exe", &manifest.Switches{Silent: "/S"}) {
		t.Error("v1 policy is to skip exe even when switches are declared")
	}
}

func TestSupportedTypes(t *testing.T) {
	for _, tt := range []string{"nullsoft", "inno", "burn", "msi", "wix", "portable", "zip"} {
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

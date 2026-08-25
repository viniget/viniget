package install

import "testing"

func TestObsidianDisablesPowerShellDuringInstall(t *testing.T) {
	got := installQuirkFor("Obsidian.Obsidian").DLLOverrides
	if got != "powershell.exe=d" {
		t.Errorf("DLLOverrides = %q, want powershell.exe=d", got)
	}
}

func TestUnknownPackageHasNoInstallQuirk(t *testing.T) {
	got := installQuirkFor("Example.NoQuirk")
	if got != (installQuirk{}) {
		t.Errorf("quirk = %+v, want the zero value", got)
	}
}

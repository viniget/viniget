package install

import (
	"errors"
	"testing"

	"github.com/viniget/viniget/internal/manifest"
)

func TestSelectPrefersX64(t *testing.T) {
	m := &manifest.InstallerManifest{
		InstallerType: "nullsoft",
		Installers: []manifest.Installer{
			{Architecture: "x86", InstallerURL: "http://e/32.exe"},
			{Architecture: "x64", InstallerURL: "http://e/64.exe"},
		},
	}
	c, err := Select(m)
	if err != nil {
		t.Fatal(err)
	}
	if c.URL != "http://e/64.exe" {
		t.Errorf("picked %s, want the x64 variant", c.URL)
	}
	if c.Type != "nullsoft" {
		t.Errorf("type = %q, want nullsoft inherited from the manifest top level", c.Type)
	}
}

func TestSelectPrefersMachineScope(t *testing.T) {
	m := &manifest.InstallerManifest{
		InstallerType: "inno",
		Installers: []manifest.Installer{
			{Architecture: "x64", Scope: "user", InstallerURL: "http://e/user.exe"},
			{Architecture: "x64", Scope: "machine", InstallerURL: "http://e/machine.exe"},
		},
	}
	c, err := Select(m)
	if err != nil {
		t.Fatal(err)
	}
	if c.URL != "http://e/machine.exe" {
		t.Errorf("picked %s; machine scope is what registers the common Start Menu", c.URL)
	}
}

// The archive penalty is the design decision most likely to look like a bug,
// so it gets its own test: an installer beats an archive even when the archive
// is the better architecture.
func TestSelectPenalisesArchives(t *testing.T) {
	m := &manifest.InstallerManifest{
		Installers: []manifest.Installer{
			{Architecture: "x64", InstallerType: "portable", InstallerURL: "http://e/app.exe"},
			{Architecture: "x86", InstallerType: "nullsoft", InstallerURL: "http://e/setup.exe"},
		},
	}
	c, err := Select(m)
	if err != nil {
		t.Fatal(err)
	}
	if c.Type != "nullsoft" {
		t.Errorf("picked %q; the installer registers shortcuts an archive cannot", c.Type)
	}
}

func TestSelectFallsBackToArchive(t *testing.T) {
	m := &manifest.InstallerManifest{
		Installers: []manifest.Installer{
			{Architecture: "x64", InstallerType: "msix", InstallerURL: "http://e/app.msix"},
			{Architecture: "x64", InstallerType: "portable", InstallerURL: "http://e/app.exe"},
		},
	}
	c, err := Select(m)
	if err != nil {
		t.Fatal(err)
	}
	if c.Type != "portable" {
		t.Errorf("type = %q, want the portable fallback", c.Type)
	}
}

func TestSelectResolvesNestedType(t *testing.T) {
	m := &manifest.InstallerManifest{
		Installers: []manifest.Installer{{
			Architecture:         "x64",
			InstallerType:        "zip",
			NestedInstallerType:  "inno",
			NestedInstallerFiles: []manifest.NestedFile{{RelativeFilePath: `setup\app.exe`}},
			InstallerURL:         "http://e/app.zip",
		}},
	}
	c, err := Select(m)
	if err != nil {
		t.Fatal(err)
	}
	if c.Type != "inno" || c.Container != "zip" {
		t.Errorf("type/container = %q/%q, want inno/zip", c.Type, c.Container)
	}
	if len(c.Nested) != 1 {
		t.Errorf("nested files not carried through: %v", c.Nested)
	}
}

// SumatraPDF declares NestedInstallerFiles on a "portable" entry whose payload
// is a zip. Those files are the only statement of its entry point, so they
// have to survive selection even though no zip container was declared.
func TestSelectCarriesNestedFilesWithoutAZipContainer(t *testing.T) {
	m := &manifest.InstallerManifest{
		Installers: []manifest.Installer{{
			Architecture:         "x64",
			InstallerType:        "portable",
			NestedInstallerType:  "portable",
			NestedInstallerFiles: []manifest.NestedFile{{RelativeFilePath: "App-64.exe"}},
			InstallerURL:         "http://e/app.zip",
		}},
	}
	c, err := Select(m)
	if err != nil {
		t.Fatal(err)
	}
	if c.Container != "" {
		t.Errorf("container = %q, want empty until the payload is inspected", c.Container)
	}
	if len(c.Nested) != 1 || c.Nested[0].RelativeFilePath != "App-64.exe" {
		t.Errorf("nested files = %v", c.Nested)
	}
}

func TestSelectSkipsUnrunnableArchitectures(t *testing.T) {
	m := &manifest.InstallerManifest{
		InstallerType: "nullsoft",
		Installers: []manifest.Installer{
			{Architecture: "arm64", InstallerURL: "http://e/arm.exe"},
			{Architecture: "neutral", InstallerURL: "http://e/any.exe"},
		},
	}
	c, err := Select(m)
	if err != nil {
		t.Fatal(err)
	}
	if c.Arch != "neutral" {
		t.Errorf("arch = %q; arm64 does not run under x86 Wine", c.Arch)
	}
}

func TestSelectRejectsUnsupportedOnly(t *testing.T) {
	m := &manifest.InstallerManifest{
		Installers: []manifest.Installer{
			{Architecture: "x64", InstallerType: "msix", InstallerURL: "http://e/a.msix"},
			{Architecture: "x64", InstallerType: "exe", InstallerURL: "http://e/a.exe"},
		},
	}
	_, err := Select(m)
	var unsupported *UnsupportedError
	if !errors.As(err, &unsupported) {
		t.Fatalf("err = %v, want *UnsupportedError", err)
	}
	// The message has to say what was on offer, or the user cannot tell
	// "viniget declines this" from "the package is broken".
	if len(unsupported.Types) != 2 {
		t.Errorf("types = %v, want both offered types listed", unsupported.Types)
	}
}

func TestSelectInheritsTopLevelValues(t *testing.T) {
	m := &manifest.InstallerManifest{
		InstallerType:     "wix",
		Scope:             "machine",
		ProductCode:       "{TOP}",
		InstallerSwitches: &manifest.Switches{Custom: "ALLUSERS=1"},
		Installers: []manifest.Installer{
			{Architecture: "x64", InstallerURL: "http://e/a.msi", ProductCode: "{ENTRY}"},
		},
	}
	c, err := Select(m)
	if err != nil {
		t.Fatal(err)
	}
	if c.Scope != "machine" {
		t.Errorf("scope = %q, want the inherited machine", c.Scope)
	}
	if c.ProductCode != "{ENTRY}" {
		t.Errorf("product code = %q; the entry wins over the top level", c.ProductCode)
	}
	if c.Switches == nil || c.Switches.Custom != "ALLUSERS=1" {
		t.Errorf("switches not inherited: %+v", c.Switches)
	}
}

func TestSelectEmptyManifest(t *testing.T) {
	if _, err := Select(nil); err == nil {
		t.Error("a nil manifest should not select anything")
	}
	if _, err := Select(&manifest.InstallerManifest{}); err == nil {
		t.Error("a manifest with no installers should not select anything")
	}
}

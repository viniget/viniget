package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/viniget/viniget/internal/helper"
	"github.com/viniget/viniget/internal/install"
)

func TestInstallResultLabelsInteractiveExe(t *testing.T) {
	var out bytes.Buffer
	err := InstallResult(&out, &install.Result{
		ID:             "Vendor.Demo",
		Version:        "1.0",
		InstallerType:  "exe",
		Interactive:    true,
		Status:         install.StatusOK,
		NewFiles:       1,
		NewExecutables: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "exe (interactive)") {
		t.Errorf("output did not identify interactive mode:\n%s", out.String())
	}
}

func TestInstallResultRendersHelpers(t *testing.T) {
	var out bytes.Buffer
	err := InstallResult(&out, &install.Result{
		ID: "Vendor.Demo", Version: "1", InstallerType: "portable", Status: install.StatusOK,
		Helpers: []helper.Result{{Verb: "dotnet48", Status: helper.StatusInstalled}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Helpers") || !strings.Contains(out.String(), "dotnet48") {
		t.Errorf("helper missing from output:\n%s", out.String())
	}
}

func TestInstalledListRendersRecordedHelpers(t *testing.T) {
	var out bytes.Buffer
	if err := Installed(&out, []install.Record{{ID: "Vendor.Demo", Helpers: []string{"dotnet48"}}}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "HELPERS") || !strings.Contains(out.String(), "dotnet48") {
		t.Errorf("helper missing from list:\n%s", out.String())
	}
}

func TestInteractiveExeIsExposedInJSON(t *testing.T) {
	b, err := json.Marshal(&install.Result{InstallerType: "exe", Interactive: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"interactive":true`) {
		t.Errorf("JSON did not expose interactive mode: %s", b)
	}

	b, err = json.Marshal(&install.Result{InstallerType: "exe"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `"interactive"`) {
		t.Errorf("unattended JSON should omit interactive=false: %s", b)
	}
}

func TestInstallerLabelKeepsNestedContainer(t *testing.T) {
	got := installerLabel(&install.Result{
		InstallerType: "exe",
		Container:     "zip",
		Interactive:   true,
	})
	if got != "exe in zip (interactive)" {
		t.Errorf("installer label = %q", got)
	}
}

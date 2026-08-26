package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/viniget/viniget/internal/helper"
)

func TestHelperResultTextAndJSON(t *testing.T) {
	r := &helper.RunResult{
		ID: "Vendor.Demo", Prefix: "/prefix",
		Helpers: []helper.Result{{
			Verb: "dotnetdesktop8", Source: helper.SourceDependency,
			Status: helper.StatusInstalled, Dependency: "Microsoft.DotNet.DesktopRuntime.8",
			MinimumVersion: "8.0.21", DetectedVersion: "8.0.30", ExitCode: "0",
		}},
	}
	var out bytes.Buffer
	if err := HelperResult(&out, r); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"installed helper dotnetdesktop8", "Microsoft.DotNet.DesktopRuntime.8", "8.0.30"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output missing %q:\n%s", want, out.String())
		}
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"source":"dependency"`, `"minimum_version":"8.0.21"`, `"detected_version":"8.0.30"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("JSON missing %s: %s", want, b)
		}
	}
}

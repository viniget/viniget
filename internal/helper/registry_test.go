package helper

import (
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/viniget/viniget/internal/manifest"
)

func TestCuratedCatalogExcludesSDKAndVerifier(t *testing.T) {
	want := []string{
		"dotnet11", "dotnet11sp1", "dotnet20", "dotnet20sp1", "dotnet20sp2",
		"dotnet30", "dotnet30sp1", "dotnet35", "dotnet35sp1", "dotnet40",
		"dotnet40_kb2468871", "dotnet45", "dotnet452", "dotnet46", "dotnet461",
		"dotnet462", "dotnet471", "dotnet472", "dotnet48", "dotnetcore2",
		"dotnetcore3", "dotnetcoredesktop3", "dotnet6", "dotnet7", "dotnet8",
		"dotnet9", "dotnet10", "dotnetdesktop6", "dotnetdesktop7", "dotnetdesktop8",
		"dotnetdesktop9", "dotnetdesktop10",
	}
	var got []string
	for _, d := range Definitions() {
		got = append(got, d.Verb)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("catalog = %v, want %v", got, want)
	}
	for _, name := range []string{"dotnet48", "DOTNETDESKTOP10", " dotnetcore3 "} {
		if _, err := Lookup(name); err != nil {
			t.Errorf("Lookup(%q): %v", name, err)
		}
	}
	for _, name := range []string{"dotnet20sdk", "dotnet_verifier", "corefonts"} {
		if _, err := Lookup(name); err == nil {
			t.Errorf("Lookup(%q) accepted a non-runtime helper", name)
		}
	}
}

func TestEveryPackageDependencyMapping(t *testing.T) {
	type mapping struct {
		id, verb, family string
		major            int
	}
	tests := []mapping{
		{"Microsoft.DotNet.Framework.Redistributable.1_1", "dotnet11", "", 0},
		{"Microsoft.DotNet.Runtime.3_1", "dotnetcore3", "Microsoft.NETCore.App", 3},
		{"Microsoft.DotNet.DesktopRuntime.3_1", "dotnetcoredesktop3", "Microsoft.WindowsDesktop.App", 3},
	}
	for major := 6; major <= 10; major++ {
		m := strconv.Itoa(major)
		tests = append(tests,
			mapping{"Microsoft.DotNet.Runtime." + m, "dotnet" + m, "Microsoft.NETCore.App", major},
			mapping{"Microsoft.DotNet.DesktopRuntime." + m, "dotnetdesktop" + m, "Microsoft.WindowsDesktop.App", major},
		)
	}
	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			got, ok := packageRequest(manifest.PackageDependency{PackageIdentifier: strings.ToUpper(tt.id)}, "x64")
			if !ok || got.Verb != tt.verb || got.RuntimeFamily != tt.family || got.RuntimeMajor != tt.major {
				t.Errorf("mapping = %+v, %v; want %s, %s, %d", got, ok, tt.verb, tt.family, tt.major)
			}
		})
	}
}

func TestInferExactDotNetDependencies(t *testing.T) {
	deps := &manifest.Dependencies{
		PackageDependencies: []manifest.PackageDependency{
			{PackageIdentifier: "Microsoft.DotNet.Framework.Redistributable.1_1"},
			{PackageIdentifier: "Microsoft.DotNet.Runtime.3_1", MinimumVersion: "3.1.20"},
			{PackageIdentifier: "Microsoft.DotNet.DesktopRuntime.8", MinimumVersion: "8.0.21"},
			{PackageIdentifier: "Microsoft.DotNet.DesktopRuntime.8", MinimumVersion: "8.0.30"},
			{PackageIdentifier: "Microsoft.DotNet.AspNetCore.8"},
		},
		WindowsFeatures:  []string{"NetFx3", "DirectPlay"},
		WindowsLibraries: []string{"Example.Library"},
	}
	got, notes := Infer(deps, "x64")
	var verbs []string
	for _, r := range got {
		verbs = append(verbs, r.Verb)
		if r.Verb == "dotnetdesktop8" {
			if r.MinimumVersion != "8.0.30" || r.RuntimeFamily != "Microsoft.WindowsDesktop.App" || r.RuntimeMajor != 8 {
				t.Errorf("desktop request = %+v", r)
			}
		}
	}
	want := []string{"dotnet11", "dotnetcore3", "dotnetdesktop8", "dotnet35sp1"}
	if !reflect.DeepEqual(verbs, want) {
		t.Errorf("verbs = %v, want %v", verbs, want)
	}
	joined := strings.Join(notes, "\n")
	for _, wantNote := range []string{"AspNetCore.8", "DirectPlay", "Example.Library"} {
		if !strings.Contains(joined, wantNote) {
			t.Errorf("notes = %v, want %q", notes, wantNote)
		}
	}
}

func TestMergeSourcePrecedenceAndDeduplication(t *testing.T) {
	auto := []Request{
		{Verb: "dotnet48", Source: SourceDependency},
		{Verb: "dotnet8", Source: SourceDependency},
	}
	required, inferred, err := Merge([]string{"dotnet48"}, []string{"DOTNET48", "dotnet7"}, auto, "x86")
	if err != nil {
		t.Fatal(err)
	}
	if len(required) != 2 || required[0].Source != SourceRecorded || required[1].Verb != "dotnet7" {
		t.Errorf("required = %+v", required)
	}
	if len(inferred) != 1 || inferred[0].Verb != "dotnet8" {
		t.Errorf("automatic = %+v", inferred)
	}
}

func TestMatchingRuntimeHonorsFamilyMajorAndMinimum(t *testing.T) {
	output := "Microsoft.NETCore.App 8.0.30 [C:\\dotnet]\n" +
		"Microsoft.WindowsDesktop.App 8.0.12 [C:\\dotnet]\n" +
		"Microsoft.WindowsDesktop.App 9.0.1 [C:\\dotnet]\n"
	if got := matchingRuntime(output, "Microsoft.WindowsDesktop.App", 8, "8.0.10"); got != "8.0.12" {
		t.Errorf("matching runtime = %q", got)
	}
	if got := matchingRuntime(output, "Microsoft.WindowsDesktop.App", 8, "8.0.21"); got != "" {
		t.Errorf("insufficient runtime matched as %q", got)
	}
	if got := matchingRuntime(output, "Microsoft.WindowsDesktop.App", 8, "not-a-version"); got != "" {
		t.Errorf("unverifiable minimum matched as %q", got)
	}
}

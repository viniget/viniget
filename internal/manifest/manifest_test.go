package manifest

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func manifestResponse(req *http.Request, body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}
}

func TestGetAuthenticatesGitHubManifest(t *testing.T) {
	f := &Fetcher{
		GitHubToken: "secret",
		Client: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if got := req.Header.Get("Authorization"); got != "Bearer secret" {
				t.Errorf("Authorization = %q, want Bearer secret", got)
			}
			return manifestResponse(req, "manifest"), nil
		})},
	}

	body, err := f.get(context.Background(), "https://raw.githubusercontent.com/owner/repo/main/file")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if string(body) != "manifest" {
		t.Fatalf("body = %q, want manifest", body)
	}
}

func TestFetchCarriesDependenciesFromSingletonAndMultiFileManifests(t *testing.T) {
	t.Run("singleton", func(t *testing.T) {
		const singleton = `
PackageIdentifier: Vendor.Singleton
PackageVersion: 1.0
PackageLocale: en-US
PackageName: Singleton
InstallerType: exe
Dependencies:
  WindowsFeatures: [NetFx3]
Installers:
  - Architecture: x64
    InstallerUrl: https://example.test/app.exe
ManifestType: singleton
ManifestVersion: 1.10.0
`
		f := &Fetcher{Client: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return manifestResponse(req, singleton), nil
		})}}
		pkg, err := f.Fetch(context.Background(), "Vendor.Singleton", "1.0")
		if err != nil {
			t.Fatal(err)
		}
		deps := pkg.Installer.EffectiveDependencies(pkg.Installer.Installers[0])
		if !pkg.Singleton || deps == nil || len(deps.WindowsFeatures) != 1 || deps.WindowsFeatures[0] != "NetFx3" {
			t.Fatalf("singleton dependencies = %+v in %+v", deps, pkg)
		}
	})

	t.Run("multi-file", func(t *testing.T) {
		const version = `
PackageIdentifier: Vendor.Multi
PackageVersion: 2.0
DefaultLocale: en-US
ManifestType: version
ManifestVersion: 1.10.0
`
		const installer = `
PackageIdentifier: Vendor.Multi
PackageVersion: 2.0
InstallerType: exe
Dependencies:
  PackageDependencies:
    - PackageIdentifier: Microsoft.DotNet.Runtime.8
      MinimumVersion: 8.0.21
Installers:
  - Architecture: x64
    InstallerUrl: https://example.test/app.exe
ManifestType: installer
ManifestVersion: 1.10.0
`
		const locale = `
PackageIdentifier: Vendor.Multi
PackageVersion: 2.0
PackageLocale: en-US
PackageName: Multi
ManifestType: defaultLocale
ManifestVersion: 1.10.0
`
		f := &Fetcher{Client: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			switch {
			case strings.HasSuffix(req.URL.Path, ".installer.yaml"):
				return manifestResponse(req, installer), nil
			case strings.HasSuffix(req.URL.Path, ".locale.en-US.yaml"):
				return manifestResponse(req, locale), nil
			default:
				return manifestResponse(req, version), nil
			}
		})}}
		pkg, err := f.Fetch(context.Background(), "Vendor.Multi", "2.0")
		if err != nil {
			t.Fatal(err)
		}
		deps := pkg.Installer.EffectiveDependencies(pkg.Installer.Installers[0])
		if pkg.Singleton || deps == nil || len(deps.PackageDependencies) != 1 {
			t.Fatalf("multi-file dependencies = %+v in %+v", deps, pkg)
		}
		dep := deps.PackageDependencies[0]
		if dep.PackageIdentifier != "Microsoft.DotNet.Runtime.8" || dep.MinimumVersion != "8.0.21" {
			t.Errorf("package dependency = %+v", dep)
		}
	})
}

func TestDependenciesParseAndInstallerOverride(t *testing.T) {
	const body = `
Dependencies:
  PackageDependencies:
    - PackageIdentifier: Microsoft.DotNet.Runtime.8
      MinimumVersion: 8.0.21
  WindowsFeatures:
    - NetFx3
Installers:
  - Architecture: x64
    InstallerUrl: https://example.test/default.exe
  - Architecture: x86
    InstallerUrl: https://example.test/override.exe
    Dependencies:
      PackageDependencies:
        - PackageIdentifier: Microsoft.DotNet.DesktopRuntime.9
`
	var m InstallerManifest
	if err := yaml.Unmarshal([]byte(body), &m); err != nil {
		t.Fatal(err)
	}
	root := m.EffectiveDependencies(m.Installers[0])
	if root == nil || len(root.PackageDependencies) != 1 || root.PackageDependencies[0].MinimumVersion != "8.0.21" {
		t.Fatalf("root dependencies = %+v", root)
	}
	if len(root.WindowsFeatures) != 1 || root.WindowsFeatures[0] != "NetFx3" {
		t.Errorf("Windows features = %v", root.WindowsFeatures)
	}
	override := m.EffectiveDependencies(m.Installers[1])
	if override == nil || len(override.PackageDependencies) != 1 || override.PackageDependencies[0].PackageIdentifier != "Microsoft.DotNet.DesktopRuntime.9" {
		t.Fatalf("entry dependencies = %+v", override)
	}
	if len(override.WindowsFeatures) != 0 {
		t.Errorf("entry dependencies were merged with root: %+v", override)
	}
}

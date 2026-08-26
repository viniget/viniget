// Package manifest fetches and models winget package manifests.
//
// Manifests are multi-file. A package version directory holds a version
// manifest naming the default locale, one locale manifest per translation, and
// an installer manifest. Older entries may instead ship a single "singleton"
// manifest carrying all three sections at once.
package manifest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/goccy/go-yaml"

	"github.com/viniget/viniget/internal/githubauth"
	"github.com/viniget/viniget/internal/index"
)

// Switches are the command-line switches an installer accepts. They appear
// both at manifest top level and per installer entry.
type Switches struct {
	Silent             string `yaml:"Silent" json:"silent,omitempty"`
	SilentWithProgress string `yaml:"SilentWithProgress" json:"silent_with_progress,omitempty"`
	Interactive        string `yaml:"Interactive" json:"interactive,omitempty"`
	InstallLocation    string `yaml:"InstallLocation" json:"install_location,omitempty"`
	Log                string `yaml:"Log" json:"log,omitempty"`
	Upgrade            string `yaml:"Upgrade" json:"upgrade,omitempty"`
	Custom             string `yaml:"Custom" json:"custom,omitempty"`
}

// NestedFile names an executable inside an archive installer.
type NestedFile struct {
	RelativeFilePath     string `yaml:"RelativeFilePath" json:"relative_file_path,omitempty"`
	PortableCommandAlias string `yaml:"PortableCommandAlias" json:"portable_command_alias,omitempty"`
}

// PackageDependency names another package from the same winget source that
// the selected installer needs. MinimumVersion is optional in the schema.
type PackageDependency struct {
	PackageIdentifier string `yaml:"PackageIdentifier" json:"package_identifier"`
	MinimumVersion    string `yaml:"MinimumVersion" json:"minimum_version,omitempty"`
}

// Dependencies are the prerequisites declared for an installer.
//
// Winget permits this object both at manifest level and on an individual
// installer entry. As with the other inheritable installer fields, an entry's
// object replaces the manifest-level default rather than being merged into it.
type Dependencies struct {
	WindowsFeatures      []string            `yaml:"WindowsFeatures" json:"windows_features,omitempty"`
	WindowsLibraries     []string            `yaml:"WindowsLibraries" json:"windows_libraries,omitempty"`
	PackageDependencies  []PackageDependency `yaml:"PackageDependencies" json:"package_dependencies,omitempty"`
	ExternalDependencies []string            `yaml:"ExternalDependencies" json:"external_dependencies,omitempty"`
}

// Installer is one downloadable variant of a package.
type Installer struct {
	Architecture         string        `yaml:"Architecture" json:"architecture,omitempty"`
	InstallerType        string        `yaml:"InstallerType" json:"installer_type,omitempty"`
	NestedInstallerType  string        `yaml:"NestedInstallerType" json:"nested_installer_type,omitempty"`
	NestedInstallerFiles []NestedFile  `yaml:"NestedInstallerFiles" json:"nested_installer_files,omitempty"`
	Scope                string        `yaml:"Scope" json:"scope,omitempty"`
	InstallerLocale      string        `yaml:"InstallerLocale" json:"installer_locale,omitempty"`
	InstallerURL         string        `yaml:"InstallerUrl" json:"installer_url,omitempty"`
	InstallerSha256      string        `yaml:"InstallerSha256" json:"installer_sha256,omitempty"`
	ProductCode          string        `yaml:"ProductCode" json:"product_code,omitempty"`
	InstallerSwitches    *Switches     `yaml:"InstallerSwitches" json:"installer_switches,omitempty"`
	FileExtensions       []string      `yaml:"FileExtensions" json:"file_extensions,omitempty"`
	Dependencies         *Dependencies `yaml:"Dependencies" json:"dependencies,omitempty"`
	ElevationRequirement string        `yaml:"ElevationRequirement" json:"elevation_requirement,omitempty"`
	ReleaseDate          string        `yaml:"ReleaseDate" json:"release_date,omitempty"`
}

// InstallerManifest is the installer section of a package version.
//
// InstallerType, Scope, InstallerLocale, ProductCode and InstallerSwitches are
// all legal both here and on each entry, with the entry winning. 7zip.7zip is
// the worked example: four exe entries and two wix entries with different
// ProductCodes, under a single top-level Scope. Read them through the
// Effective* accessors rather than off the struct.
type InstallerManifest struct {
	PackageIdentifier string        `yaml:"PackageIdentifier" json:"package_identifier,omitempty"`
	PackageVersion    string        `yaml:"PackageVersion" json:"package_version,omitempty"`
	InstallerType     string        `yaml:"InstallerType" json:"installer_type,omitempty"`
	NestedType        string        `yaml:"NestedInstallerType" json:"nested_installer_type,omitempty"`
	Scope             string        `yaml:"Scope" json:"scope,omitempty"`
	InstallerLocale   string        `yaml:"InstallerLocale" json:"installer_locale,omitempty"`
	ProductCode       string        `yaml:"ProductCode" json:"product_code,omitempty"`
	InstallerSwitches *Switches     `yaml:"InstallerSwitches" json:"installer_switches,omitempty"`
	FileExtensions    []string      `yaml:"FileExtensions" json:"file_extensions,omitempty"`
	Dependencies      *Dependencies `yaml:"Dependencies" json:"dependencies,omitempty"`
	Installers        []Installer   `yaml:"Installers" json:"installers,omitempty"`
	ReleaseDate       string        `yaml:"ReleaseDate" json:"release_date,omitempty"`
	ManifestType      string        `yaml:"ManifestType" json:"-"`
	ManifestVersion   string        `yaml:"ManifestVersion" json:"manifest_version,omitempty"`
}

// EffectiveLocale resolves an entry's locale, falling back to the top-level
// value. An empty locale means the installer is not tied to one locale.
func (m *InstallerManifest) EffectiveLocale(i Installer) string {
	if i.InstallerLocale != "" {
		return i.InstallerLocale
	}
	return m.InstallerLocale
}

// EffectiveType resolves an entry's installer type, falling back to the
// top-level value. A "zip" resolves further to its nested type, which is the
// type that actually has to be driven.
func (m *InstallerManifest) EffectiveType(i Installer) string {
	t := strings.ToLower(i.InstallerType)
	if t == "" {
		t = strings.ToLower(m.InstallerType)
	}
	if t == "zip" {
		nested := i.NestedInstallerType
		if nested == "" {
			nested = m.NestedType
		}
		if nested != "" {
			return strings.ToLower(nested)
		}
	}
	return t
}

// EffectiveScope resolves an entry's scope, falling back to the top-level value.
func (m *InstallerManifest) EffectiveScope(i Installer) string {
	if i.Scope != "" {
		return i.Scope
	}
	return m.Scope
}

// EffectiveProductCode resolves an entry's product code, falling back to the
// top-level value.
func (m *InstallerManifest) EffectiveProductCode(i Installer) string {
	if i.ProductCode != "" {
		return i.ProductCode
	}
	return m.ProductCode
}

// EffectiveSwitches resolves an entry's switches, falling back to the
// top-level value.
func (m *InstallerManifest) EffectiveSwitches(i Installer) *Switches {
	if i.InstallerSwitches != nil {
		return i.InstallerSwitches
	}
	return m.InstallerSwitches
}

// EffectiveFileExtensions resolves the file types an entry claims, falling
// back to the top-level list. These become the desktop file associations.
func (m *InstallerManifest) EffectiveFileExtensions(i Installer) []string {
	if len(i.FileExtensions) > 0 {
		return i.FileExtensions
	}
	return m.FileExtensions
}

// EffectiveDependencies resolves an entry's dependency object, falling back
// to the manifest-level default. The objects are not merged: the winget
// schema defines the entry value as the complete override.
func (m *InstallerManifest) EffectiveDependencies(i Installer) *Dependencies {
	if i.Dependencies != nil {
		return i.Dependencies
	}
	return m.Dependencies
}

// Documentation is one labelled documentation link.
type Documentation struct {
	DocumentLabel string `yaml:"DocumentLabel" json:"label,omitempty"`
	DocumentURL   string `yaml:"DocumentUrl" json:"url,omitempty"`
}

// LocaleManifest is the descriptive section of a package version.
type LocaleManifest struct {
	PackageIdentifier   string          `yaml:"PackageIdentifier" json:"package_identifier,omitempty"`
	PackageVersion      string          `yaml:"PackageVersion" json:"package_version,omitempty"`
	PackageLocale       string          `yaml:"PackageLocale" json:"package_locale,omitempty"`
	Publisher           string          `yaml:"Publisher" json:"publisher,omitempty"`
	PublisherURL        string          `yaml:"PublisherUrl" json:"publisher_url,omitempty"`
	PublisherSupportURL string          `yaml:"PublisherSupportUrl" json:"publisher_support_url,omitempty"`
	Author              string          `yaml:"Author" json:"author,omitempty"`
	PackageName         string          `yaml:"PackageName" json:"package_name,omitempty"`
	PackageURL          string          `yaml:"PackageUrl" json:"package_url,omitempty"`
	License             string          `yaml:"License" json:"license,omitempty"`
	LicenseURL          string          `yaml:"LicenseUrl" json:"license_url,omitempty"`
	Copyright           string          `yaml:"Copyright" json:"copyright,omitempty"`
	ShortDescription    string          `yaml:"ShortDescription" json:"short_description,omitempty"`
	Description         string          `yaml:"Description" json:"description,omitempty"`
	Moniker             string          `yaml:"Moniker" json:"moniker,omitempty"`
	Tags                []string        `yaml:"Tags" json:"tags,omitempty"`
	ReleaseNotes        string          `yaml:"ReleaseNotes" json:"release_notes,omitempty"`
	ReleaseNotesURL     string          `yaml:"ReleaseNotesUrl" json:"release_notes_url,omitempty"`
	Documentations      []Documentation `yaml:"Documentations" json:"documentations,omitempty"`
	ManifestType        string          `yaml:"ManifestType" json:"-"`
	ManifestVersion     string          `yaml:"ManifestVersion" json:"-"`
}

// versionManifest is the entry point file. It names the default locale, which
// is not always en-US -- roughly 6% of the catalog uses something else.
type versionManifest struct {
	PackageIdentifier string `yaml:"PackageIdentifier"`
	PackageVersion    string `yaml:"PackageVersion"`
	DefaultLocale     string `yaml:"DefaultLocale"`
	ManifestType      string `yaml:"ManifestType"`
	ManifestVersion   string `yaml:"ManifestVersion"`
}

// Package is a fully resolved package version.
type Package struct {
	ID            string             `json:"id"`
	Version       string             `json:"version"`
	DefaultLocale string             `json:"default_locale,omitempty"`
	Singleton     bool               `json:"singleton,omitempty"`
	Locale        *LocaleManifest    `json:"locale,omitempty"`
	Installer     *InstallerManifest `json:"installer,omitempty"`
}

// Fetcher retrieves manifests over HTTP.
type Fetcher struct {
	Client      *http.Client
	GitHubToken string
	// Fallback resolves a manifest base path when the derived one 404s. It is
	// optional; without it a 404 is simply an error.
	Fallback func(ctx context.Context, id, version string) (string, error)
}

type errNotFound struct{ url string }

func (e *errNotFound) Error() string { return "manifest not found: " + e.url }

func (f *Fetcher) client() *http.Client {
	if f.Client != nil {
		return f.Client
	}
	return http.DefaultClient
}

func (f *Fetcher) get(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	githubauth.Add(req, f.GitHubToken)
	resp, err := f.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, &errNotFound{url: url}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch %s: %s", url, resp.Status)
	}
	return io.ReadAll(resp.Body)
}

// Fetch resolves a package version to its manifests.
func (f *Fetcher) Fetch(ctx context.Context, id, version string) (*Package, error) {
	base := index.RAW + "/" + index.ManifestBase(id, version)

	body, err := f.get(ctx, base+".yaml")
	var nf *errNotFound
	if err != nil {
		if errors.As(err, &nf) && f.Fallback != nil {
			// The derived path is a repository convention, not a contract.
			// Fall back to the v1 pathparts tree before giving up.
			fmt.Fprintf(os.Stderr,
				"warning: derived manifest path 404'd for %s %s; falling back to the v1 index\n",
				id, version)
			dir, ferr := f.Fallback(ctx, id, version)
			if ferr != nil {
				return nil, ferr
			}
			if dir == "" {
				return nil, err
			}
			base = index.RAW + "/" + strings.TrimSuffix(dir, "/") + "/" + id
			body, err = f.get(ctx, base+".yaml")
		}
	}
	if err != nil {
		return nil, err
	}

	var vm versionManifest
	if err := yaml.Unmarshal(body, &vm); err != nil {
		return nil, fmt.Errorf("parse version manifest: %w", err)
	}
	pkg := &Package{ID: id, Version: version, DefaultLocale: vm.DefaultLocale}

	// A singleton carries every section in the one file we already hold.
	if strings.EqualFold(vm.ManifestType, "singleton") {
		pkg.Singleton = true
		var lm LocaleManifest
		var im InstallerManifest
		if err := yaml.Unmarshal(body, &lm); err != nil {
			return nil, fmt.Errorf("parse singleton manifest: %w", err)
		}
		if err := yaml.Unmarshal(body, &im); err != nil {
			return nil, fmt.Errorf("parse singleton manifest: %w", err)
		}
		pkg.Locale, pkg.Installer = &lm, &im
		if pkg.DefaultLocale == "" {
			pkg.DefaultLocale = lm.PackageLocale
		}
		return pkg, nil
	}

	locale := vm.DefaultLocale
	if locale == "" {
		locale = "en-US"
	}

	var wg sync.WaitGroup
	var lm LocaleManifest
	var im InstallerManifest
	var lerr, ierr error

	wg.Add(2)
	go func() {
		defer wg.Done()
		b, err := f.get(ctx, base+".locale."+locale+".yaml")
		var missing *errNotFound
		if err != nil && errors.As(err, &missing) && locale != "en-US" {
			b, err = f.get(ctx, base+".locale.en-US.yaml")
		}
		if err != nil {
			lerr = err
			return
		}
		lerr = yaml.Unmarshal(b, &lm)
	}()
	go func() {
		defer wg.Done()
		b, err := f.get(ctx, base+".installer.yaml")
		if err != nil {
			ierr = err
			return
		}
		ierr = yaml.Unmarshal(b, &im)
	}()
	wg.Wait()

	// A package with no description is odd but usable; one with no installer
	// is not, so only the installer error is fatal.
	if ierr != nil {
		return nil, fmt.Errorf("installer manifest: %w", ierr)
	}
	pkg.Installer = &im
	if lerr == nil {
		pkg.Locale = &lm
	}
	return pkg, nil
}

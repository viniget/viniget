package install

import (
	"fmt"
	"sort"
	"strings"

	"github.com/viniget/viniget/internal/manifest"
)

// Choice is the installer variant viniget will drive, with every value that
// can be inherited from the manifest's top level already resolved.
type Choice struct {
	Installer manifest.Installer `json:"-"`
	// Type is the effective installer type, with NestedInstallerType already
	// applied. For a zip carrying an inno setup this is "inno", not "zip".
	Type string `json:"installer_type"`
	// Container is the outer type when the payload has to be unpacked before
	// Type can be driven -- "zip", or empty when the download is the installer.
	Container string `json:"container,omitempty"`
	// Nested names the file inside the container that Type refers to.
	Nested      []manifest.NestedFile `json:"-"`
	Switches    *manifest.Switches    `json:"-"`
	Scope       string                `json:"scope,omitempty"`
	ProductCode string                `json:"product_code,omitempty"`
	URL         string                `json:"url"`
	Sha256      string                `json:"sha256,omitempty"`
	Arch        string                `json:"architecture,omitempty"`
	// FileExtensions are the file types this variant claims, which desktop
	// integration turns into associations.
	FileExtensions []string `json:"file_extensions,omitempty"`
}

// Select makes the Wine-aware installer choice for a package.
//
// It differs from winget's own logic in two ways. MSIX and APPX are hard
// skipped rather than ranked last, because Wine cannot run them at all. And
// archive variants are deliberately *penalised* even though extracting is far
// more reliable than executing an installer: the installer is what registers
// Start Menu shortcuts, file associations and icons, which is the metadata
// both desktop integration and launch-target discovery depend on. Extraction
// is the fallback when no supported installer exists, not the happy path.
func Select(m *manifest.InstallerManifest) (*Choice, error) {
	if m == nil || len(m.Installers) == 0 {
		return nil, fmt.Errorf("manifest declares no installers")
	}

	type candidate struct {
		score  int
		choice Choice
	}
	var (
		candidates []candidate
		seenTypes  []string
	)

	for _, entry := range m.Installers {
		t := m.EffectiveType(entry)
		sw := m.EffectiveSwitches(entry)
		arch := strings.ToLower(entry.Architecture)
		seenTypes = appendUnique(seenTypes, t)

		if IsUnsupported(t) || !Supported(t, sw) {
			continue
		}
		// x86 runs fine in a win64 prefix via WoW64; arm64 does not run at
		// all, and "neutral" is the architecture portable payloads declare.
		if arch != "x64" && arch != "x86" && arch != "neutral" {
			continue
		}

		scope := m.EffectiveScope(entry)
		score := 0
		if IsArchive(t) {
			score -= 4
		}
		if arch == "x64" {
			score += 2
		} else {
			score++
		}
		// A machine-scope install writes to Program Files and the common Start
		// Menu, which is where both the launch probe and desktop integration
		// go looking.
		if scope == "machine" {
			score++
		}

		c := Choice{
			Installer:   entry,
			Type:        t,
			Switches:    sw,
			Scope:       scope,
			ProductCode: m.EffectiveProductCode(entry),
			URL:         entry.InstallerURL,
			Sha256:      entry.InstallerSha256,
			Arch:        arch,
			// Carried whatever the container turns out to be. The schema
			// attaches NestedInstallerFiles to zip entries, but SumatraPDF
			// declares them on a "portable" that ships a zip anyway, and they
			// are the only statement of that package's entry point.
			Nested:         entry.NestedInstallerFiles,
			FileExtensions: m.EffectiveFileExtensions(entry),
		}
		if outer := outerType(m, entry); outer == "zip" {
			c.Container = "zip"
		}
		candidates = append(candidates, candidate{score: score, choice: c})
	}

	if len(candidates) == 0 {
		return nil, &UnsupportedError{Types: seenTypes}
	}
	// Stable, so manifest order breaks ties -- publishers list their preferred
	// variant first.
	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].score > candidates[j].score
	})
	best := candidates[0].choice
	return &best, nil
}

// UnsupportedError reports that no variant of a package can be driven.
type UnsupportedError struct {
	Types []string
}

func (e *UnsupportedError) Error() string {
	found := strings.Join(e.Types, ", ")
	if found == "" {
		found = "none"
	}
	return fmt.Sprintf("no installable variant (manifest offers: %s; viniget v1 drives: %s)",
		found, strings.Join(SupportedTypes(), ", "))
}

// outerType is the container type before NestedInstallerType is applied.
func outerType(m *manifest.InstallerManifest, i manifest.Installer) string {
	t := strings.ToLower(i.InstallerType)
	if t == "" {
		t = strings.ToLower(m.InstallerType)
	}
	return t
}

func appendUnique(list []string, s string) []string {
	if s == "" {
		return list
	}
	for _, existing := range list {
		if existing == s {
			return list
		}
	}
	return append(list, s)
}

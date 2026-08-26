package install

import (
	"fmt"
	"os"
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
	// Interactive is true only for a generic exe without a declared silent
	// mode. It is selected as a last resort and runs on the host display.
	Interactive bool `json:"interactive,omitempty"`
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
	// Dependencies are the prerequisites declared for this exact installer
	// variant, with the manifest-level default already inherited.
	Dependencies *manifest.Dependencies `json:"dependencies,omitempty"`
}

// Select makes the Wine-aware installer choice for a package. Locale-specific
// variants prefer the Linux host's active message locale, then the package's
// default locale. Manifest order breaks the remaining ties.
//
// It differs from winget's own logic in two ways. MSIX and APPX are hard
// skipped rather than ranked last, because Wine cannot run them at all. And
// archive variants are deliberately *penalised* behind unattended installers
// even though extracting is far more reliable: a full installer registers the
// Start Menu shortcuts, file associations and icons that desktop integration
// and launch-target discovery depend on. An archive still beats a generic exe
// that would require user interaction.
func Select(m *manifest.InstallerManifest, defaultLocale string) (*Choice, error) {
	if m == nil || len(m.Installers) == 0 {
		return nil, fmt.Errorf("manifest declares no installers")
	}

	type candidate struct {
		score  int
		locale string
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
			Interactive: IsInteractive(t, sw),
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
			Dependencies:   m.EffectiveDependencies(entry),
		}
		if outer := outerType(m, entry); outer == "zip" {
			c.Container = "zip"
		}
		candidates = append(candidates, candidate{
			score: score, locale: m.EffectiveLocale(entry), choice: c,
		})
	}

	if len(candidates) == 0 {
		return nil, &UnsupportedError{Types: seenTypes}
	}
	host := hostLocale()
	listed := candidates[0].locale
	// Locale is the primary choice: a native x86 build is more useful than an
	// x64 build the user cannot read. Within one locale, keep the existing
	// Wine-aware score. The first locale among runnable manifest entries is the
	// fallback when neither requested locale is present.
	sort.SliceStable(candidates, func(i, j int) bool {
		li := localePriority(candidates[i].locale, host, defaultLocale, listed)
		lj := localePriority(candidates[j].locale, host, defaultLocale, listed)
		if li != lj {
			return li > lj
		}
		// A GUI installer needs a person and is the final fallback. This tier
		// deliberately sits above the archive penalty so even a portable
		// payload beats an otherwise better interactive exe.
		if candidates[i].choice.Interactive != candidates[j].choice.Interactive {
			return !candidates[i].choice.Interactive
		}
		return candidates[i].score > candidates[j].score
	})
	best := candidates[0].choice
	return &best, nil
}

// hostLocale returns the locale governing translated messages under POSIX.
// LC_ALL overrides the category-specific value, which in turn overrides LANG.
func hostLocale() string {
	for _, name := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if value := os.Getenv(name); value != "" {
			return normalizeLocale(value)
		}
	}
	return ""
}

// localePriority ranks an installer's locale against the requested locales
// and then the locale of the first runnable entry in the manifest.
// A host language-only match deliberately beats an exact default-locale match:
// the host preference is the first fallback tier, not merely a tie breaker.
func localePriority(installer, host, fallback, listed string) int {
	if match := localeMatch(installer, host); match != 0 {
		return 4 + match // host exact 6, host language 5
	}
	if match := localeMatch(installer, fallback); match != 0 {
		return 2 + match // default exact 4, default language 3
	}
	return listedLocaleMatch(installer, listed) // listed exact 2, language 1
}

// listedLocaleMatch also matches two empty locales. Empty InstallerLocale is a
// real manifest choice here: it identifies a locale-neutral installer group.
func listedLocaleMatch(installer, listed string) int {
	installer = normalizeLocale(installer)
	listed = normalizeLocale(listed)
	if installer == listed {
		return 2
	}
	if installer != "" && listed != "" && localeLanguage(installer) == localeLanguage(listed) {
		return 1
	}
	return 0
}

// localeMatch returns 2 for an exact locale match and 1 when just the primary
// language matches. This lets en-US select an installer labelled simply en.
func localeMatch(installer, wanted string) int {
	installer = normalizeLocale(installer)
	wanted = normalizeLocale(wanted)
	if installer == "" || wanted == "" {
		return 0
	}
	if installer == wanted {
		return 2
	}
	if localeLanguage(installer) == localeLanguage(wanted) {
		return 1
	}
	return 0
}

// normalizeLocale turns the common Linux locale form en_US.UTF-8 into the
// BCP-47-shaped en-us used by winget. C and POSIX carry no language preference.
func normalizeLocale(locale string) string {
	locale = strings.TrimSpace(locale)
	if end := strings.IndexAny(locale, ".@"); end >= 0 {
		locale = locale[:end]
	}
	locale = strings.ToLower(strings.ReplaceAll(locale, "_", "-"))
	if locale == "c" || locale == "posix" {
		return ""
	}
	return locale
}

func localeLanguage(locale string) string {
	language, _, _ := strings.Cut(locale, "-")
	return language
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

// Package install drives a winget installer to completion inside a Wine
// prefix and scores what it left behind.
package install

import (
	"strings"

	"github.com/viniget/viniget/internal/manifest"
)

// defaultSilent is the switch table viniget applies when a manifest declares
// nothing.
//
// This is load-bearing, not a convenience. Only 29 of those same 199 sampled
// manifests declare a Silent switch; the rest assume the client already knows
// how to drive their installer type. winget hardcodes exactly this knowledge,
// so viniget has to as well. Three of the five packages in the validation run
// installed purely on these defaults.
var defaultSilent = map[string][]string{
	"nullsoft": {"/S"},
	"inno":     {"/VERYSILENT", "/SUPPRESSMSGBOXES", "/NORESTART", "/SP-"},
	"burn":     {"/quiet", "/norestart"},
}

// msiTypes are driven through msiexec rather than by switches on the payload.
var msiTypes = map[string]bool{"msi": true, "wix": true}

// archiveTypes are handled by extracting rather than executing.
var archiveTypes = map[string]bool{"zip": true, "portable": true}

// unsupportedTypes are structurally impossible under Wine, not merely
// untested. Together they are ~2% of the catalog.
var unsupportedTypes = map[string]bool{"msix": true, "appx": true, "msstore": true, "pwa": true}

// IsArchive reports whether a type is installed by extraction.
func IsArchive(t string) bool { return archiveTypes[strings.ToLower(t)] }

// IsMSI reports whether a type is driven through msiexec.
func IsMSI(t string) bool { return msiTypes[strings.ToLower(t)] }

// IsUnsupported reports whether Wine can never run a type.
func IsUnsupported(t string) bool { return unsupportedTypes[strings.ToLower(t)] }

// Supported reports whether viniget can drive an installer of this type.
// Generic exe installers without a declared silent mode are supported through
// their visible UI; the other accepted types remain unattended.
func Supported(t string, _ *manifest.Switches) bool {
	t = strings.ToLower(t)
	switch {
	case archiveTypes[t], msiTypes[t]:
		return true
	case defaultSilent[t] != nil:
		return true
	case t == "exe":
		return true
	}
	return false
}

// SupportedTypes lists what v1 can drive, for error messages.
func SupportedTypes() []string {
	return []string{"burn", "exe", "inno", "msi", "nullsoft", "portable", "wix", "zip"}
}

func declaredSilent(sw *manifest.Switches) string {
	if sw == nil {
		return ""
	}
	if sw.Silent != "" {
		return sw.Silent
	}
	return sw.SilentWithProgress
}

// SilentArgs resolves the argv for an unattended install.
//
// The second return is false when the type is driven some other way (msiexec,
// extraction) or when there is no safe way to run it unattended at all. For
// msi and wix it returns any Custom switches with true, since those are still
// appended to the msiexec command line.
func SilentArgs(t string, sw *manifest.Switches) ([]string, bool) {
	t = strings.ToLower(t)
	if archiveTypes[t] || unsupportedTypes[t] {
		return nil, false
	}

	var args []string
	switch declared := declaredSilent(sw); {
	case declared != "":
		args = append(args, splitArgs(declared)...)
	case t == "exe":
		return nil, false // never guess switches for a bare exe
	case defaultSilent[t] != nil:
		args = append(args, defaultSilent[t]...)
	case !msiTypes[t]:
		return nil, false
	}

	if sw != nil && sw.Custom != "" {
		args = append(args, splitArgs(sw.Custom)...)
	}
	return args, true
}

// IsInteractive reports whether a generic exe has to be driven through its
// visible UI. A declared Silent or SilentWithProgress switch always keeps the
// install unattended; viniget never invents silent switches for an exe.
func IsInteractive(t string, sw *manifest.Switches) bool {
	return strings.EqualFold(t, "exe") && declaredSilent(sw) == ""
}

// InteractiveArgs resolves the switches Winget declares for an interactive
// generic-exe install. Custom applies to every manifest-driven install mode,
// including interactive. An empty successful result means the executable is
// launched as-is and the user completes its UI.
func InteractiveArgs(t string, sw *manifest.Switches) ([]string, bool) {
	if !IsInteractive(t, sw) {
		return nil, false
	}
	var args []string
	if sw != nil {
		args = append(args, splitArgs(sw.Interactive)...)
		args = append(args, splitArgs(sw.Custom)...)
	}
	return args, true
}

// Declared reports whether the resolved switches came from the manifest
// rather than viniget's own table. It is worth surfacing, because a failure
// on a declared switch is a vendor bug and a failure on a default is ours.
func Declared(sw *manifest.Switches) bool { return declaredSilent(sw) != "" }

// splitArgs splits a declared switch string into argv.
//
// The Python prototype used str.split(), which is wrong for the manifests
// that quote a path: `/DIR="C:\Program Files\App"` has to survive as one
// argument. Only double quotes are honoured, because that is what cmd.exe
// itself honours and what manifest authors are writing against.
func splitArgs(s string) []string {
	var (
		args    []string
		cur     strings.Builder
		inQuote bool
		started bool
	)
	flush := func() {
		if started {
			args = append(args, cur.String())
			cur.Reset()
			started = false
		}
	}
	for _, r := range s {
		switch {
		case r == '"':
			inQuote = !inQuote
			started = true
		case (r == ' ' || r == '\t') && !inQuote:
			flush()
		default:
			cur.WriteRune(r)
			started = true
		}
	}
	flush()
	return args
}

// expandPlaceholders substitutes the tokens winget defines for switch values.
// A manifest that writes <LOGPATH> expects the client to fill it in; passing
// it through literally hands the installer a filename containing angle
// brackets.
func expandPlaceholders(args []string, logPath, installPath string) []string {
	r := strings.NewReplacer("<LOGPATH>", logPath, "<INSTALLPATH>", installPath)
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = r.Replace(a)
	}
	return out
}

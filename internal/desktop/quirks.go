package desktop

// Quirks are the per-package wine settings an application needs in order to
// start at all, as opposed to the ones that merely improve it.
//
// This table is a placeholder for what the compatibility harness is meant to
// ship in the index: a verdict database keyed by package identifier, built by
// measurement rather than by hand. Until that exists, an entry here earns its
// place the same way -- it names what was measured, on which wine version --
// and every one of them is a wine bug whose real fix is in wine. A package
// that merely runs badly does not belong here; the bar is "does not start".

type environmentDefault struct {
	Name  string
	Value string
}

type launchQuirk struct {
	DLLOverrides     string
	Environment      []environmentDefault
	UnsetEnvironment []string
	PrependPath      string
}

// launchQuirks maps a package identifier to the measured launch environment
// its generated launcher carries.
var launchQuirks = map[string]launchQuirk{
	// Icecream Ebook Reader initialises SAPI on startup and enumerates the
	// voice tokens' attributes, looping until it is told there are no more.
	// Wine's ISpDataKey::EnumValues is a stub that returns E_NOTIMPL, which
	// is not SPERR_NO_MORE_ITEMS, so the loop never ends: the application
	// spins at 100% of a core on its splash screen and never opens a window.
	// Disabling sapi costs the read-aloud feature, which the same stub was
	// never going to provide, and the application then starts normally.
	// Measured on wine-11.16.
	"IcecreamApps.IcecreamEbookReader": {DLLOverrides: "sapi=d"},

	// Zalo 26.8.10 embeds Electron 22/Chromium 108. Its login renderer blocks
	// permanently at the white splash screen on Wine 11.15 and 11.16; the
	// identical prefix reaches the QR login under the Wine 10 build installed
	// in /opt/wine-cachyos. The sync settings make the known-good launch use
	// Wine's server synchronization, though switching NTSync on was measured
	// to work under Wine 10 too and is not the Wine 11 fix. Clear host-side
	// Electron and overlay injection so they cannot change that measured path.
	// Measured with Zalo 26.8.10 on wine-10.0-20260425 and wine-11.15/11.16.
	"VNGCorp.Zalo": {
		Environment: []environmentDefault{
			{Name: "WINENTSYNC", Value: "0"},
			{Name: "WINEESYNC", Value: "0"},
			{Name: "WINEFSYNC", Value: "0"},
		},
		UnsetEnvironment: []string{
			"MANGOHUD",
			"ELECTRON_RUN_AS_NODE",
			"ELECTRON_NO_ATTACH_CONSOLE",
		},
		PrependPath: "/opt/wine-cachyos/bin",
	},
}

func launchQuirkFor(id string) launchQuirk { return launchQuirks[id] }

// DLLOverridesFor returns the WINEDLLOVERRIDES a package is known to need, or
// the empty string for the overwhelming majority that need none.
func DLLOverridesFor(id string) string { return launchQuirkFor(id).DLLOverrides }

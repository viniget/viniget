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

// dllOverrideQuirks maps a package identifier to the WINEDLLOVERRIDES its
// generated launcher carries.
var dllOverrideQuirks = map[string]string{
	// Icecream Ebook Reader initialises SAPI on startup and enumerates the
	// voice tokens' attributes, looping until it is told there are no more.
	// Wine's ISpDataKey::EnumValues is a stub that returns E_NOTIMPL, which
	// is not SPERR_NO_MORE_ITEMS, so the loop never ends: the application
	// spins at 100% of a core on its splash screen and never opens a window.
	// Disabling sapi costs the read-aloud feature, which the same stub was
	// never going to provide, and the application then starts normally.
	// Measured on wine-11.16.
	"IcecreamApps.IcecreamEbookReader": "sapi=d",
}

// DLLOverridesFor returns the WINEDLLOVERRIDES a package is known to need, or
// the empty string for the overwhelming majority that need none.
func DLLOverridesFor(id string) string { return dllOverrideQuirks[id] }

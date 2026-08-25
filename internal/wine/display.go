package wine

import (
	"os"
	"os/exec"
)

// Display is how a wine command gets an X server.
//
// A silent install is supposed to draw nothing, but "supposed to" is doing a
// lot of work there: Inno paints a splash before it parses /VERYSILENT, and
// several MSI custom actions create a window and immediately hide it. Running
// on the user's own display means those flash across their desktop and can
// steal focus mid-keystroke, so a private Xvfb is the default whenever one is
// available.
type Display struct {
	// Wrapper is an argv prefix that provides the display, e.g. xvfb-run.
	Wrapper []string
	// Value is exported as DISPLAY. Empty inherits the caller's.
	Value string
	// Kind names the strategy for logs and --json output.
	Kind string
}

// Headless resolves the best available display strategy.
//
// The fallbacks matter more than they look. Wine with no display at all still
// installs most nullsoft and Inno packages, but msiexec's UI-suppressed mode
// still initialises a window station, so an MSI on a headless box with no
// Xvfb is the one case that genuinely needs the host's display.
func Headless() Display {
	if path, err := exec.LookPath("xvfb-run"); err == nil {
		// -a picks a free display number, which matters because installs run
		// in parallel during a sweep.
		return Display{
			Wrapper: []string{path, "-a", "-s", "-screen 0 1280x1024x24"},
			Kind:    "xvfb",
		}
	}
	if d := os.Getenv("DISPLAY"); d != "" {
		return Display{Value: d, Kind: "host"}
	}
	return Display{Kind: "none"}
}

// OnHost forces the caller's own display, for watching an install go wrong.
func OnHost() Display {
	return Display{Value: os.Getenv("DISPLAY"), Kind: "host"}
}

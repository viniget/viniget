package desktop

import (
	"os"
	"path/filepath"
)

// Dirs are the destinations desktop integration writes to.
//
// All four are per-user. Nothing here ever touches a system directory: an
// application installed into one user's Wine prefix cannot be launched by
// anybody else, so a system-wide menu entry would be a broken one.
type Dirs struct {
	// Applications holds the .desktop entries.
	Applications string
	// Icons is the icon theme root; icons land under hicolor/<size>/apps.
	Icons string
	// MIME is the shared-mime-info root; declarations land in packages/.
	MIME string
	// Launchers holds the generated launch scripts. It lives in viniget's own
	// data directory rather than an XDG one because it is viniget's state,
	// not a place a desktop environment goes looking.
	Launchers string
}

// UserDirs resolves the XDG destinations, with launchers under viniget's data
// directory.
func UserDirs(dataDir string) Dirs {
	share := xdgDataHome()
	return Dirs{
		Applications: filepath.Join(share, "applications"),
		Icons:        filepath.Join(share, "icons"),
		MIME:         filepath.Join(share, "mime"),
		Launchers:    filepath.Join(dataDir, "launchers"),
	}
}

func xdgDataHome() string {
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "share")
	}
	return filepath.Join(home, ".local", "share")
}

// xdgDataDirs is the search path for data already installed on the system,
// which is where the shared MIME database is read from.
func xdgDataDirs() []string {
	dirs := []string{xdgDataHome()}
	list := os.Getenv("XDG_DATA_DIRS")
	if list == "" {
		list = "/usr/local/share:/usr/share"
	}
	for _, d := range filepath.SplitList(list) {
		if d != "" {
			dirs = append(dirs, d)
		}
	}
	return dirs
}

package install

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// scoreDirs are the trees an install is scored on. They are also the common
// locations desktop integration searches when an installer creates no
// shortcut.
//
// Deliberately not the whole prefix. Wine itself writes to the registry, to
// drive_c/windows and to the user's temp directory on any run at all, so
// scoring those would report a delta for an installer that did nothing.
var scoreDirs = []string{
	"drive_c/Program Files",
	"drive_c/Program Files (x86)",
	"drive_c/ProgramData",
	"drive_c/users",
}

// snapshot records every file under the scored trees, relative to the prefix.
func snapshot(prefix string) (map[string]struct{}, error) {
	seen := make(map[string]struct{}, 4096)
	for _, sub := range scoreDirs {
		root := filepath.Join(prefix, sub)
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				// A tree that does not exist yet is not an error: a 32-bit
				// prefix has no "Program Files (x86)" until something needs it.
				if os.IsNotExist(err) {
					return nil
				}
				return err
			}
			if d.IsDir() {
				return nil
			}
			rel, err := filepath.Rel(prefix, p)
			if err != nil {
				return err
			}
			seen[rel] = struct{}{}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return seen, nil
}

// Delta is what an installer added to a prefix.
//
// This, not the exit code, is what decides whether an install worked.
// WinMerge's Inno installer runs past a four-minute timeout having already
// written 337 files and 8 executables correctly; a harness that trusts the
// return code records a working application as broken.
type Delta struct {
	Files       []string
	Executables []string
}

func diff(before, after map[string]struct{}) Delta {
	var d Delta
	for f := range after {
		if _, existed := before[f]; existed {
			continue
		}
		d.Files = append(d.Files, f)
		if strings.EqualFold(filepath.Ext(f), ".exe") {
			d.Executables = append(d.Executables, f)
		}
	}
	sort.Strings(d.Files)
	sort.Strings(d.Executables)
	return d
}

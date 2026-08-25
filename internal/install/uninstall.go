package install

import (
	"fmt"

	"github.com/viniget/viniget/internal/desktop"
	"github.com/viniget/viniget/internal/wine"
)

// Uninstalling is where the one-prefix-per-package decision pays for itself a
// second time.
//
// Nothing in the prefix has to be reasoned about, because nothing else shares
// it. There is no vendor uninstaller to run -- the record even carries the
// ProductCode that `msiexec /x` would want -- and running one would be strictly
// worse: it is slow, it is the least tested path in any Windows installer, it
// fails differently on Wine than on Windows, and whatever it left behind would
// be deleted a moment later anyway. The registry, the shared DLLs, the
// %APPDATA% the application wrote and the uninstaller would have missed: all of
// it is inside the directory being removed.
//
// So the whole operation is three deletions, and the only real decisions are
// what order they happen in and what to do when one of them fails.

// Removed is what uninstalling one package took away.
type Removed struct {
	ID       string       `json:"id"`
	Name     string       `json:"name,omitempty"`
	Version  string       `json:"version,omitempty"`
	Prefixes wine.Removal `json:"prefixes"`
	// Desktop is every integration file that was deleted.
	Desktop []string `json:"desktop,omitempty"`
	// Orphan marks a package that had no installed-package record, and whose
	// removal therefore worked off what was found on disk.
	Orphan bool     `json:"orphan,omitempty"`
	Notes  []string `json:"notes,omitempty"`
}

func (r *Removed) note(format string, a ...any) {
	r.Notes = append(r.Notes, fmt.Sprintf(format, a...))
}

// Uninstall removes one package: its desktop integration, its prefix, and its
// record, in that order.
//
// The order is the interesting part. The menu entry goes first, so that a
// removal interrupted halfway is an application that can no longer be launched
// rather than a menu full of entries pointing into a prefix that is being
// deleted. The record goes last, and only if the prefix actually went: a
// record dropped over a prefix that survived is an orphan nothing lists, which
// is the one state this command exists to get people out of.
//
// db is left dirty; the caller decides when to Save, so that removing twenty
// packages writes the state file once.
func Uninstall(db *DB, prefixes *wine.Store, dirs desktop.Dirs, id string) (*Removed, error) {
	rec, recorded := db.Get(id)
	out := &Removed{ID: id, Name: rec.Name, Version: rec.Version, Orphan: !recorded}

	// The recorded file list is exact; without one, Remove falls back to
	// globbing, which is what makes an orphan cleanable at all.
	files, err := desktop.Remove(dirs, id, rec.Desktop)
	out.Desktop = files
	// Wine's menu builder wrote entries of its own for anything installed
	// before viniget started disabling it, and they name the prefix that is
	// about to be deleted. Nothing else would ever clean them up.
	if prefix := rec.Prefix; prefix != "" || !recorded {
		if prefix == "" {
			prefix, _ = prefixes.For(id)
		}
		out.Desktop = append(out.Desktop, desktop.PruneWineMenu(dirs, prefix)...)
	}
	if err != nil {
		// Not fatal, and deliberately so: a menu entry that could not be
		// deleted is a smaller problem than a prefix left on disk because
		// viniget gave up before reaching it.
		out.note("desktop integration: %v", err)
	}

	rm, err := prefixes.Remove(id)
	out.Prefixes = rm
	if err != nil {
		return out, err
	}
	if len(rm.Paths) == 0 && recorded {
		out.note("no prefix at %s; it had already been removed by hand", rec.Prefix)
	}

	db.Remove(id)
	return out, nil
}

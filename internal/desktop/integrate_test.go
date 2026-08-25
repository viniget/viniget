package desktop

import (
	"image/color"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// fakePrefix builds a wine prefix with an installed application in it: an
// executable carrying an icon, and the Start Menu shortcut naming it.
func fakePrefix(t *testing.T) string {
	t.Helper()
	prefix := t.TempDir()

	appDir := filepath.Join(prefix, "drive_c", "Program Files", "Demo")
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appDir, "demo.exe"),
		buildPE(t, demoIconDIB(), 1, 1), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appDir, "helper.exe"), []byte("MZ"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(prefix, "dosdevices"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../drive_c", filepath.Join(prefix, "dosdevices", "c:")); err != nil {
		t.Fatal(err)
	}

	menu := filepath.Join(prefix, "drive_c", "ProgramData", "Microsoft", "Windows", "Start Menu", "Programs")
	writeLNK(t, filepath.Join(menu, "Demo.lnk"), &lnkBuilder{
		flags:   flagHasLinkInfo | flagHasName,
		target:  `C:\Program Files\Demo\demo.exe`,
		strings: []string{"The demo application"},
	})
	writeLNK(t, filepath.Join(menu, "Demo Tools.lnk"), &lnkBuilder{
		flags:  flagHasLinkInfo,
		target: `C:\Program Files\Demo\helper.exe`,
	})
	writeLNK(t, filepath.Join(menu, "Uninstall Demo.lnk"), &lnkBuilder{
		flags:  flagHasLinkInfo,
		target: `C:\Program Files\Demo\demo.exe`,
	})
	return prefix
}

func testDirs(t *testing.T) Dirs {
	t.Helper()
	root := t.TempDir()
	return Dirs{
		Applications: filepath.Join(root, "applications"),
		Icons:        filepath.Join(root, "icons"),
		MIME:         filepath.Join(root, "mime"),
		Launchers:    filepath.Join(root, "launchers"),
	}
}

func TestIntegrate(t *testing.T) {
	fakeMIMEDatabase(t, "50:text/plain:*.txt")
	prefix := fakePrefix(t)
	dirs := testDirs(t)

	res, err := Integrate(Package{
		ID:         "Vendor.Demo",
		Name:       "Demo",
		Version:    "1.2.3",
		Comment:    "A demo application",
		Tags:       []string{"editor"},
		Extensions: []string{".txt", ".dmo"},
		Prefix:     prefix,
	}, Options{Dirs: dirs, NoRefresh: true})
	if err != nil {
		t.Fatal(err)
	}

	if len(res.Apps) != 2 {
		var names []string
		for _, a := range res.Apps {
			names = append(names, a.Name)
		}
		t.Fatalf("got %d apps (%v), want 2 -- the uninstaller must be filtered", len(res.Apps), names)
	}

	primary := res.Apps[0]
	if primary.Name != "Demo" {
		t.Errorf("primary app = %q, want the Start Menu label", primary.Name)
	}
	if primary.Source != "start-menu" {
		t.Errorf("primary source = %q", primary.Source)
	}
	// The primary entry keeps the bare identifier so its filename is stable
	// and predictable; the rest are distinguished by their label.
	if want := filepath.Join(dirs.Applications, "viniget-Vendor.Demo.desktop"); primary.DesktopFile != want {
		t.Errorf("desktop file = %q, want %q", primary.DesktopFile, want)
	}
	if want := filepath.Join(dirs.Applications, "viniget-Vendor.Demo-demo-tools.desktop"); res.Apps[1].DesktopFile != want {
		t.Errorf("secondary desktop file = %q, want %q", res.Apps[1].DesktopFile, want)
	}
	if res.Entry() != filepath.Join(prefix, "drive_c", "Program Files", "Demo", "demo.exe") {
		t.Errorf("Entry = %q", res.Entry())
	}

	body, err := os.ReadFile(primary.DesktopFile)
	if err != nil {
		t.Fatal(err)
	}
	entry := string(body)
	for _, want := range []string{
		"Name=Demo\n",
		"Comment=A demo application\n",
		"Icon=viniget-Vendor.Demo\n",
		"Categories=Utility;TextEditor;\n",
		"MimeType=text/plain;application/x-viniget-dmo;\n",
		"X-Viniget-Package=Vendor.Demo\n",
		"Exec=" + primary.Launcher + " %F\n",
	} {
		if !strings.Contains(entry, want) {
			t.Errorf("entry missing %q:\n%s", want, entry)
		}
	}

	// Associations belong to the primary entry alone: a suite's secondary
	// entries have no claim on the package's file types.
	second, err := os.ReadFile(res.Apps[1].DesktopFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(second), "MimeType=") {
		t.Errorf("secondary entry claimed the file associations:\n%s", second)
	}

	if len(primary.IconSizes) != 1 || primary.IconSizes[0] != 48 || primary.IconName != "viniget-Vendor.Demo" {
		t.Errorf("icon = %q at sizes %v, want the theme name at 48px", primary.IconName, primary.IconSizes)
	}
	if _, err := os.Stat(filepath.Join(dirs.Icons, "hicolor", "48x48", "apps", "viniget-Vendor.Demo.png")); err != nil {
		t.Errorf("icon not written where the theme looks for it: %v", err)
	}
	// helper.exe carries no icon at all, which is a note rather than a
	// failure -- the entry is still worth having.
	if res.Apps[1].IconName != "" {
		t.Errorf("an iconless executable produced an icon: %q", res.Apps[1].IconName)
	}
	if !hasNote(res, "no icon in") {
		t.Errorf("no note about the missing icon: %v", res.Notes)
	}
	if res.MIMEFile == "" {
		t.Error("no MIME declaration written for the unknown .dmo extension")
	}
	// Nothing viniget writes changes which application a double-click opens:
	// the default handler lives in mimeapps.list, which is left alone.
	if _, err := os.Stat(filepath.Join(dirs.Applications, "mimeapps.list")); !os.IsNotExist(err) {
		t.Error("integration wrote mimeapps.list and took over the default handler")
	}

	// Every recorded file must exist, or removal will silently miss some.
	for _, f := range res.Files {
		if _, err := os.Stat(f); err != nil {
			t.Errorf("recorded file is missing: %v", err)
		}
	}

	removed, err := Remove(dirs, "Vendor.Demo", res.Files)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != len(res.Files) {
		t.Errorf("removed %d of %d recorded files", len(removed), len(res.Files))
	}
	for _, f := range res.Files {
		if _, err := os.Stat(f); !os.IsNotExist(err) {
			t.Errorf("%s survived removal", f)
		}
	}
}

func TestIntegrateOddIconSizeIsReferencedByPath(t *testing.T) {
	// A .desktop file may name its icon by absolute path, which is what keeps
	// an application whose only icon is 20x20 from having no icon at all.
	prefix := t.TempDir()
	appDir := filepath.Join(prefix, "drive_c", "Program Files", "Odd")
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(appDir, "odd.exe")
	odd := solidIconDIB(20, color.RGBA{R: 0xFF, A: 0xFF})
	if err := os.WriteFile(exe, buildPE(t, odd, 1, 1), 0o755); err != nil {
		t.Fatal(err)
	}

	dirs := testDirs(t)
	res, err := Integrate(Package{ID: "Vendor.Odd", Name: "Odd", Prefix: prefix, Entry: exe},
		Options{Dirs: dirs, NoRefresh: true})
	if err != nil {
		t.Fatal(err)
	}
	icon := res.Apps[0].IconName
	if !filepath.IsAbs(icon) || !strings.HasSuffix(icon, ".png") {
		t.Errorf("icon = %q, want an absolute path", icon)
	}
	if _, err := os.Stat(icon); err != nil {
		t.Errorf("%v", err)
	}
	if !hasNote(res, "no directory for") {
		t.Errorf("notes = %v", res.Notes)
	}
}

func TestIntegrateFallsBackToManifestEntry(t *testing.T) {
	// An archive package has no installer to write a shortcut, so the entry
	// point the manifest declared is all there is.
	prefix := t.TempDir()
	appDir := filepath.Join(prefix, "drive_c", "Program Files", "Portable")
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(appDir, "portable.exe")
	if err := os.WriteFile(exe, buildPE(t, demoIconDIB(), 1, 1), 0o755); err != nil {
		t.Fatal(err)
	}

	dirs := testDirs(t)
	res, err := Integrate(Package{
		ID: "Vendor.Portable", Name: "Portable App", Prefix: prefix, Entry: exe,
	}, Options{Dirs: dirs, NoRefresh: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Apps) != 1 {
		t.Fatalf("got %d apps, want 1", len(res.Apps))
	}
	if res.Apps[0].Source != "manifest" {
		t.Errorf("source = %q, want manifest", res.Apps[0].Source)
	}
	if res.Apps[0].Name != "Portable App" {
		t.Errorf("name = %q; with no shortcut to name it, the package name is the label", res.Apps[0].Name)
	}
}

func TestIntegrateWithNothingLaunchable(t *testing.T) {
	// A runtime or a codec that installed correctly and put nothing in the
	// Start Menu has behaved exactly as intended.
	res, err := Integrate(Package{ID: "Vendor.Runtime", Prefix: t.TempDir()},
		Options{Dirs: testDirs(t), NoRefresh: true})
	if err != nil {
		t.Fatalf("a package with nothing to launch must not be an error: %v", err)
	}
	if len(res.Apps) != 0 || len(res.Files) != 0 {
		t.Errorf("wrote %d entries and %d files for a package with no entry point", len(res.Apps), len(res.Files))
	}
	if !hasNote(res, "nothing launchable") {
		t.Errorf("notes = %v", res.Notes)
	}
}

func TestIntegrateReplacesStaleEntries(t *testing.T) {
	// The second run has one fewer shortcut than the first. The entry for the
	// shortcut that went away has to go with it.
	prefix := fakePrefix(t)
	dirs := testDirs(t)
	pkg := Package{ID: "Vendor.Demo", Name: "Demo", Prefix: prefix}

	first, err := Integrate(pkg, Options{Dirs: dirs, NoRefresh: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Apps) != 2 {
		t.Fatalf("first run produced %d apps, want 2", len(first.Apps))
	}
	stale := first.Apps[1].DesktopFile

	menu := filepath.Join(prefix, "drive_c", "ProgramData", "Microsoft", "Windows", "Start Menu", "Programs")
	if err := os.Remove(filepath.Join(menu, "Demo Tools.lnk")); err != nil {
		t.Fatal(err)
	}

	second, err := Integrate(pkg, Options{Dirs: dirs, NoRefresh: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Apps) != 1 {
		t.Fatalf("second run produced %d apps, want 1", len(second.Apps))
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("%s survived a regeneration that no longer produces it", stale)
	}
	if _, err := os.Stat(second.Apps[0].DesktopFile); err != nil {
		t.Errorf("the surviving entry was removed too: %v", err)
	}
}

func TestIntegrateDeduplicatesMIMETypes(t *testing.T) {
	// .djv and .djvu are both image/vnd.djvu. Listing it twice makes the entry
	// malformed, which desktop-file-validate reports and some menus reject.
	fakeMIMEDatabase(t, "50:image/vnd.djvu:*.djv", "50:image/vnd.djvu:*.djvu")
	dirs := testDirs(t)
	res, err := Integrate(Package{
		ID: "Vendor.Demo", Name: "Demo", Prefix: fakePrefix(t),
		Extensions: []string{".djv", ".djvu"},
	}, Options{Dirs: dirs, NoRefresh: true})
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(res.Apps[0].DesktopFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "MimeType=image/vnd.djvu;\n") {
		t.Errorf("MimeType is not deduplicated:\n%s", body)
	}
	// Both extensions still need their own glob wherever one is declared.
	if len(res.MIMETypes) != 2 {
		t.Errorf("MIMETypes = %v, want one entry per declared extension", res.MIMETypes)
	}
}

func TestIntegrateNoAssociations(t *testing.T) {
	fakeMIMEDatabase(t, "50:text/plain:*.txt")
	dirs := testDirs(t)
	res, err := Integrate(Package{
		ID: "Vendor.Demo", Name: "Demo", Prefix: fakePrefix(t), Extensions: []string{".txt"},
	}, Options{Dirs: dirs, NoAssociations: true, NoRefresh: true})
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(res.Apps[0].DesktopFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "MimeType=") {
		t.Errorf("-no-associations still wrote MimeType:\n%s", body)
	}
	if res.MIMEFile != "" {
		t.Error("-no-associations still wrote a MIME declaration")
	}
}

func TestRemoveByGlobDoesNotTakeSiblingPackages(t *testing.T) {
	// Mozilla.Firefox and Mozilla.Firefox.ESR are different packages whose
	// generated filenames share a prefix. A single wide glob would take both.
	dirs := testDirs(t)
	if err := os.MkdirAll(dirs.Applications, 0o755); err != nil {
		t.Fatal(err)
	}
	names := []string{
		"viniget-Mozilla.Firefox.desktop",
		"viniget-Mozilla.Firefox-safe-mode.desktop",
		"viniget-Mozilla.Firefox.ESR.desktop",
	}
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dirs.Applications, n), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	removed, err := Remove(dirs, "Mozilla.Firefox", nil)
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(removed)
	if len(removed) != 2 {
		t.Fatalf("removed %v, want only the two Mozilla.Firefox files", removed)
	}
	if _, err := os.Stat(filepath.Join(dirs.Applications, "viniget-Mozilla.Firefox.ESR.desktop")); err != nil {
		t.Error("removing Mozilla.Firefox took Mozilla.Firefox.ESR with it")
	}
}

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"Git Bash":           "git-bash",
		"7-Zip File Manager": "7-zip-file-manager",
		"Demo (x64)":         "demo-x64",
		"  ":                 "",
	}
	for in, want := range cases {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q) = %q, want %q", in, got, want)
		}
	}
}

func hasNote(res *Result, substr string) bool {
	for _, n := range res.Notes {
		if strings.Contains(n, substr) {
			return true
		}
	}
	return false
}

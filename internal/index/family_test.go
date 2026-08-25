package index

import "testing"

// fixture mirrors the shapes that matter: a Mozilla locale fork set, and the
// near-misses that a looser rule would wrongly fold.
func fixture() []Package {
	return []Package{
		{ID: "Mozilla.Firefox", Name: "Mozilla Firefox (en-US)"},
		{ID: "Mozilla.Firefox.af", Name: "Mozilla Firefox (af)"},
		{ID: "Mozilla.Firefox.zh-CN", Name: "Mozilla Firefox (zh-CN)"},
		{ID: "Mozilla.Firefox.ESR", Name: "Mozilla Firefox ESR (en-US)"},
		{ID: "Mozilla.Firefox.ESR.af", Name: "Mozilla Firefox ESR (af)"},
		{ID: "Mozilla.Firefox.DeveloperEdition", Name: "Mozilla Firefox Developer Edition (en-US)"},

		// Distinct products whose trailing segment is not a locale.
		{ID: "Microsoft.DotNet.SDK", Name: "Microsoft .NET SDK"},
		{ID: "Microsoft.DotNet.SDK.10", Name: "Microsoft .NET SDK 10.0"},
		{ID: "JetBrains.PyCharm", Name: "PyCharm"},
		{ID: "JetBrains.PyCharm.EAP", Name: "PyCharm (EAP)"},
		{ID: "OpenJS.NodeJS", Name: "Node.js"},
		{ID: "OpenJS.NodeJS.LTS", Name: "Node.js (LTS)"},

		// Trailing segments that a shape regex reads as language codes.
		{ID: "Corsinvest.cv4pve", Name: "cv4pve"},
		{ID: "Corsinvest.cv4pve.cli", Name: "cv4pve-cli"},
		{ID: "tbillington.kondo", Name: "kondo"},
		{ID: "tbillington.kondo.ui", Name: "kondo-ui"},
		{ID: "fstubner.netscli", Name: "netscli"},
		{ID: "fstubner.netscli.gui", Name: "NetsCLI Desktop"},

		// Locale-shaped tail whose parent is absent from the index.
		{ID: "Orphan.Package.af", Name: "Orphan Package (af)"},
	}
}

func TestFoldCollapsesLocaleForks(t *testing.T) {
	f := NewFolder(fixture())
	cases := map[string]string{
		"Mozilla.Firefox.af":     "Mozilla.Firefox",
		"Mozilla.Firefox.zh-CN":  "Mozilla.Firefox",
		"Mozilla.Firefox.ESR.af": "Mozilla.Firefox.ESR",
	}
	for in, want := range cases {
		if got := f.Fold(in); got != want {
			t.Errorf("Fold(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFoldKeepsDistinctProducts(t *testing.T) {
	f := NewFolder(fixture())
	// Every one of these is a real product that must survive as its own row.
	untouched := []string{
		"Mozilla.Firefox",
		"Mozilla.Firefox.ESR",
		"Mozilla.Firefox.DeveloperEdition",
		"Microsoft.DotNet.SDK.10", // numeric tail, not a locale
		"JetBrains.PyCharm.EAP",   // name is tagged "(EAP)" but EAP is not a locale
		"OpenJS.NodeJS.LTS",       // likewise "(LTS)"
		"Corsinvest.cv4pve.cli",   // "cli" matches ^[a-z]{2,3}$ but is not a locale
		"tbillington.kondo.ui",    // "ui" likewise
		"fstubner.netscli.gui",    // "gui" likewise
		"Orphan.Package.af",       // real locale tail, but no parent package
	}
	for _, id := range untouched {
		if got := f.Fold(id); got != id {
			t.Errorf("Fold(%q) = %q, want it unchanged", id, got)
		}
	}
}

func TestFoldNeverGoesBelowPublisherPackage(t *testing.T) {
	// "af" is a locale subtag, but Mozilla alone is not a package identifier.
	f := NewFolder([]Package{
		{ID: "Mozilla", Name: "Mozilla (af)"},
		{ID: "Mozilla.af", Name: "Mozilla (af)"},
	})
	if got := f.Fold("Mozilla.af"); got != "Mozilla.af" {
		t.Errorf("Fold(%q) = %q, want it unchanged", "Mozilla.af", got)
	}
}

func TestFoldRequiresNameTag(t *testing.T) {
	// Parent exists and the tail is a locale, but the display name does not
	// confirm it -- so this is some other kind of variant.
	f := NewFolder([]Package{
		{ID: "Vendor.Tool", Name: "Vendor Tool"},
		{ID: "Vendor.Tool.it", Name: "Vendor Tool IT Edition"},
	})
	if got := f.Fold("Vendor.Tool.it"); got != "Vendor.Tool.it" {
		t.Errorf("Fold(%q) = %q, want it unchanged", "Vendor.Tool.it", got)
	}
}

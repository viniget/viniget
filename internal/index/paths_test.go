package index

import "testing"

func TestManifestBase(t *testing.T) {
	// Expected values are the real repository paths, cross-checked against the
	// v1 pathparts tree for every package in the catalog.
	cases := []struct {
		id, version, want string
	}{
		{"7zip.7zip", "26.02", "manifests/7/7zip/7zip/26.02/7zip.7zip"},
		{"WinMerge.WinMerge", "2.16.58", "manifests/w/WinMerge/WinMerge/2.16.58/WinMerge.WinMerge"},
		{"Mozilla.Firefox.zh-CN", "153.0.4", "manifests/m/Mozilla/Firefox/zh-CN/153.0.4/Mozilla.Firefox.zh-CN"},
		{
			"Microsoft.VisualStudio.2022.Community", "17.14.39",
			"manifests/m/Microsoft/VisualStudio/2022/Community/17.14.39/Microsoft.VisualStudio.2022.Community",
		},
		// Lowercase publisher: the shard letter is folded, the identifier is not.
		{"presono.presono", "3.27.5", "manifests/p/presono/presono/3.27.5/presono.presono"},
		{"voidtools.Everything", "1.4.1.1032", "manifests/v/voidtools/Everything/1.4.1.1032/voidtools.Everything"},
	}
	for _, c := range cases {
		if got := ManifestBase(c.id, c.version); got != c.want {
			t.Errorf("ManifestBase(%q, %q)\n got %q\nwant %q", c.id, c.version, got, c.want)
		}
	}
}

func TestManifestBaseIncomplete(t *testing.T) {
	if got := ManifestBase("", "1.0"); got != "" {
		t.Errorf("empty id: got %q, want %q", got, "")
	}
	if got := ManifestBase("A.B", ""); got != "" {
		t.Errorf("empty version: got %q, want %q", got, "")
	}
}

func TestManifestURL(t *testing.T) {
	got := ManifestURL("7zip.7zip", "26.02", ".installer.yaml")
	want := RAW + "/manifests/7/7zip/7zip/26.02/7zip.7zip.installer.yaml"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

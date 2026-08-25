package index

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// v2Schema is the real schema of the published source2.msix index, copied
// verbatim so the tests exercise the same column names and types as production.
const v2Schema = `
CREATE TABLE [metadata]([name] TEXT PRIMARY KEY NOT NULL, [value] TEXT NOT NULL) WITHOUT ROWID;
CREATE TABLE [packages](rowid INTEGER PRIMARY KEY, [id] TEXT NOT NULL, [name] TEXT NOT NULL,
  [moniker] TEXT, [latest_version] TEXT NOT NULL, [arp_min_version] TEXT,
  [arp_max_version] TEXT, [hash] BLOB);
CREATE TABLE [tags2](rowid INTEGER PRIMARY KEY, [tag] TEXT NOT NULL);
CREATE TABLE [tags2_map]([tag] INT64 NOT NULL, [package] INT64 NOT NULL,
  PRIMARY KEY([tag], [package])) WITHOUT ROWID;
CREATE TABLE [commands2](rowid INTEGER PRIMARY KEY, [command] TEXT NOT NULL);
CREATE TABLE [commands2_map]([command] INT64 NOT NULL, [package] INT64 NOT NULL,
  PRIMARY KEY([command], [package])) WITHOUT ROWID;
`

type seedPkg struct {
	id, name, moniker, version string
	tags                       []string
	commands                   []string
}

func seedCatalog(t *testing.T) *Catalog {
	t.Helper()
	path := filepath.Join(t.TempDir(), "index-v2.db")

	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(v2Schema); err != nil {
		t.Fatal(err)
	}

	pkgs := []seedPkg{
		{id: "Mozilla.Firefox", name: "Mozilla Firefox (en-US)", moniker: "firefox", version: "153.0.4",
			tags: []string{"browser"}, commands: []string{"firefox"}},
		{id: "Mozilla.Firefox.af", name: "Mozilla Firefox (af)", moniker: "firefox", version: "154.0"},
		{id: "Mozilla.Firefox.zh-CN", name: "Mozilla Firefox (zh-CN)", moniker: "firefox", version: "154.0"},
		{id: "Mozilla.Firefox.ESR", name: "Mozilla Firefox ESR (en-US)", moniker: "firefox", version: "140.6.0"},
		{id: "Mozilla.Firefox.ESR.af", name: "Mozilla Firefox ESR (af)", moniker: "firefox", version: "140.6.0"},
		{id: "filips.FirefoxPWA", name: "PWAsForFirefox", moniker: "firefoxpwa", version: "2.19.0"},

		{id: "Microsoft.VisualStudioCode", name: "Microsoft Visual Studio Code", moniker: "vscode",
			version: "1.134.0", tags: []string{"editor"}, commands: []string{"code"}},
		{id: "Microsoft.VisualStudio.2022.Community", name: "Visual Studio Community 2022",
			version: "17.14.39", tags: []string{"editor"}},

		{id: "Microsoft.DotNet.SDK.10", name: "Microsoft .NET SDK 10.0", version: "10.0.100"},
		{id: "Microsoft.DotNet.Runtime.10", name: "Microsoft .NET Runtime 10.0", version: "10.0.0"},

		{id: "JetBrains.PyCharm", name: "PyCharm", version: "2026.2"},
		{id: "JetBrains.PyCharm.EAP", name: "PyCharm (EAP)", version: "2026.3"},

		{id: "7zip.7zip", name: "7-Zip", moniker: "7zip", version: "26.02",
			tags: []string{"archive"}, commands: []string{"7z"}},
		{id: "WinMerge.WinMerge", name: "WinMerge", moniker: "winmerge", version: "2.16.58",
			tags: []string{"diff"}},
		{id: "voidtools.Everything", name: "Everything", moniker: "everything", version: "1.4.1.1032",
			tags: []string{"search"}},
		{id: "Notepad++.Notepad++", name: "Notepad++", moniker: "notepad++", version: "8.9.8",
			tags: []string{"editor"}, commands: []string{"notepad++"}},
	}

	tagID := map[string]int64{}
	cmdID := map[string]int64{}
	for i, p := range pkgs {
		row := int64(i + 1)
		if _, err := db.Exec(
			"insert into packages(rowid, id, name, moniker, latest_version) values(?,?,?,?,?)",
			row, p.id, p.name, nullable(p.moniker), p.version); err != nil {
			t.Fatal(err)
		}
		for _, tag := range p.tags {
			id, ok := tagID[tag]
			if !ok {
				id = int64(len(tagID) + 1)
				tagID[tag] = id
				if _, err := db.Exec("insert into tags2(rowid, tag) values(?,?)", id, tag); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := db.Exec("insert into tags2_map(tag, package) values(?,?)", id, row); err != nil {
				t.Fatal(err)
			}
		}
		for _, cmd := range p.commands {
			id, ok := cmdID[cmd]
			if !ok {
				id = int64(len(cmdID) + 1)
				cmdID[cmd] = id
				if _, err := db.Exec("insert into commands2(rowid, command) values(?,?)", id, cmd); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := db.Exec("insert into commands2_map(command, package) values(?,?)", id, row); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	cat, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cat.Close() })
	return cat
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func ids(results []Result) []string {
	out := make([]string, len(results))
	for i, r := range results {
		out[i] = r.ID
	}
	return out
}

func TestSearchRanksExactMonikerFirst(t *testing.T) {
	cat := seedCatalog(t)
	got, err := cat.Search("vscode", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 || got[0].ID != "Microsoft.VisualStudioCode" {
		t.Fatalf("search vscode = %v, want Microsoft.VisualStudioCode first", ids(got))
	}
}

func TestSearchRanksExactIDFirst(t *testing.T) {
	cat := seedCatalog(t)
	// The substring "7zip" also matches the identifier, but the exact-id tier wins.
	got, err := cat.Search("7zip.7zip", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 || got[0].ID != "7zip.7zip" {
		t.Fatalf("search 7zip.7zip = %v, want 7zip.7zip first", ids(got))
	}
}

func TestSearchRanksExactCommand(t *testing.T) {
	cat := seedCatalog(t)
	// "code" is a command on VS Code and a substring of several other names.
	got, err := cat.Search("code", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 || got[0].ID != "Microsoft.VisualStudioCode" {
		t.Fatalf("search code = %v, want Microsoft.VisualStudioCode first", ids(got))
	}
}

func TestSearchFoldsLocaleForks(t *testing.T) {
	cat := seedCatalog(t)
	got, err := cat.Search("firefox", 50)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"Mozilla.Firefox", "Mozilla.Firefox.ESR", "filips.FirefoxPWA"}
	if len(got) != len(want) {
		t.Fatalf("search firefox = %v, want exactly %v", ids(got), want)
	}
	for i, id := range want {
		if got[i].ID != id {
			t.Errorf("result %d = %q, want %q (full: %v)", i, got[i].ID, id, ids(got))
		}
	}
	if got[0].Locales != 2 {
		t.Errorf("Mozilla.Firefox folded %d locales, want 2", got[0].Locales)
	}
	if got[1].Locales != 1 {
		t.Errorf("Mozilla.Firefox.ESR folded %d locales, want 1", got[1].Locales)
	}
}

func TestSearchKeepsProductVariantsDistinct(t *testing.T) {
	cat := seedCatalog(t)
	got, err := cat.Search("dotnet", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("search dotnet = %v, want both SDK and Runtime", ids(got))
	}
	got, err = cat.Search("pycharm", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("search pycharm = %v, want PyCharm and PyCharm.EAP kept apart", ids(got))
	}
}

func TestSearchByTag(t *testing.T) {
	cat := seedCatalog(t)
	got, err := cat.Search("editor", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) < 3 {
		t.Fatalf("search editor = %v, want the three tagged packages", ids(got))
	}
}

func TestSearchWildcardsAreLiteral(t *testing.T) {
	cat := seedCatalog(t)
	// "%" must not act as a LIKE wildcard matching every package.
	got, err := cat.Search("%", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("search %% = %v, want no matches", ids(got))
	}
}

func TestSearchLimit(t *testing.T) {
	cat := seedCatalog(t)
	got, err := cat.Search("e", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) > 3 {
		t.Errorf("search returned %d results, want at most 3", len(got))
	}
}

func TestGet(t *testing.T) {
	cat := seedCatalog(t)
	p, err := cat.Get("7ZIP.7ZIP") // identifiers are matched case-insensitively
	if err != nil {
		t.Fatal(err)
	}
	if p == nil || p.ID != "7zip.7zip" || p.Version != "26.02" {
		t.Fatalf("Get = %+v, want 7zip.7zip 26.02", p)
	}
	missing, err := cat.Get("No.Such.Package")
	if err != nil {
		t.Fatal(err)
	}
	if missing != nil {
		t.Errorf("Get(missing) = %+v, want nil", missing)
	}
}

func TestFamilies(t *testing.T) {
	cat := seedCatalog(t)
	packages, families, err := cat.Families()
	if err != nil {
		t.Fatal(err)
	}
	if packages != 16 {
		t.Errorf("packages = %d, want 16", packages)
	}
	// Three Firefox locale forks fold away: .af, .zh-CN, and .ESR.af.
	if families != 13 {
		t.Errorf("families = %d, want 13", families)
	}
}

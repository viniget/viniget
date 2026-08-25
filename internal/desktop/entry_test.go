package desktop

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEntryRender(t *testing.T) {
	e := &Entry{
		Name:       "Demo",
		Comment:    "A demo\nwith a newline",
		Exec:       ExecLine("/home/u/.local/share/viniget/launchers/Vendor.Demo"),
		Path:       "/prefix/drive_c/Program Files/Demo",
		Icon:       "viniget-Vendor.Demo",
		Categories: []string{"Utility", "TextEditor"},
		Keywords:   []string{"demo", "editor"},
		MimeTypes:  []string{"text/plain"},
		Package:    "Vendor.Demo",
		Prefix:     "/prefix",
		Version:    "1.2.3",
	}
	got := string(e.Render())

	for _, want := range []string{
		"[Desktop Entry]\nType=Application\n",
		"Name=Demo\n",
		`Comment=A demo\nwith a newline` + "\n",
		"Exec=/home/u/.local/share/viniget/launchers/Vendor.Demo %F\n",
		"Categories=Utility;TextEditor;\n",
		"Keywords=demo;editor;\n",
		"MimeType=text/plain;\n",
		"Terminal=false\n",
		"X-Viniget-Package=Vendor.Demo\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered entry missing %q:\n%s", want, got)
		}
	}
	// A newline in a value would end the key early and corrupt everything
	// after it.
	if strings.Contains(got, "A demo\nwith") {
		t.Error("a literal newline survived into the value")
	}
	// Keys with no value are omitted rather than written empty, which
	// desktop-file-validate rejects for some of them.
	if strings.Contains(got, "GenericName=") {
		t.Error("empty key written")
	}
}

func TestExecArgQuoting(t *testing.T) {
	cases := map[string]string{
		"/home/u/launchers/Vendor.Demo": "/home/u/launchers/Vendor.Demo",
		"/home/My Files/launchers/Demo": `"/home/My Files/launchers/Demo"`,
		`/home/u/we"ird`:                `"/home/u/we\"ird"`,
		"/home/u/$HOME":                 `"/home/u/\$HOME"`,
		"":                              `""`,
	}
	for in, want := range cases {
		if got := execArg(in); got != want {
			t.Errorf("execArg(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestExecLineSurvivesValueEscaping(t *testing.T) {
	// The Exec key is quoted first and escaped second, so a backslash in a
	// path ends up doubled twice. Both layers have to be there or the
	// launcher path comes out wrong on the way back in.
	e := &Entry{Exec: ExecLine(`/home/u/back\slash dir/Demo`)}
	got := string(e.Render())
	if !strings.Contains(got, `Exec="/home/u/back\\\\slash dir/Demo" %F`) {
		t.Errorf("Exec line = %q", got)
	}
	// The field code must not be quoted or it is passed as a literal argument.
	if strings.Contains(got, `%F"`) {
		t.Error("the field code was swallowed by the quoting")
	}
}

func TestCategories(t *testing.T) {
	cases := []struct {
		tags []string
		want string
	}{
		{[]string{"chat", "messaging"}, "Network;InstantMessaging;"},
		{[]string{"editor", "text-editor"}, "Utility;TextEditor;"},
		{nil, "Utility;"},
		{[]string{"nonsense-tag"}, "Utility;"},
	}
	for _, c := range cases {
		if got := list(Categories(c.tags)); got != c.want {
			t.Errorf("Categories(%v) = %q, want %q", c.tags, got, c.want)
		}
	}
	// A subcategory on its own files an entry in no menu at all, so a main
	// category is always present.
	for _, tags := range [][]string{{"ide"}, {"pdf"}, {"emulator"}, {"password"}} {
		got := Categories(tags)
		found := false
		for _, c := range got {
			found = found || mainCategories[c]
		}
		if !found {
			t.Errorf("Categories(%v) = %v, with no main category", tags, got)
		}
	}
}

func TestKeywords(t *testing.T) {
	got := Keywords([]string{"Demo", "editor", "Editor", "", "  ", "text"})
	if len(got) != 3 {
		t.Fatalf("Keywords = %v, want three unique entries", got)
	}
	if len(Keywords(make([]string, 40))) != 0 {
		t.Error("blank tags produced keywords")
	}
}

func TestWriteFileIsAtomic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "demo.desktop")
	if err := writeFile(path, []byte("first"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeFile(path, []byte("second"), 0o755); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil || string(b) != "second" {
		t.Fatalf("content = %q, err = %v", b, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o755 {
		t.Errorf("mode = %v, err = %v", info.Mode().Perm(), err)
	}
	// The temporary file must not be left behind for a desktop environment
	// to index.
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Error("temporary file left on disk")
	}
}

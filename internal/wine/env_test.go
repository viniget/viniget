package wine

import (
	"strings"
	"testing"
)

// envValue reads one variable out of a composed environment, last write wins,
// the way exec.Cmd resolves duplicates.
func envValue(env []string, key string) (string, bool) {
	out, ok := "", false
	for _, kv := range env {
		if v, found := strings.CutPrefix(kv, key+"="); found {
			out, ok = v, true
		}
	}
	return out, ok
}

// The menu builder has to be off for every command, not only the install:
// wine writes its own .desktop files into the user's menu from inside the
// prefix, naming whatever WINEPREFIX it saw -- which mid-install is the
// ".installing" staging directory that is renamed away seconds later.
func TestEnvAlwaysDisablesTheMenuBuilder(t *testing.T) {
	r := &Runner{Prefix: "/tmp/pfx"}
	got, ok := envValue(r.Env(), "WINEDLLOVERRIDES")
	if !ok {
		t.Fatal("no WINEDLLOVERRIDES in the environment")
	}
	if got != NoMenuBuilder {
		t.Errorf("WINEDLLOVERRIDES = %q, want %q", got, NoMenuBuilder)
	}
}

// The runtime stubs and the menu builder share one variable, so setting one
// must not drop the other.
func TestEnvKeepsRuntimeStubsAndTheMenuBuilderTogether(t *testing.T) {
	r := &Runner{Prefix: "/tmp/pfx", NoRuntimePrompts: true}
	got, _ := envValue(r.Env(), "WINEDLLOVERRIDES")
	if !strings.Contains(got, "mscoree,mshtml=") {
		t.Errorf("WINEDLLOVERRIDES = %q, want the runtime stubs", got)
	}
	if !strings.Contains(got, NoMenuBuilder) {
		t.Errorf("WINEDLLOVERRIDES = %q, want the menu builder disabled", got)
	}
}

// An inherited value is somebody debugging a package, and is kept. viniget's
// own overrides go last so that they win the ones they name.
func TestEnvKeepsAnInheritedOverride(t *testing.T) {
	t.Setenv("WINEDLLOVERRIDES", "sapi=d")
	r := &Runner{Prefix: "/tmp/pfx"}
	got, _ := envValue(r.Env(), "WINEDLLOVERRIDES")
	if got != "sapi=d;"+NoMenuBuilder {
		t.Errorf("WINEDLLOVERRIDES = %q, want the inherited value first", got)
	}
}

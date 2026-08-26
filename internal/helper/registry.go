// Package helper provisions optional runtimes inside viniget Wine prefixes.
//
// The public helper names are deliberately curated Winetricks verbs. Passing
// arbitrary verbs through would turn a runtime feature into an unbounded
// shell-script interface and make installed-package state impossible to
// interpret reliably.
package helper

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/viniget/viniget/internal/manifest"
)

const DefaultTimeout = 20 * time.Minute

type Source string

const (
	SourceRecorded   Source = "recorded"
	SourceExplicit   Source = "explicit"
	SourceDependency Source = "dependency"
)

type Status string

const (
	StatusInstalled      Status = "installed"
	StatusAlreadyPresent Status = "already-present"
	StatusFailed         Status = "failed"
	StatusUnavailable    Status = "unavailable"
)

// Definition is one helper viniget permits the user to invoke.
type Definition struct {
	Verb  string `json:"verb"`
	Title string `json:"title"`
}

var definitions = []Definition{
	{Verb: "dotnet11", Title: ".NET Framework 1.1"},
	{Verb: "dotnet11sp1", Title: ".NET Framework 1.1 SP1"},
	{Verb: "dotnet20", Title: ".NET Framework 2.0"},
	{Verb: "dotnet20sp1", Title: ".NET Framework 2.0 SP1"},
	{Verb: "dotnet20sp2", Title: ".NET Framework 2.0 SP2"},
	{Verb: "dotnet30", Title: ".NET Framework 3.0"},
	{Verb: "dotnet30sp1", Title: ".NET Framework 3.0 SP1"},
	{Verb: "dotnet35", Title: ".NET Framework 3.5"},
	{Verb: "dotnet35sp1", Title: ".NET Framework 3.5 SP1"},
	{Verb: "dotnet40", Title: ".NET Framework 4.0"},
	{Verb: "dotnet40_kb2468871", Title: ".NET Framework 4.0 KB2468871"},
	{Verb: "dotnet45", Title: ".NET Framework 4.5"},
	{Verb: "dotnet452", Title: ".NET Framework 4.5.2"},
	{Verb: "dotnet46", Title: ".NET Framework 4.6"},
	{Verb: "dotnet461", Title: ".NET Framework 4.6.1"},
	{Verb: "dotnet462", Title: ".NET Framework 4.6.2"},
	{Verb: "dotnet471", Title: ".NET Framework 4.7.1"},
	{Verb: "dotnet472", Title: ".NET Framework 4.7.2"},
	{Verb: "dotnet48", Title: ".NET Framework 4.8"},
	{Verb: "dotnetcore2", Title: ".NET Core Runtime 2.1"},
	{Verb: "dotnetcore3", Title: ".NET Core Runtime 3.1"},
	{Verb: "dotnetcoredesktop3", Title: ".NET Core Desktop Runtime 3.1"},
	{Verb: "dotnet6", Title: ".NET Runtime 6"},
	{Verb: "dotnet7", Title: ".NET Runtime 7"},
	{Verb: "dotnet8", Title: ".NET Runtime 8"},
	{Verb: "dotnet9", Title: ".NET Runtime 9"},
	{Verb: "dotnet10", Title: ".NET Runtime 10"},
	{Verb: "dotnetdesktop6", Title: ".NET Desktop Runtime 6"},
	{Verb: "dotnetdesktop7", Title: ".NET Desktop Runtime 7"},
	{Verb: "dotnetdesktop8", Title: ".NET Desktop Runtime 8"},
	{Verb: "dotnetdesktop9", Title: ".NET Desktop Runtime 9"},
	{Verb: "dotnetdesktop10", Title: ".NET Desktop Runtime 10"},
}

var known = func() map[string]Definition {
	m := make(map[string]Definition, len(definitions))
	for _, d := range definitions {
		m[d.Verb] = d
	}
	return m
}()

// Definitions returns the stable public helper catalog.
func Definitions() []Definition { return append([]Definition(nil), definitions...) }

// Lookup validates and normalizes a helper name.
func Lookup(name string) (Definition, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	if d, ok := known[name]; ok {
		return d, nil
	}
	return Definition{}, fmt.Errorf("unknown helper %q (try: viniget helper list)", name)
}

// Request is one helper application, including why it was requested and the
// runtime evidence an inferred dependency must satisfy.
type Request struct {
	Verb           string `json:"verb"`
	Source         Source `json:"source"`
	Dependency     string `json:"dependency,omitempty"`
	MinimumVersion string `json:"minimum_version,omitempty"`
	RuntimeFamily  string `json:"-"`
	RuntimeMajor   int    `json:"-"`
	Architecture   string `json:"-"`
}

// Infer maps only dependency identifiers whose Winetricks equivalent is
// exact. Everything else is returned as a note rather than guessed at.
func Infer(d *manifest.Dependencies, architecture string) ([]Request, []string) {
	if d == nil {
		return nil, nil
	}
	var requests []Request
	var notes []string
	for _, dep := range d.PackageDependencies {
		r, ok := packageRequest(dep, architecture)
		if !ok {
			notes = append(notes, fmt.Sprintf("dependency %s is not handled by a viniget helper", dep.PackageIdentifier))
			continue
		}
		requests = mergeInferred(requests, r)
	}
	for _, feature := range d.WindowsFeatures {
		if strings.EqualFold(feature, "NetFx3") {
			requests = mergeInferred(requests, Request{
				Verb: "dotnet35sp1", Source: SourceDependency,
				Dependency: "Windows feature " + feature, Architecture: architecture,
			})
		} else {
			notes = append(notes, fmt.Sprintf("Windows feature %s is not handled by a viniget helper", feature))
		}
	}
	for _, library := range d.WindowsLibraries {
		notes = append(notes, fmt.Sprintf("Windows library %s is not handled by a viniget helper", library))
	}
	for _, external := range d.ExternalDependencies {
		notes = append(notes, fmt.Sprintf("external dependency %s is not handled by a viniget helper", external))
	}
	return requests, notes
}

func packageRequest(dep manifest.PackageDependency, architecture string) (Request, bool) {
	id := strings.ToLower(dep.PackageIdentifier)
	r := Request{
		Source: SourceDependency, Dependency: dep.PackageIdentifier,
		MinimumVersion: dep.MinimumVersion, Architecture: architecture,
	}
	switch id {
	case "microsoft.dotnet.framework.redistributable.1_1":
		r.Verb = "dotnet11"
		return r, true
	case "microsoft.dotnet.runtime.3_1":
		r.Verb, r.RuntimeFamily, r.RuntimeMajor = "dotnetcore3", "Microsoft.NETCore.App", 3
		return r, true
	case "microsoft.dotnet.desktopruntime.3_1":
		r.Verb, r.RuntimeFamily, r.RuntimeMajor = "dotnetcoredesktop3", "Microsoft.WindowsDesktop.App", 3
		return r, true
	}
	for major := 6; major <= 10; major++ {
		suffix := strconv.Itoa(major)
		switch id {
		case "microsoft.dotnet.runtime." + suffix:
			r.Verb, r.RuntimeFamily, r.RuntimeMajor = "dotnet"+suffix, "Microsoft.NETCore.App", major
			return r, true
		case "microsoft.dotnet.desktopruntime." + suffix:
			r.Verb, r.RuntimeFamily, r.RuntimeMajor = "dotnetdesktop"+suffix, "Microsoft.WindowsDesktop.App", major
			return r, true
		}
	}
	return Request{}, false
}

func mergeInferred(list []Request, add Request) []Request {
	for i := range list {
		if list[i].Verb != add.Verb {
			continue
		}
		if compareVersions(add.MinimumVersion, list[i].MinimumVersion) > 0 {
			list[i].MinimumVersion = add.MinimumVersion
		}
		return list
	}
	return append(list, add)
}

// Merge combines durable, command-line and inferred requests. The first
// source wins, so an explicit request is never downgraded to best effort by a
// duplicate manifest dependency.
func Merge(recorded, explicit []string, inferred []Request, architecture string) (required, automatic []Request, err error) {
	seen := map[string]bool{}
	addRequired := func(names []string, source Source) error {
		for _, name := range names {
			d, lookupErr := Lookup(name)
			if lookupErr != nil {
				return lookupErr
			}
			if seen[d.Verb] {
				continue
			}
			seen[d.Verb] = true
			required = append(required, Request{Verb: d.Verb, Source: source, Architecture: architecture})
		}
		return nil
	}
	if err := addRequired(recorded, SourceRecorded); err != nil {
		return nil, nil, err
	}
	if err := addRequired(explicit, SourceExplicit); err != nil {
		return nil, nil, err
	}
	for _, r := range inferred {
		if seen[r.Verb] {
			continue
		}
		seen[r.Verb] = true
		automatic = append(automatic, r)
	}
	return required, automatic, nil
}

// SuccessfulVerbs returns every unique helper that finished successfully.
func SuccessfulVerbs(results []Result) []string {
	seen := map[string]bool{}
	var verbs []string
	for _, r := range results {
		if r.Status != StatusInstalled && r.Status != StatusAlreadyPresent || seen[r.Verb] {
			continue
		}
		seen[r.Verb] = true
		verbs = append(verbs, r.Verb)
	}
	return verbs
}

func compareVersions(a, b string) int {
	av, aok := numericVersion(a)
	bv, bok := numericVersion(b)
	if !aok && !bok {
		return strings.Compare(a, b)
	}
	if !aok {
		return -1
	}
	if !bok {
		return 1
	}
	n := len(av)
	if len(bv) > n {
		n = len(bv)
	}
	for i := 0; i < n; i++ {
		var x, y int
		if i < len(av) {
			x = av[i]
		}
		if i < len(bv) {
			y = bv[i]
		}
		if x < y {
			return -1
		}
		if x > y {
			return 1
		}
	}
	return 0
}

func numericVersion(s string) ([]int, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	if s == "" {
		return nil, false
	}
	parts := strings.FieldsFunc(s, func(r rune) bool { return r == '.' || r == '-' || r == '+' || r == '_' })
	var out []int
	for _, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil {
			break
		}
		out = append(out, n)
	}
	return out, len(out) > 0
}

package index

import "strings"

// RAW is the manifest origin. The winget CDN serves the source index but not
// the manifests themselves -- every path shape there returns BlobNotFound.
const RAW = "https://raw.githubusercontent.com/microsoft/winget-pkgs/master"

// ManifestBase returns the repo-relative path prefix shared by a package's
// manifest files, without the trailing file suffix:
//
//	Mozilla.Firefox 153.0.4
//	  -> manifests/m/Mozilla/Firefox/153.0.4/Mozilla.Firefox
//
// The layout is derivable from the identifier alone: lowercase first letter,
// then the identifier split on ".", then the version. Verified against every
// one of the 164,828 manifest paths in the v1 pathparts tree -- all matched.
// It is an invariant of the winget-pkgs repository rather than a documented
// contract, so callers fall back to PathIndex when a derived URL 404s.
func ManifestBase(id, version string) string {
	if id == "" || version == "" {
		return ""
	}
	shard := strings.ToLower(id[:1])
	segs := strings.Split(id, ".")
	return "manifests/" + shard + "/" + strings.Join(segs, "/") + "/" + version + "/" + id
}

// ManifestURL builds an absolute URL for one manifest file. Suffix is the part
// after the identifier: ".yaml", ".installer.yaml", ".locale.en-US.yaml".
func ManifestURL(id, version, suffix string) string {
	base := ManifestBase(id, version)
	if base == "" {
		return ""
	}
	return RAW + "/" + base + suffix
}

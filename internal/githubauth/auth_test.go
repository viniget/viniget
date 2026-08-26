package githubauth

import (
	"net/http"
	"testing"
)

func TestTokenFromEnvPrecedence(t *testing.T) {
	t.Setenv("VINIGET_GH_TOKEN", "viniget")
	t.Setenv("GH_TOKEN", "gh")
	t.Setenv("GITHUB_TOKEN", "github")
	if got := TokenFromEnv(); got != "viniget" {
		t.Fatalf("TokenFromEnv() = %q, want viniget", got)
	}

	t.Setenv("VINIGET_GH_TOKEN", "  ")
	if got := TokenFromEnv(); got != "gh" {
		t.Fatalf("TokenFromEnv() after empty viniget token = %q, want gh", got)
	}

	t.Setenv("GH_TOKEN", "")
	if got := TokenFromEnv(); got != "github" {
		t.Fatalf("TokenFromEnv() after empty gh token = %q, want github", got)
	}
}

func TestAddOnlyAuthenticatesGitHub(t *testing.T) {
	for _, tc := range []struct {
		url  string
		want string
	}{
		{"https://github.com/owner/repo/releases/download/v1/app.exe", "Bearer secret"},
		{"https://api.github.com/repos/owner/repo/releases", "Bearer secret"},
		{"https://raw.githubusercontent.com/owner/repo/main/file", "Bearer secret"},
		{"https://release-assets.githubusercontent.com/file", "Bearer secret"},
		{"http://github.com/owner/repo/file", ""},
		{"https://github.com.evil.example/file", ""},
		{"https://raw.githubusercontent.com.evil.example/file", ""},
		{"https://vendor.example/file", ""},
	} {
		t.Run(tc.url, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodGet, tc.url, nil)
			if err != nil {
				t.Fatal(err)
			}
			Add(req, " secret ")
			if got := req.Header.Get("Authorization"); got != tc.want {
				t.Errorf("Authorization = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAddPreservesExplicitAuthorization(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "https://github.com/file", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Basic existing")
	Add(req, "secret")
	if got := req.Header.Get("Authorization"); got != "Basic existing" {
		t.Fatalf("Authorization = %q, want existing header preserved", got)
	}
}

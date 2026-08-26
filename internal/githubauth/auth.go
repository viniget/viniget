// Package githubauth applies optional GitHub authentication to HTTP requests.
package githubauth

import (
	"net/http"
	"os"
	"strings"
)

// TokenFromEnv returns the first configured GitHub token.
//
// VINIGET_GH_TOKEN is the explicit viniget setting, analogous to Scoop's
// SCOOP_GH_TOKEN. The standard variables make an existing GitHub CLI or CI
// setup work without duplicating the secret under another name.
func TokenFromEnv() string {
	for _, name := range []string{"VINIGET_GH_TOKEN", "GH_TOKEN", "GITHUB_TOKEN"} {
		if token := strings.TrimSpace(os.Getenv(name)); token != "" {
			return token
		}
	}
	return ""
}

// Add attaches token to a request going to GitHub. Requests to every other
// host are deliberately left alone: installer URLs name arbitrary vendor
// servers, and a credential must never follow the manifest there.
func Add(req *http.Request, token string) {
	token = strings.TrimSpace(token)
	if token == "" || req == nil || req.URL == nil || req.URL.Scheme != "https" ||
		!isGitHubHost(req.URL.Hostname()) {
		return
	}
	if req.Header.Get("Authorization") == "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
}

func isGitHubHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	return host == "github.com" || strings.HasSuffix(host, ".github.com") ||
		host == "githubusercontent.com" || strings.HasSuffix(host, ".githubusercontent.com")
}

package manifest

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestGetAuthenticatesGitHubManifest(t *testing.T) {
	f := &Fetcher{
		GitHubToken: "secret",
		Client: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if got := req.Header.Get("Authorization"); got != "Bearer secret" {
				t.Errorf("Authorization = %q, want Bearer secret", got)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader("manifest")),
				Request:    req,
			}, nil
		})},
	}

	body, err := f.get(context.Background(), "https://raw.githubusercontent.com/owner/repo/main/file")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if string(body) != "manifest" {
		t.Fatalf("body = %q, want manifest", body)
	}
}

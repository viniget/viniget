package install

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

const interstitial = "<html><body>your download will start shortly</body></html>"

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// sourceforgeLike mimics the behaviour that broke CrystalDewWorld.CrystalDiskInfo:
// the payload URL redirects twice before reaching the endpoint that serves it,
// and that endpoint answers a referred request with a landing page instead of
// the file.
func sourceforgeLike(t *testing.T, payload []byte) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/file.exe", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/file.exe/", http.StatusMovedPermanently)
	})
	mux.HandleFunc("/file.exe/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/file.exe/download", http.StatusMovedPermanently)
	})
	mux.HandleFunc("/file.exe/download", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Referer") != "" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write([]byte(interstitial))
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write(payload)
	})
	return httptest.NewServer(mux)
}

func TestGetStripsRefererAcrossRedirects(t *testing.T) {
	payload := []byte("MZ fake installer")
	srv := sourceforgeLike(t, payload)
	defer srv.Close()

	d := NewDownloader(t.TempDir())
	path, err := d.Get(context.Background(), srv.URL+"/file.exe", sha256Hex(payload))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(payload) {
		t.Errorf("downloaded %q, want the installer payload", got)
	}
}

func TestGetRejectsHTMLWhenManifestHasNoDigest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(interstitial))
	}))
	defer srv.Close()

	d := NewDownloader(t.TempDir())
	_, err := d.Get(context.Background(), srv.URL+"/file.exe", "")
	if err == nil {
		t.Fatal("Get accepted a landing page as an installer payload")
	}
	if !strings.Contains(err.Error(), "not an installer payload") {
		t.Errorf("error = %v, want it to name the landing page", err)
	}
}

// A declared digest is a stronger check than the media type, so a server that
// mislabels a real payload must not be turned away.
func TestGetKeepsMislabelledPayloadWithMatchingDigest(t *testing.T) {
	payload := []byte("MZ fake installer")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write(payload)
	}))
	defer srv.Close()

	d := NewDownloader(t.TempDir())
	if _, err := d.Get(context.Background(), srv.URL+"/file.exe", sha256Hex(payload)); err != nil {
		t.Fatalf("Get rejected a payload matching its digest: %v", err)
	}
}

func TestGetMismatchReportsSizeAndType(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(interstitial))
	}))
	defer srv.Close()

	d := NewDownloader(t.TempDir())
	_, err := d.Get(context.Background(), srv.URL+"/file.exe", sha256Hex([]byte("something else")))
	if err == nil {
		t.Fatal("Get accepted a payload that did not match its digest")
	}
	for _, want := range []string{"sha256 mismatch", "text/html", humanBytes(int64(len(interstitial)))} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to mention %q", err, want)
		}
	}
}

func TestGetBoundsRedirects(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, srv.URL+"/next", http.StatusFound)
	}))
	defer srv.Close()

	d := NewDownloader(t.TempDir())
	if _, err := d.Get(context.Background(), srv.URL+"/file.exe", ""); err == nil {
		t.Fatal("Get followed an unbounded redirect loop")
	}
}

func TestGetAuthenticatesGitHubDownload(t *testing.T) {
	d := NewDownloader(t.TempDir())
	d.GitHubToken = "secret"
	d.Client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if got := req.Header.Get("Authorization"); got != "Bearer secret" {
			t.Errorf("Authorization = %q, want Bearer secret", got)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     http.Header{"Content-Type": {"application/octet-stream"}},
			Body:       io.NopCloser(strings.NewReader("payload")),
			Request:    req,
		}, nil
	})

	if _, err := d.Get(context.Background(), "https://github.com/owner/repo/file.exe", ""); err != nil {
		t.Fatalf("Get: %v", err)
	}
}

func TestDescribeType(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"text/html; charset=utf-8", "text/html"},
		{"application/octet-stream", "application/octet-stream"},
		{"", "unknown type"},
		{"not a media type", "unknown type"},
	} {
		if got := describeType(tc.in); got != tc.want {
			t.Errorf("describeType(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

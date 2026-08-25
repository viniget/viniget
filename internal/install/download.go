package install

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// userAgent identifies viniget to vendor download servers. Several of them
// reject an empty agent outright.
const userAgent = "viniget/0.2 (+https://github.com/viniget/viniget)"

// Downloader fetches installer payloads into a content-addressed cache.
type Downloader struct {
	Dir      string
	Client   *http.Client
	Progress io.Writer // nil for quiet
}

// NewDownloader returns a Downloader writing under dir.
func NewDownloader(dir string) *Downloader {
	return &Downloader{
		Dir: dir,
		// Installer payloads average ~20 MB but the tail runs to gigabytes, so
		// the bound is on idle connections rather than the whole transfer.
		Client: &http.Client{
			Timeout:       60 * time.Minute,
			CheckRedirect: followWithoutReferer,
		},
	}
}

// followWithoutReferer strips the Referer that net/http attaches to each
// redirect hop. Vendors read it as "a human clicked a link on our site" and
// answer with a landing page instead of the file: SourceForge serves its "your
// download will start shortly" interstitial to a referred request but a 302 to
// a mirror to an unreferred one. A manifest URL that redirects before reaching
// /download would otherwise fetch that page. curl omits the header by default,
// which is why such a URL downloads correctly by hand.
func followWithoutReferer(req *http.Request, via []*http.Request) error {
	req.Header.Del("Referer")
	// net/http applies this bound itself only while using its default policy.
	if len(via) >= 10 {
		return fmt.Errorf("stopped after 10 redirects")
	}
	return nil
}

// Get returns a local path to the payload at url, downloading it if needed.
//
// When the manifest declares a digest the file is stored under it, which makes
// the cache content-addressed: the same payload referenced by two packages is
// downloaded once, and a cache hit needs no re-verification because a file
// could only have been written there after its hash matched.
func (d *Downloader) Get(ctx context.Context, url, want string) (string, error) {
	if url == "" {
		return "", fmt.Errorf("installer has no download URL")
	}
	if err := os.MkdirAll(d.Dir, 0o755); err != nil {
		return "", err
	}

	want = strings.ToLower(strings.TrimSpace(want))
	dest := filepath.Join(d.Dir, cacheName(url, want))
	if _, err := os.Stat(dest); err == nil {
		return dest, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := d.Client.Do(req)
	if err != nil {
		return "", fmt.Errorf("download %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download %s: %s", url, resp.Status)
	}
	// Without a digest to check the payload against, the media type is the only
	// remaining signal that a vendor served a landing page in place of the file,
	// and no installer is ever HTML. When the manifest does declare a digest the
	// mismatch below reports the same failure with more to go on, so leave that
	// case to the hash rather than second-guessing a server's Content-Type.
	if ct := resp.Header.Get("Content-Type"); want == "" && isHTML(ct) {
		return "", fmt.Errorf("download %s: server returned %s, not an installer payload", url, ct)
	}

	// Write beside the target and rename, so an interrupted download can never
	// be mistaken for a verified payload on the next run.
	tmp, err := os.CreateTemp(d.Dir, ".partial-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())

	sum := sha256.New()
	var body io.Reader = io.TeeReader(resp.Body, sum)
	if d.Progress != nil {
		p := &progress{w: d.Progress, total: resp.ContentLength, label: path.Base(strings.SplitN(url, "?", 2)[0])}
		body = io.TeeReader(body, p)
		defer p.done()
	}
	n, err := io.Copy(tmp, body)
	if err != nil {
		tmp.Close()
		return "", fmt.Errorf("download %s: %w", url, err)
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}

	got := hex.EncodeToString(sum.Sum(nil))
	if want != "" && got != want {
		return "", fmt.Errorf("sha256 mismatch for %s\n  manifest: %s\n  download: %s\n  received: %s of %s",
			url, want, got, humanBytes(n), describeType(resp.Header.Get("Content-Type")))
	}
	if err := os.Rename(tmp.Name(), dest); err != nil {
		return "", err
	}
	return dest, nil
}

// cacheName keys a payload by its declared digest when there is one, and by
// its URL otherwise. The extension is preserved either way, because both 7z
// and wine dispatch on it.
func cacheName(url, sha string) string {
	clean := strings.SplitN(url, "?", 2)[0]
	ext := strings.ToLower(filepath.Ext(clean))
	switch ext {
	case ".exe", ".msi", ".zip", ".msu", ".appx", ".msix", ".7z", ".tar", ".gz", ".bin":
	default:
		ext = ".bin"
	}
	key := sha
	if key == "" {
		digest := sha256.Sum256([]byte(url))
		key = "url-" + hex.EncodeToString(digest[:])[:32]
	}
	return key + ext
}

// isHTML reports whether a response carries a web page rather than a payload.
func isHTML(contentType string) bool {
	mt, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return false
	}
	return mt == "text/html" || mt == "application/xhtml+xml"
}

// describeType names a response's media type for an error message.
func describeType(contentType string) string {
	mt, _, err := mime.ParseMediaType(contentType)
	if err != nil || mt == "" {
		return "unknown type"
	}
	return mt
}

// progress renders a single rewriting line of download status.
type progress struct {
	w       io.Writer
	total   int64
	seen    int64
	label   string
	lastLen int
	last    time.Time
}

func (p *progress) Write(b []byte) (int, error) {
	p.seen += int64(len(b))
	if time.Since(p.last) < 100*time.Millisecond {
		return len(b), nil
	}
	p.last = time.Now()
	p.render()
	return len(b), nil
}

func (p *progress) render() {
	line := fmt.Sprintf("  %s  %s", p.label, humanBytes(p.seen))
	if p.total > 0 {
		line = fmt.Sprintf("  %s  %s / %s  %d%%",
			p.label, humanBytes(p.seen), humanBytes(p.total), p.seen*100/p.total)
	}
	pad := ""
	if n := p.lastLen - len(line); n > 0 {
		pad = strings.Repeat(" ", n)
	}
	p.lastLen = len(line)
	fmt.Fprintf(p.w, "\r%s%s", line, pad)
}

func (p *progress) done() {
	p.render()
	fmt.Fprintln(p.w)
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGT"[exp])
}

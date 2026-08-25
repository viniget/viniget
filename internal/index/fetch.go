package index

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

const (
	// CDN hosts the source index. It does NOT host manifests -- every manifest
	// path shape there returns BlobNotFound; those come from RAW instead.
	CDN = "https://cdn.winget.microsoft.com/cache"
	// SourceV2 is the v2 schema index: ~3.5 MB, 14,586 packages. All search and
	// manifest resolution runs off this one file.
	SourceV2 = CDN + "/source2.msix"
	// SourceV1 is the v1 schema index: ~19.5 MB, 40 MB extracted. Downloaded
	// only when a derived manifest URL 404s. See PathIndex.
	SourceV1 = CDN + "/source.msix"

	// dbMember is the SQLite database inside the msix, which is a plain zip.
	dbMember = "Public/index.db"

	// DefaultTTL is how stale a local index may get before commands try a
	// conditional refresh. The CDN sets cache-control max-age=900 and rebuilds
	// roughly hourly, but a 304 is cheap enough that daily is plenty.
	DefaultTTL = 24 * time.Hour
)

// Meta records what we know about the cached index.
type Meta struct {
	ETag          string    `json:"etag,omitempty"`
	LastModified  string    `json:"last_modified,omitempty"`
	FetchedAt     time.Time `json:"fetched_at"`
	SourceVersion string    `json:"source_version,omitempty"`
	PackageCount  int       `json:"package_count,omitempty"`
}

// Store is the on-disk index cache.
type Store struct {
	Dir    string
	Client *http.Client
}

// DefaultDir is $XDG_CACHE_HOME/viniget, falling back to ~/.cache/viniget.
func DefaultDir() string {
	if d := os.Getenv("XDG_CACHE_HOME"); d != "" {
		return filepath.Join(d, "viniget")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "viniget")
	}
	return filepath.Join(home, ".cache", "viniget")
}

// NewStore returns a Store rooted at dir, or the default cache dir when empty.
func NewStore(dir string) *Store {
	if dir == "" {
		dir = DefaultDir()
	}
	return &Store{Dir: dir, Client: &http.Client{Timeout: 5 * time.Minute}}
}

func (s *Store) DBPath() string   { return filepath.Join(s.Dir, "index-v2.db") }
func (s *Store) metaPath() string { return filepath.Join(s.Dir, "index-v2.meta.json") }
func (s *Store) v1Path() string   { return filepath.Join(s.Dir, "index-v1.db") }

// ReadMeta returns the cached metadata, or a zero Meta when there is none.
func (s *Store) ReadMeta() Meta {
	var m Meta
	b, err := os.ReadFile(s.metaPath())
	if err != nil {
		return m
	}
	_ = json.Unmarshal(b, &m)
	return m
}

func (s *Store) writeMeta(m Meta) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.metaPath(), append(b, '\n'), 0o644)
}

// Ensure makes a usable index available and returns its metadata.
//
// It downloads when nothing is cached, and otherwise issues a conditional GET
// once the cache is older than ttl. noRefresh suppresses the refresh but never
// the initial download -- there is nothing to search without one.
func (s *Store) Ensure(ctx context.Context, ttl time.Duration, noRefresh bool) (Meta, bool, error) {
	meta := s.ReadMeta()
	if _, err := os.Stat(s.DBPath()); err != nil {
		return s.Refresh(ctx)
	}
	if noRefresh || time.Since(meta.FetchedAt) < ttl {
		return meta, false, nil
	}
	m, changed, err := s.Refresh(ctx)
	if err != nil {
		// A refresh failure is not fatal when we already hold a usable index.
		return meta, false, nil
	}
	return m, changed, nil
}

// Refresh performs a conditional GET of the v2 index. It reports whether the
// local copy actually changed; an unchanged index costs one 304 and no body.
func (s *Store) Refresh(ctx context.Context) (Meta, bool, error) {
	prev := s.ReadMeta()
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return prev, false, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, SourceV2, nil)
	if err != nil {
		return prev, false, err
	}
	_, haveDB := os.Stat(s.DBPath())
	if haveDB == nil {
		if prev.ETag != "" {
			req.Header.Set("If-None-Match", prev.ETag)
		} else if prev.LastModified != "" {
			req.Header.Set("If-Modified-Since", prev.LastModified)
		}
	}

	resp, err := s.Client.Do(req)
	if err != nil {
		return prev, false, fmt.Errorf("fetch index: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotModified {
		prev.FetchedAt = time.Now()
		if err := s.writeMeta(prev); err != nil {
			return prev, false, err
		}
		return prev, false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return prev, false, fmt.Errorf("fetch index: %s", resp.Status)
	}

	blob, err := io.ReadAll(resp.Body)
	if err != nil {
		return prev, false, fmt.Errorf("read index: %w", err)
	}
	if err := extractTo(blob, s.DBPath()); err != nil {
		return prev, false, err
	}

	m := Meta{
		ETag:          resp.Header.Get("ETag"),
		LastModified:  resp.Header.Get("Last-Modified"),
		FetchedAt:     time.Now(),
		SourceVersion: resp.Header.Get("x-ms-meta-sourceversion"),
	}
	if cat, err := Open(s.DBPath()); err == nil {
		if n, err := cat.Count(); err == nil {
			m.PackageCount = n
		}
		cat.Close()
	}
	if err := s.writeMeta(m); err != nil {
		return m, true, err
	}
	return m, true, nil
}

// extractTo pulls Public/index.db out of an msix and installs it at dest.
// The write goes to a temp file and is renamed into place, so an interrupted
// download can never leave a truncated database behind.
func extractTo(msix []byte, dest string) error {
	zr, err := zip.NewReader(bytes.NewReader(msix), int64(len(msix)))
	if err != nil {
		return fmt.Errorf("index is not a valid msix: %w", err)
	}
	var member *zip.File
	for _, f := range zr.File {
		if f.Name == dbMember {
			member = f
			break
		}
	}
	if member == nil {
		return fmt.Errorf("index msix has no %s", dbMember)
	}
	rc, err := member.Open()
	if err != nil {
		return err
	}
	defer rc.Close()

	tmp := dest + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, rc); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dest)
}

// EnsureV1 downloads and extracts the v1 index, which is needed only as the
// fallback path resolver. It is ~19.5 MB on the wire and 40 MB extracted, so
// it is fetched lazily and kept once obtained.
func (s *Store) EnsureV1(ctx context.Context) (string, error) {
	if _, err := os.Stat(s.v1Path()); err == nil {
		return s.v1Path(), nil
	}
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, SourceV1, nil)
	if err != nil {
		return "", err
	}
	resp, err := s.Client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch v1 index: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetch v1 index: %s", resp.Status)
	}
	blob, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if err := extractTo(blob, s.v1Path()); err != nil {
		return "", err
	}
	return s.v1Path(), nil
}

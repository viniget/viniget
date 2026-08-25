package install

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/viniget/viniget/internal/manifest"
	"github.com/viniget/viniget/internal/wine"
)

// DefaultTimeout bounds a single installer run.
//
// Four minutes is not generous, it is measured: WinMerge's Inno installer is
// still going at 240s with the job already done. The timeout exists to stop a
// wedged interactive dialog from hanging forever, and expiry is explicitly
// not treated as failure -- see Status.
const DefaultTimeout = 240 * time.Second

// Status is the install verdict, in the same vocabulary the compat harness
// records, so a local install and a CI verdict can be compared directly.
type Status string

const (
	// StatusOK is an install that wrote executables and returned cleanly.
	StatusOK Status = "ok"
	// StatusTimeoutInstalled is an install that ran past the timeout having
	// already written executables. This is a pass.
	StatusTimeoutInstalled Status = "timeout-but-installed"
	// StatusFilesNoExe is data on disk with nothing to run -- a resource pack,
	// or an installer that unpacked and then failed.
	StatusFilesNoExe Status = "files-but-no-exe"
	// StatusNoFiles is an installer that ran and changed nothing.
	StatusNoFiles Status = "no-files"
	// StatusDownloadFailed never reached Wine at all.
	StatusDownloadFailed Status = "download-failed"
	// StatusUnsupported is a package viniget declines to drive.
	StatusUnsupported Status = "unsupported"
	// StatusError is viniget failing rather than the package: a corrupt
	// archive, a nested installer the manifest promised and did not ship, a
	// prefix that could not be written. It is kept distinct from the verdicts
	// above because it says nothing about whether the package works on Wine.
	StatusError Status = "install-error"
)

// Installed reports whether a status means the application is on disk.
func (s Status) Installed() bool {
	return s == StatusOK || s == StatusTimeoutInstalled
}

// Result is everything one install attempt produced.
type Result struct {
	ID             string    `json:"id"`
	Version        string    `json:"version"`
	Name           string    `json:"name,omitempty"`
	InstallerType  string    `json:"installer_type"`
	Container      string    `json:"container,omitempty"`
	Status         Status    `json:"status"`
	Prefix         string    `json:"prefix,omitempty"`
	Scope          string    `json:"scope,omitempty"`
	Architecture   string    `json:"architecture,omitempty"`
	ProductCode    string    `json:"product_code,omitempty"`
	Seconds        float64   `json:"seconds"`
	ExitCode       string    `json:"exit_code,omitempty"`
	NewFiles       int       `json:"new_files"`
	NewExecutables int       `json:"new_executables"`
	Entry          string    `json:"entry,omitempty"`
	WineVersion    string    `json:"wine_version,omitempty"`
	InstalledAt    time.Time `json:"installed_at,omitempty"`
	Notes          []string  `json:"notes,omitempty"`
}

func (r *Result) note(format string, a ...any) {
	r.Notes = append(r.Notes, fmt.Sprintf(format, a...))
}

// Options configures one install.
type Options struct {
	// Prefixes owns the baseline and the per-package prefixes.
	Prefixes *wine.Store
	// Cache is where installer payloads are downloaded to.
	Cache string
	// Timeout bounds the installer run. Zero uses DefaultTimeout.
	Timeout time.Duration
	// Display picks the X strategy. The zero value resolves to Headless.
	Display wine.Display
	// Verbose passes wine's own output through instead of discarding it.
	Verbose bool
	// KeepFailed leaves a failed staging prefix on disk for inspection.
	KeepFailed bool
	// Log receives human-readable progress. Nil is quiet.
	Log io.Writer
	// Progress receives the download status line. It is separate from Log
	// because that line rewrites itself with a carriage return, which reads as
	// one enormous line in anything that is not a terminal.
	Progress io.Writer
}

func (o *Options) timeout() time.Duration {
	if o.Timeout > 0 {
		return o.Timeout
	}
	return DefaultTimeout
}

func (o *Options) logf(format string, a ...any) {
	if o.Log != nil {
		fmt.Fprintf(o.Log, format+"\n", a...)
	}
}

// Run installs a resolved package into its own prefix.
//
// The sequence is ordered so nothing expensive happens before the cheap checks
// have passed: pick the variant first, so a package viniget cannot drive fails
// in milliseconds without touching the network or Wine; then fetch the payload
// and boot the baseline prefix together, since on a first install those two
// are the entire runtime and neither depends on the other; then clone, install
// and score.
func Run(ctx context.Context, pkg *manifest.Package, opt Options) (*Result, error) {
	res := &Result{ID: pkg.ID, Version: pkg.Version, InstalledAt: time.Now()}
	if pkg.Locale != nil {
		res.Name = pkg.Locale.PackageName
	}

	choice, err := Select(pkg.Installer)
	if err != nil {
		res.Status = StatusUnsupported
		if pkg.Installer != nil {
			res.InstallerType = strings.ToLower(pkg.Installer.InstallerType)
		}
		res.note("%v", err)
		return res, err
	}
	res.InstallerType = choice.Type
	res.Container = choice.Container
	res.Scope = choice.Scope
	res.Architecture = choice.Arch
	res.ProductCode = choice.ProductCode

	if err := wine.Available(); err != nil {
		return res, err
	}
	final, err := opt.Prefixes.For(pkg.ID)
	if err != nil {
		return res, err
	}

	// The baseline prefix costs 20-40s to create and the payload takes about
	// as long to fetch. On a first install they are the whole runtime, and
	// they are independent, so they run together.
	if !opt.Prefixes.HasBase() {
		opt.logf("creating baseline wine prefix (one time, ~444 MB) ...")
	}
	booted := make(chan error, 1)
	go func() { booted <- opt.Prefixes.EnsureBase(ctx, opt.Verbose) }()

	dl := NewDownloader(opt.Cache)
	dl.Progress = opt.Progress
	payload, dlErr := dl.Get(ctx, choice.URL, choice.Sha256)

	if err := <-booted; err != nil {
		return res, err
	}
	if dlErr != nil {
		res.Status = StatusDownloadFailed
		res.note("%v", dlErr)
		return res, dlErr
	}
	if choice.Sha256 != "" {
		res.note("sha256 verified")
	} else {
		res.note("manifest declares no sha256; payload unverified")
	}

	staging := final + ".installing"
	opt.logf("cloning prefix ...")
	if err := opt.Prefixes.Clone(staging); err != nil {
		res.Status = StatusError
		return res, err
	}
	res.WineVersion = wine.Version(ctx)

	before, err := snapshot(staging)
	if err != nil {
		res.Status = StatusError
		return res, err
	}

	runner := &wine.Runner{Prefix: staging, Display: opt.Display, Verbose: opt.Verbose}
	if runner.Display.Kind == "" {
		runner.Display = wine.Headless()
	}
	if opt.Verbose {
		runner.Output = opt.Log
	}

	started := time.Now()
	outcome, err := drive(ctx, runner, choice, payload, staging, opt, res)
	if err != nil {
		res.Status = StatusError
		if !opt.KeepFailed {
			os.RemoveAll(staging)
		}
		return res, err
	}
	runner.Settle(ctx)
	res.Seconds = round1(time.Since(started).Seconds())
	res.ExitCode = outcome.Code()

	after, err := snapshot(staging)
	if err != nil {
		res.Status = StatusError
		return res, err
	}
	delta := diff(before, after)
	res.NewFiles = len(delta.Files)
	res.NewExecutables = len(delta.Executables)

	// The filesystem decides, not the return code.
	switch {
	case res.NewExecutables > 0 && outcome.TimedOut:
		res.Status = StatusTimeoutInstalled
	case res.NewExecutables > 0:
		res.Status = StatusOK
	case res.NewFiles > 0:
		res.Status = StatusFilesNoExe
	default:
		res.Status = StatusNoFiles
	}

	if !res.Status.Installed() {
		// Naming what did land is the difference between "it failed" and a
		// starting point: an installer that unpacked its payload and then
		// died leaves a very different trail from one that never ran.
		for _, f := range delta.Files {
			if len(res.Notes) > 6 {
				res.note("... and %d more files", res.NewFiles-4)
				break
			}
			res.note("wrote %s", f)
		}
		if opt.KeepFailed {
			res.Prefix = staging
			res.note("failed prefix kept at %s", staging)
		} else {
			os.RemoveAll(staging)
		}
		return res, nil
	}

	// Only now is the previous prefix touched: a failed install above left it
	// untouched, because nothing was ever written into it.
	if err := wine.Swap(staging, final); err != nil {
		res.Status = StatusError
		return res, err
	}
	res.Prefix = final
	if res.Entry != "" {
		res.Entry = strings.Replace(res.Entry, staging, final, 1)
	}
	return res, nil
}

// drive runs the chosen installer. Everything type-specific lives here.
func drive(ctx context.Context, r *wine.Runner, c *Choice, payload, prefix string, opt Options, res *Result) (wine.Result, error) {
	// A manifest's declared type is a hint about the payload, not a promise:
	// see isZip. Believing the file itself costs one four-byte read.
	container := c.Container
	if container == "" && IsArchive(c.Type) && isZip(payload) {
		container = "zip"
		res.Container = container
		res.note("manifest declares %s but the payload is a zip; extracting it", c.Type)
	}

	// A zip has to be unpacked before anything inside it can be identified,
	// let alone run.
	if container == "zip" {
		unpacked := filepath.Join(prefix, "drive_c", "windows", "temp", "viniget-unpack")
		os.RemoveAll(unpacked)
		if err := os.MkdirAll(unpacked, 0o755); err != nil {
			return wine.Result{}, err
		}
		if err := unzip(payload, unpacked); err != nil {
			return wine.Result{}, err
		}
		if IsArchive(c.Type) {
			return wine.Result{}, installArchive(c, unpacked, prefix, res)
		}
		nested, err := nestedInstaller(c, unpacked)
		if err != nil {
			return wine.Result{}, err
		}
		res.note("nested installer: %s", filepath.Base(nested))
		payload = nested
	} else if IsArchive(c.Type) {
		// A bare portable payload is the application itself.
		return wine.Result{}, installArchive(c, payload, prefix, res)
	}

	installDir := programFiles(prefix, res.ID)
	logPath := filepath.Join(prefix, "drive_c", "windows", "temp", "viniget-install.log")

	if IsMSI(c.Type) {
		// msiexec is a Wine builtin, so the payload is addressed through the
		// prefix's own drive letters rather than as a host path.
		dos, err := wine.DOSPath(prefix, payload)
		if err != nil {
			dos = payload // Z: should always map /, but never fail on this
		}
		args := []string{"msiexec", "/i", dos, "/qn"}
		extra, ok := SilentArgs(c.Type, c.Switches)
		if ok && len(extra) > 0 {
			args = append(args, expandPlaceholders(extra, logPath, installDir)...)
			res.note("switches: %s [manifest]", strings.Join(extra, " "))
		}
		opt.logf("installing (%s, msiexec) ...", c.Type)
		return r.Run(ctx, opt.timeout(), args...)
	}

	args, ok := SilentArgs(c.Type, c.Switches)
	if !ok {
		return wine.Result{}, fmt.Errorf("no safe unattended switches for installer type %q", c.Type)
	}
	args = expandPlaceholders(args, logPath, installDir)
	source := "viniget default"
	if Declared(c.Switches) {
		source = "manifest"
	}
	res.note("switches: %s [%s]", orNone(strings.Join(args, " ")), source)

	opt.logf("installing (%s) ...", c.Type)
	return r.Run(ctx, opt.timeout(), append([]string{"wine", payload}, args...)...)
}

// installArchive places an extracted or portable payload into Program Files.
//
// Archive packages have no installer to register anything, so their entry
// point comes from NestedInstallerFiles in the manifest -- the one case where
// the launch target is not read out of a Start Menu shortcut.
func installArchive(c *Choice, src, prefix string, res *Result) error {
	dest := programFiles(prefix, res.ID)
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}

	res.note("extracted into the prefix; no installer was executed")

	if info, err := os.Stat(src); err == nil && !info.IsDir() {
		// A bare portable: the download is the executable. Manifests name it
		// with a PortableCommandAlias when they care what it is called.
		name := portableName(c, src)
		if err := copyFile(src, filepath.Join(dest, name)); err != nil {
			return err
		}
		res.Entry = filepath.Join(dest, name)
		return nil
	}

	if err := moveTree(src, dest); err != nil {
		return err
	}
	for _, nested := range c.Nested {
		if nested.RelativeFilePath == "" {
			continue
		}
		if found, ok := findNested(dest, nested.RelativeFilePath); ok {
			res.Entry = found
			break
		}
	}
	return nil
}

// nestedInstaller resolves the installer inside an extracted archive.
func nestedInstaller(c *Choice, root string) (string, error) {
	for _, nested := range c.Nested {
		if nested.RelativeFilePath == "" {
			continue
		}
		if found, ok := findNested(root, nested.RelativeFilePath); ok {
			return found, nil
		}
	}
	return "", fmt.Errorf("archive declares a nested %s installer but none of its NestedInstallerFiles exist in the payload", c.Type)
}

// portableName picks the filename a bare portable payload lands under.
//
// The URL's own basename is kept, version and all, because a portable binary
// is entitled to care what it is called -- several check their own filename
// to decide where to keep settings. PortableCommandAlias is deliberately not
// used here: it names the command a user types, which is a shim in the launch
// path rather than a rename on disk.
func portableName(c *Choice, src string) string {
	base := filepath.Base(strings.SplitN(c.URL, "?", 2)[0])
	if base == "" || base == "." || base == "/" {
		return "app" + filepath.Ext(src)
	}
	return base
}

// programFiles is where viniget puts payloads that no installer placed. The
// last identifier segment is used rather than the display name, which is
// localised and full of characters Windows paths handle badly.
func programFiles(prefix, id string) string {
	name := id
	if i := strings.LastIndex(id, "."); i >= 0 && i+1 < len(id) {
		name = id[i+1:]
	}
	return filepath.Join(prefix, "drive_c", "Program Files", name)
}

func moveTree(src, dest string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		from, to := filepath.Join(src, e.Name()), filepath.Join(dest, e.Name())
		os.RemoveAll(to)
		if err := os.Rename(from, to); err != nil {
			return err
		}
	}
	return os.RemoveAll(src)
}

func copyFile(src, dest string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}

func round1(f float64) float64 { return float64(int64(f*10+0.5)) / 10 }

package helper

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/viniget/viniget/internal/wine"
)

var ErrNotInstalled = errors.New("winetricks is not installed")

// Result is the durable, JSON-facing outcome of one helper request.
type Result struct {
	Verb            string   `json:"verb"`
	Source          Source   `json:"source"`
	Status          Status   `json:"status"`
	Dependency      string   `json:"dependency,omitempty"`
	MinimumVersion  string   `json:"minimum_version,omitempty"`
	DetectedVersion string   `json:"detected_version,omitempty"`
	Seconds         float64  `json:"seconds"`
	ExitCode        string   `json:"exit_code,omitempty"`
	Notes           []string `json:"notes,omitempty"`
}

func ResultFor(r Request, status Status) Result {
	return Result{
		Verb: r.Verb, Source: r.Source, Status: status,
		Dependency: r.Dependency, MinimumVersion: r.MinimumVersion,
	}
}

// ListedDefinition is one curated definition plus its availability in the
// host's installed Winetricks script.
type ListedDefinition struct {
	Definition
	Available bool `json:"available"`
}

// Executor is a validated Winetricks installation. Availability is resolved
// once per command instead of spawning `winetricks list-all` for every verb.
type Executor struct {
	Binary    string
	available map[string]bool
}

// NewExecutor locates Winetricks and reads the verbs the installed script
// actually provides. binary is injectable for tests; an empty value uses PATH.
func NewExecutor(ctx context.Context, binary string) (*Executor, error) {
	var err error
	if binary == "" {
		binary, err = exec.LookPath("winetricks")
		if err != nil {
			return nil, fmt.Errorf("%w: install your distribution's winetricks package", ErrNotInstalled)
		}
	}
	cmd := exec.CommandContext(ctx, binary, "list-all")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("list winetricks verbs: %w", err)
	}
	available := map[string]bool{}
	s := bufio.NewScanner(strings.NewReader(string(out)))
	for s.Scan() {
		verb, _, _ := strings.Cut(strings.TrimSpace(s.Text()), " ")
		if verb != "" {
			available[strings.ToLower(verb)] = true
		}
	}
	return &Executor{Binary: binary, available: available}, nil
}

func (e *Executor) Available(verb string) bool {
	return e != nil && e.available[strings.ToLower(verb)]
}

func (e *Executor) List() []ListedDefinition {
	defs := Definitions()
	out := make([]ListedDefinition, 0, len(defs))
	for _, d := range defs {
		out = append(out, ListedDefinition{Definition: d, Available: e.Available(d.Verb)})
	}
	return out
}

// Options controls one helper invocation.
type Options struct {
	Prefix  string
	Display wine.Display
	Timeout time.Duration
	Force   bool
	Verbose bool
	Output  io.Writer
}

// Apply invokes one Winetricks verb and, for inferred modern runtimes, proves
// that the requested runtime family and minimum version are present.
func (e *Executor) Apply(ctx context.Context, req Request, opt Options) (Result, error) {
	res := ResultFor(req, StatusFailed)
	if _, err := Lookup(req.Verb); err != nil {
		res.Notes = append(res.Notes, err.Error())
		return res, err
	}
	if e == nil {
		res.Status = StatusUnavailable
		err := fmt.Errorf("%w: install your distribution's winetricks package", ErrNotInstalled)
		res.Notes = append(res.Notes, err.Error())
		return res, err
	}
	if !e.Available(req.Verb) {
		res.Status = StatusUnavailable
		err := fmt.Errorf("helper %s is not provided by %s; update winetricks", req.Verb, e.Binary)
		res.Notes = append(res.Notes, err.Error())
		return res, err
	}
	if opt.Timeout <= 0 {
		opt.Timeout = DefaultTimeout
	}
	if opt.Display.Kind == "" {
		opt.Display = wine.Headless()
	}
	runner := &wine.Runner{
		Prefix: opt.Prefix, Display: opt.Display,
		Verbose: opt.Verbose, Output: opt.Output,
	}
	args := []string{e.Binary, "--unattended"}
	if opt.Force {
		args = append(args, "--force")
	}
	args = append(args, req.Verb)

	started := time.Now()
	finish := func() { res.Seconds = roundedSeconds(time.Since(started)) }
	outcome, err := runner.Run(ctx, opt.Timeout, args...)
	if err != nil {
		runner.Kill(ctx)
		res.Notes = append(res.Notes, err.Error())
		finish()
		return res, err
	}
	res.ExitCode = outcome.Code()
	if outcome.TimedOut {
		runner.Kill(ctx)
		err := fmt.Errorf("helper %s timed out after %s", req.Verb, opt.Timeout)
		res.Notes = append(res.Notes, err.Error())
		finish()
		return res, err
	}
	runner.Settle(ctx)
	if outcome.ExitCode != 0 {
		runner.Kill(ctx)
		err := fmt.Errorf("helper %s failed (exit %d)", req.Verb, outcome.ExitCode)
		res.Notes = append(res.Notes, err.Error())
		finish()
		return res, err
	}

	if req.RuntimeFamily != "" {
		version, err := verifyRuntime(ctx, runner, req)
		res.DetectedVersion = version
		if err != nil {
			res.Notes = append(res.Notes, err.Error())
			runner.Kill(ctx)
			finish()
			return res, err
		}
	}
	res.Status = StatusInstalled
	finish()
	return res, nil
}

// verifyRuntime writes command output to a real file. A pipe is unsafe here:
// wineserver inherits its write end and may keep the Go wait open forever.
func verifyRuntime(ctx context.Context, runner *wine.Runner, req Request) (string, error) {
	tempDir := filepath.Join(runner.Prefix, "drive_c", "windows", "temp")
	if err := os.MkdirAll(tempDir, 0o755); err != nil {
		return "", err
	}
	best := ""
	minimumValid := true
	if req.MinimumVersion != "" {
		_, minimumValid = numericVersion(req.MinimumVersion)
	}
	for _, exe := range dotnetExecutables(runner.Prefix, req.Architecture) {
		if _, err := os.Stat(exe); err != nil {
			continue
		}
		f, err := os.CreateTemp(tempDir, "viniget-dotnet-runtimes-*.txt")
		if err != nil {
			return "", err
		}
		name := f.Name()
		probe := *runner
		probe.Output = f
		outcome, runErr := probe.Run(ctx, 2*time.Minute, "wine", exe, "--list-runtimes")
		probe.Settle(ctx)
		closeErr := f.Close()
		body, readErr := os.ReadFile(name)
		_ = os.Remove(name)
		if runErr != nil || outcome.TimedOut || outcome.ExitCode != 0 || closeErr != nil || readErr != nil {
			continue
		}
		if version := matchingRuntime(string(body), req.RuntimeFamily, req.RuntimeMajor, ""); version != "" {
			if best == "" || compareVersions(version, best) > 0 {
				best = version
			}
			if minimumValid && (req.MinimumVersion == "" || compareVersions(version, req.MinimumVersion) >= 0) {
				return version, nil
			}
		}
	}
	want := fmt.Sprintf("%s %d", req.RuntimeFamily, req.RuntimeMajor)
	if req.MinimumVersion != "" {
		want += " >= " + req.MinimumVersion
	}
	return best, fmt.Errorf("helper %s did not provide required runtime %s", req.Verb, want)
}

func dotnetExecutables(prefix, arch string) []string {
	x64 := filepath.Join(prefix, "drive_c", "Program Files", "dotnet", "dotnet.exe")
	x86 := filepath.Join(prefix, "drive_c", "Program Files (x86)", "dotnet", "dotnet.exe")
	switch strings.ToLower(arch) {
	case "x86":
		return []string{x86}
	case "x64":
		return []string{x64}
	default:
		return []string{x64, x86}
	}
}

func matchingRuntime(output, family string, major int, minimum string) string {
	if minimum != "" {
		if _, ok := numericVersion(minimum); !ok {
			return ""
		}
	}
	best := ""
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != family {
			continue
		}
		parts, ok := numericVersion(fields[1])
		if !ok || len(parts) == 0 || parts[0] != major {
			continue
		}
		if minimum != "" && compareVersions(fields[1], minimum) < 0 {
			continue
		}
		if best == "" || compareVersions(fields[1], best) > 0 {
			best = fields[1]
		}
	}
	return best
}

func roundedSeconds(d time.Duration) float64 {
	f, _ := strconv.ParseFloat(fmt.Sprintf("%.1f", d.Seconds()), 64)
	return f
}

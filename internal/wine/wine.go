// Package wine drives a system Wine installation.
//
// Scope is deliberately narrow: system Wine, nothing else. No Proton, no
// umu-launcher, no DXVK. One runner is what makes a compatibility verdict
// mean something on a machine other than the one that produced it, so this
// package only has to locate the binaries, own the environment, run a
// command, and wait for the server to settle.
package wine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// ErrNotInstalled is returned when the host has no wine binary on PATH.
var ErrNotInstalled = errors.New("wine is not installed")

// BootTimeout bounds baseline prefix creation. A cold wineboot -u writes
// ~444 MB and takes 20-40s; anything past a couple of minutes is a hang.
const BootTimeout = 5 * time.Minute

// ServerWait bounds the post-install `wineserver -w`. Installers routinely
// return before their own background processes have finished writing, so this
// wait is part of the install, not an afterthought.
const ServerWait = 60 * time.Second

// Available reports whether a usable wine is on PATH.
func Available() error {
	if _, err := exec.LookPath("wine"); err != nil {
		return fmt.Errorf("%w: install your distribution's wine package", ErrNotInstalled)
	}
	return nil
}

// Version returns the running wine's version string, e.g. "wine-11.16".
// Compatibility verdicts are only comparable within one runner version, so
// this gets recorded alongside every install.
func Version(ctx context.Context) string {
	out, err := exec.CommandContext(ctx, "wine", "--version").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// Result is the outcome of one wine command.
//
// TimedOut is tracked separately from ExitCode because a timeout is not a
// failure: WinMerge's Inno installer runs past four minutes having already
// written every file correctly. Callers score on the filesystem and treat
// this whole struct as a hint.
type Result struct {
	ExitCode int
	TimedOut bool
	Duration time.Duration
}

// Code renders the outcome the way the compat harness records it.
func (r Result) Code() string {
	if r.TimedOut {
		return "TIMEOUT"
	}
	return fmt.Sprint(r.ExitCode)
}

// Runner executes commands against one prefix.
type Runner struct {
	// Prefix is the WINEPREFIX every command runs against.
	Prefix string
	// Display supplies an X server. The zero value inherits whatever the
	// caller resolved; use Headless to pick one.
	Display Display
	// Verbose leaves WINEDEBUG alone instead of silencing it.
	Verbose bool
	// NoRuntimePrompts stubs out Mono and Gecko for the duration of the
	// command. Their "do you want to install this?" dialogs are modal, and a
	// modal dialog on a headless display is just a timeout with extra steps.
	// It is set for wineboot and left off for installs, which may legitimately
	// need .NET present.
	NoRuntimePrompts bool
	// Output receives merged stdout/stderr. Nil discards it.
	Output io.Writer
}

// Env returns the environment a wine command runs under.
func (r *Runner) Env() []string {
	env := append(os.Environ(), "WINEPREFIX="+r.Prefix)
	if !r.Verbose {
		env = append(env, "WINEDEBUG=-all")
	}
	if r.NoRuntimePrompts {
		env = append(env, "WINEDLLOVERRIDES=mscoree,mshtml=")
	}
	if r.Display.Value != "" {
		env = append(env, "DISPLAY="+r.Display.Value)
	}
	return env
}

// Run executes argv inside the prefix, wrapped in whatever display strategy
// the Runner carries. A timeout is reported in Result, not as an error;
// errors are reserved for "the command could not be started at all".
func (r *Runner) Run(ctx context.Context, timeout time.Duration, argv ...string) (Result, error) {
	if len(argv) == 0 {
		return Result{}, errors.New("wine: empty command")
	}
	full := append(append([]string{}, r.Display.Wrapper...), argv...)

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, full[0], full[1:]...)
	cmd.Env = r.Env()

	// Stdout and Stderr are left nil when there is nowhere to send output,
	// which hands the child /dev/null directly. This matters far more than it
	// looks: any io.Writer here makes os/exec create a pipe, wineserver
	// inherits the write end, and it outlives the command by design -- so Wait
	// blocks on a pipe nobody will ever close. Every install hangs after the
	// installer has already finished.
	if r.Output != nil {
		cmd.Stdout, cmd.Stderr = r.Output, r.Output
	}
	// SIGTERM rather than the default SIGKILL, so xvfb-run's own trap gets to
	// tear down the X server it started instead of leaking one per timeout.
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	// And then stop waiting on inherited descriptors regardless. The
	// installer's children are deliberately left running: an Inno setup that
	// blew its timeout is usually still writing files, and Settle is what
	// waits for those.
	cmd.WaitDelay = 5 * time.Second

	started := time.Now()
	err := cmd.Run()
	res := Result{Duration: time.Since(started)}

	if ctx.Err() == context.DeadlineExceeded {
		res.TimedOut = true
		res.ExitCode = -1
		return res, nil
	}
	var exit *exec.ExitError
	switch {
	case err == nil:
		res.ExitCode = 0
	case errors.As(err, &exit):
		res.ExitCode = exit.ExitCode()
	default:
		return res, fmt.Errorf("run %s: %w", full[0], err)
	}
	return res, nil
}

// Settle blocks until every process in the prefix has exited.
//
// This is where the tail of an install actually happens. Nullsoft and Inno
// both hand off to helper processes that outlive the installer's own exit,
// and scoring the filesystem before they finish undercounts the delta.
func (r *Runner) Settle(ctx context.Context) {
	_, _ = r.bare().Run(ctx, ServerWait, "wineserver", "-w")
}

// Kill tears the prefix's server down, abandoning anything still running.
func (r *Runner) Kill(ctx context.Context) {
	_, _ = r.bare().Run(ctx, 30*time.Second, "wineserver", "-k")
}

// bare drops the display wrapper. wineserver never draws anything, and
// wrapping it in xvfb-run would stand up a whole X server for the duration of
// a wait -- on a sweep, once per package.
func (r *Runner) bare() *Runner {
	c := *r
	c.Display = Display{Value: r.Display.Value, Kind: r.Display.Kind}
	return &c
}

// Boot creates or repairs a prefix at r.Prefix.
//
// This is the expensive one -- a cold prefix is ~444 MB -- and it is why
// viniget keeps a single baseline and reflinks every install off it.
func (r *Runner) Boot(ctx context.Context) error {
	if err := Available(); err != nil {
		return err
	}
	if err := os.MkdirAll(r.Prefix, 0o755); err != nil {
		return err
	}
	boot := *r
	boot.NoRuntimePrompts = true
	res, err := boot.Run(ctx, BootTimeout, "wineboot", "-u")
	if err != nil {
		return err
	}
	if res.TimedOut {
		return fmt.Errorf("wineboot timed out after %s creating %s", BootTimeout, r.Prefix)
	}
	boot.Settle(ctx)
	if res.ExitCode != 0 {
		return fmt.Errorf("wineboot failed (exit %d) creating %s", res.ExitCode, r.Prefix)
	}
	return nil
}

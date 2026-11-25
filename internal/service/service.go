// Package service manages installing, uninstalling, and inspecting
// Glitched's user background service (launchd on macOS, systemd user unit
// on Linux) so running threads and agent processes survive closing the GUI.
package service

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Label is the launchd job label and base identifier. scripts/glitched
// installs the same job.
const Label = "com.example.glitched.daemon"

// ErrUnsupported is returned when background service installation is
// requested on an unsupported operating system.
var ErrUnsupported = errors.New("background service installation is supported only on macOS and Linux")

// Options configure the installed background engine service.
type Options struct {
	Binary string // path to glitched executable; defaults via ResolveBinary
	Dir    string // engine state directory; defaults to ~/.glitched
	PATH   string // captured login PATH; defaults via CapturePATH
	Home   string // override for $HOME (mostly tests)
}

// Status describes the installed state and live health of the background
// service. Binary, Dir and LogPath are what the installed definition runs.
type Status struct {
	Supported bool   `json:"supported"`
	Installed bool   `json:"installed"`
	Running   bool   `json:"running"`
	PID       int    `json:"pid,omitempty"`
	UnitPath  string `json:"unitPath,omitempty"`
	LogPath   string `json:"logPath,omitempty"`
	Binary    string `json:"binary,omitempty"`
	Dir       string `json:"dir,omitempty"`
	Detail    string `json:"detail,omitempty"`
}

// manager is the OS's service manager, which runs the definition Install
// writes.
type manager interface {
	// path is where the definition lives for the user whose home is home.
	path(home string) string
	render(Options) ([]byte, error)
	// parse reads what a definition runs.
	parse(data []byte) (binary, dir, log string)
	// load has the manager run the definition at path, starting it if it
	// stopped. restart: the definition changed, so a running job restarts
	// to apply it.
	load(ctx context.Context, path string, restart bool) error
	// unload stops the job and has the manager drop it. The definition
	// stays on disk.
	unload(ctx context.Context) error
	state(ctx context.Context) (running bool, pid int)
}

// osManager is this OS's manager, nil where none is supported.
var osManager = func() manager {
	switch runtime.GOOS {
	case "darwin":
		return launchd{}
	case "linux":
		return systemd{}
	}
	return nil
}()

// Install writes the service's definition and has the manager run it. A
// definition that is installed already and unchanged leaves its engine
// running; a changed one restarts it. When the manager refuses, what was
// installed before is put back.
func Install(ctx context.Context, opts Options) (Status, error) {
	m := osManager
	if m == nil {
		return Inspect(ctx, opts), ErrUnsupported
	}
	opts, err := resolveOptions(opts)
	if err != nil {
		return Status{}, err
	}
	if err := os.MkdirAll(opts.Dir, 0o700); err != nil {
		return Status{}, err
	}
	data, err := m.render(opts)
	if err != nil {
		return Status{}, err
	}
	path := m.path(opts.Home)
	old, err := os.ReadFile(path)
	had := err == nil
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return Status{}, err
	}
	if had && bytes.Equal(old, data) {
		if err := m.load(ctx, path, false); err != nil {
			return Inspect(ctx, opts), fmt.Errorf("service: install: %w", err)
		}
		return Inspect(ctx, opts), nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return Status{}, err
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return Status{}, err
	}
	if err := m.load(ctx, path, had); err != nil {
		var undo error
		if had {
			undo = os.WriteFile(path, old, 0o644)
			if undo == nil {
				undo = m.load(ctx, path, true)
			}
		} else {
			_ = m.unload(ctx) // it may never have loaded
			undo = os.Remove(path)
		}
		if undo != nil {
			err = fmt.Errorf("%w; putting back what was installed: %w", err, undo)
		}
		return Inspect(ctx, opts), fmt.Errorf("service: install: %w", err)
	}
	return Inspect(ctx, opts), nil
}

// Uninstall stops the service and removes its definition. When the
// manager refuses to stop it, the definition stays.
func Uninstall(ctx context.Context, opts Options) (Status, error) {
	m := osManager
	if m == nil {
		return Inspect(ctx, opts), ErrUnsupported
	}
	home, err := homeOf(opts)
	if err != nil {
		return Status{}, err
	}
	opts.Home = home
	path := m.path(home)
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return Inspect(ctx, opts), nil
	}
	if err := m.unload(ctx); err != nil {
		return Inspect(ctx, opts), fmt.Errorf("service: uninstall: %w", err)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return Inspect(ctx, opts), err
	}
	return Inspect(ctx, opts), nil
}

// Inspect reads the installed definition and asks the manager whether it
// runs.
func Inspect(ctx context.Context, opts Options) Status {
	m := osManager
	if m == nil {
		return Status{Detail: ErrUnsupported.Error()}
	}
	st := Status{Supported: true}
	home, err := homeOf(opts)
	if err != nil {
		st.Detail = err.Error()
		return st
	}
	st.UnitPath = m.path(home)
	data, err := os.ReadFile(st.UnitPath)
	if err != nil {
		return st
	}
	st.Installed = true
	st.Binary, st.Dir, st.LogPath = m.parse(data)
	st.Running, st.PID = m.state(ctx)
	return st
}

func homeOf(opts Options) (string, error) {
	if opts.Home != "" {
		return opts.Home, nil
	}
	return os.UserHomeDir()
}

// ResolveBinary returns explicit if non-empty, otherwise glitched from PATH
// or the currently executing binary.
func ResolveBinary(explicit string) (string, error) {
	if explicit != "" {
		return filepath.Abs(explicit)
	}
	if p, err := exec.LookPath("glitched"); err == nil {
		return filepath.Abs(p)
	}
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("service: locate binary: %w", err)
	}
	return filepath.Abs(exe)
}

// pathMark comes before the PATH the login shell prints, so what its
// profile prints first stays out.
const pathMark = "\x00glitched-path\x00"

// CapturePATH captures the user's login-shell PATH so backgrounded agents
// find user-installed CLIs (claude, codex, agy, gemini, opencode, git, node).
func CapturePATH() string {
	fallback := os.Getenv("PATH")
	if runtime.GOOS == "windows" {
		return fallback
	}
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/zsh"
		if _, err := os.Stat(shell); err != nil {
			shell = "/bin/sh"
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, shell, "-lc", `printf '\000glitched-path\000%s' "$PATH"`).Output()
	if err != nil {
		return fallback
	}
	_, p, ok := strings.Cut(string(out), pathMark)
	if p = strings.TrimSpace(p); ok && p != "" {
		return p
	}
	return fallback
}

func resolveOptions(opts Options) (Options, error) {
	home, err := homeOf(opts)
	if err != nil {
		return opts, err
	}
	opts.Home = home
	if opts.Dir == "" {
		opts.Dir = filepath.Join(home, ".glitched")
	}
	if abs, err := filepath.Abs(opts.Dir); err == nil {
		opts.Dir = abs
	}
	bin, err := ResolveBinary(opts.Binary)
	if err != nil {
		return opts, err
	}
	opts.Binary = bin
	if opts.PATH == "" {
		opts.PATH = CapturePATH()
	}
	return opts, nil
}

// command runs a manager's command, its output in the error when it fails.
func command(ctx context.Context, name string, args ...string) ([]byte, error) {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return out, fmt.Errorf("%s %s: %s", name, strings.Join(args, " "), msg)
	}
	return out, nil
}

func xmlText(s string) string {
	var buf bytes.Buffer
	_ = xml.EscapeText(&buf, []byte(s))
	return buf.String()
}

// RenderPlist returns the launchd user agent plist for opts.
func RenderPlist(opts Options) ([]byte, error) {
	if opts.Binary == "" {
		return nil, errors.New("service: binary path is required")
	}
	if opts.Dir == "" {
		return nil, errors.New("service: state directory is required")
	}
	logPath := filepath.Join(opts.Dir, "service.log")
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	b.WriteString(`<plist version="1.0">` + "\n")
	b.WriteString(`<dict>` + "\n")
	b.WriteString("\t<key>Label</key>\n\t<string>" + xmlText(Label) + "</string>\n")
	b.WriteString("\t<key>ProgramArguments</key>\n\t<array>\n")
	for _, arg := range serveArgs(opts) {
		b.WriteString("\t\t<string>" + xmlText(arg) + "</string>\n")
	}
	b.WriteString("\t</array>\n")
	b.WriteString("\t<key>EnvironmentVariables</key>\n\t<dict>\n")
	if opts.PATH != "" {
		b.WriteString("\t\t<key>PATH</key>\n\t\t<string>" + xmlText(opts.PATH) + "</string>\n")
	}
	if opts.Home != "" {
		b.WriteString("\t\t<key>HOME</key>\n\t\t<string>" + xmlText(opts.Home) + "</string>\n")
	}
	b.WriteString("\t</dict>\n")
	b.WriteString("\t<key>RunAtLoad</key>\n\t<true/>\n")
	b.WriteString("\t<key>KeepAlive</key>\n\t<dict>\n\t\t<key>SuccessfulExit</key>\n\t\t<false/>\n\t</dict>\n")
	b.WriteString("\t<key>ThrottleInterval</key>\n\t<integer>5</integer>\n")
	b.WriteString("\t<key>StandardOutPath</key>\n\t<string>" + xmlText(logPath) + "</string>\n")
	b.WriteString("\t<key>StandardErrorPath</key>\n\t<string>" + xmlText(logPath) + "</string>\n")
	b.WriteString("</dict>\n</plist>\n")
	return []byte(b.String()), nil
}

// serveArgs is what the service runs. -wait-lock has it wait while the
// app's own engine holds the state directory, which the app hands over to
// it, instead of exiting.
func serveArgs(opts Options) []string {
	return []string{opts.Binary, "serve", "-dir", opts.Dir, "-wait-lock"}
}

// unitQuote quotes s for a systemd unit line, where % starts a specifier
// and, in ExecStart, $ a variable.
func unitQuote(s string, exec bool) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, `%`, `%%`)
	if exec {
		s = strings.ReplaceAll(s, `$`, `$$`)
	}
	return `"` + s + `"`
}

// RenderSystemdUnit returns the systemd user unit contents for opts.
func RenderSystemdUnit(opts Options) ([]byte, error) {
	if opts.Binary == "" {
		return nil, errors.New("service: binary path is required")
	}
	if opts.Dir == "" {
		return nil, errors.New("service: state directory is required")
	}
	var exec []string
	for _, arg := range serveArgs(opts) {
		exec = append(exec, unitQuote(arg, true))
	}
	var b strings.Builder
	b.WriteString("[Unit]\nDescription=Glitched Engine\nAfter=network.target\n\n")
	b.WriteString("[Service]\n")
	b.WriteString("Type=simple\n")
	b.WriteString("ExecStart=" + strings.Join(exec, " ") + "\n")
	if opts.PATH != "" {
		b.WriteString("Environment=" + unitQuote("PATH="+opts.PATH, false) + "\n")
	}
	b.WriteString("Restart=on-failure\n")
	b.WriteString("RestartSec=2\n\n")
	b.WriteString("[Install]\nWantedBy=default.target\n")
	return []byte(b.String()), nil
}

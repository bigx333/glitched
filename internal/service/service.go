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
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Label is the launchd job label and base identifier.
const Label = "com.glitched.engine"

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

// Status describes the installed state and live health of the background service.
type Status struct {
	Supported bool   `json:"supported"`
	Installed bool   `json:"installed"`
	Running   bool   `json:"running"`
	PID       int    `json:"pid,omitempty"`
	UnitPath  string `json:"unitPath,omitempty"`
	LogPath   string `json:"logPath,omitempty"`
	Binary    string `json:"binary,omitempty"`
	Detail    string `json:"detail,omitempty"`
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
	out, err := exec.CommandContext(ctx, shell, "-lc", `printf %s "$PATH"`).Output()
	if err != nil {
		return fallback
	}
	if p := strings.TrimSpace(string(out)); p != "" {
		return p
	}
	return fallback
}

func resolveOptions(opts Options) (Options, error) {
	home := opts.Home
	if home == "" {
		var err error
		home, err = os.UserHomeDir()
		if err != nil {
			return opts, err
		}
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
	b.WriteString("\t\t<string>" + xmlText(opts.Binary) + "</string>\n")
	b.WriteString("\t\t<string>serve</string>\n")
	b.WriteString("\t\t<string>-dir</string>\n")
	b.WriteString("\t\t<string>" + xmlText(opts.Dir) + "</string>\n")
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

// RenderSystemdUnit returns the systemd user unit contents for opts.
func RenderSystemdUnit(opts Options) ([]byte, error) {
	if opts.Binary == "" {
		return nil, errors.New("service: binary path is required")
	}
	if opts.Dir == "" {
		return nil, errors.New("service: state directory is required")
	}
	quote := func(s string) string {
		s = strings.ReplaceAll(s, `\`, `\\`)
		s = strings.ReplaceAll(s, `"`, `\"`)
		return `"` + s + `"`
	}
	var b strings.Builder
	b.WriteString("[Unit]\nDescription=Glitched Engine\nAfter=network.target\n\n")
	b.WriteString("[Service]\n")
	b.WriteString("Type=simple\n")
	b.WriteString("ExecStart=" + quote(opts.Binary) + " serve -dir " + quote(opts.Dir) + "\n")
	if opts.PATH != "" {
		b.WriteString("Environment=" + quote("PATH="+opts.PATH) + "\n")
	}
	b.WriteString("Restart=on-failure\n")
	// Exit code 3 means another engine holds engine.lock.
	b.WriteString("RestartPreventExitStatus=3\n")
	b.WriteString("RestartSec=2\n\n")
	b.WriteString("[Install]\nWantedBy=default.target\n")
	return []byte(b.String()), nil
}

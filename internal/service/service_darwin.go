//go:build darwin

package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

func plistPath(home string) string {
	return filepath.Join(home, "Library", "LaunchAgents", Label+".plist")
}

func domainTarget() string {
	return fmt.Sprintf("gui/%d", os.Getuid())
}

func serviceTarget() string {
	return domainTarget() + "/" + Label
}

// Install writes ~/Library/LaunchAgents/com.glitched.engine.plist and
// loads it via launchctl.
func Install(ctx context.Context, opts Options) (Status, error) {
	opts, err := resolveOptions(opts)
	if err != nil {
		return Status{}, err
	}
	if err := os.MkdirAll(opts.Dir, 0o700); err != nil {
		return Status{}, err
	}
	data, err := RenderPlist(opts)
	if err != nil {
		return Status{}, err
	}
	path := plistPath(opts.Home)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return Status{}, err
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return Status{}, err
	}
	// Bootout any prior registration first; ignore not-loaded errors.
	_, _ = exec.CommandContext(ctx, "launchctl", "bootout", domainTarget(), path).CombinedOutput()
	if out, err := exec.CommandContext(ctx, "launchctl", "bootstrap", domainTarget(), path).CombinedOutput(); err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return Inspect(ctx, opts), fmt.Errorf("service: launchctl bootstrap: %s", msg)
	}
	_, _ = exec.CommandContext(ctx, "launchctl", "kickstart", serviceTarget()).CombinedOutput()
	return Inspect(ctx, opts), nil
}

// Uninstall unloads the launchd agent and removes its plist.
func Uninstall(ctx context.Context, opts Options) (Status, error) {
	opts, err := resolveOptions(opts)
	if err != nil {
		return Status{}, err
	}
	path := plistPath(opts.Home)
	_, _ = exec.CommandContext(ctx, "launchctl", "bootout", domainTarget(), path).CombinedOutput()
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return Inspect(ctx, opts), err
	}
	return Inspect(ctx, opts), nil
}

// Inspect checks whether the launchd plist exists and whether launchctl
// reports the job running.
func Inspect(ctx context.Context, opts Options) Status {
	home := opts.Home
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	dir := opts.Dir
	if dir == "" && home != "" {
		dir = filepath.Join(home, ".glitched")
	}
	st := Status{
		Supported: true,
		UnitPath:  plistPath(home),
		LogPath:   filepath.Join(dir, "service.log"),
	}
	if _, err := os.Stat(st.UnitPath); err == nil {
		st.Installed = true
	}
	if bin, err := ResolveBinary(opts.Binary); err == nil {
		st.Binary = bin
	}
	if home == "" || !st.Installed {
		return st
	}
	out, err := exec.CommandContext(ctx, "launchctl", "print", serviceTarget()).CombinedOutput()
	if err != nil {
		return st
	}
	for _, line := range strings.Split(string(out), "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "pid = ") {
			if pid, err := strconv.Atoi(strings.TrimPrefix(t, "pid = ")); err == nil && pid > 0 {
				st.PID = pid
				st.Running = true
			}
		}
		if strings.Contains(t, "state = running") {
			st.Running = true
		}
	}
	return st
}

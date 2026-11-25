//go:build linux

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

const unitName = "glitched.service"

func unitPath(home string) string {
	return filepath.Join(home, ".config", "systemd", "user", unitName)
}

// Install writes ~/.config/systemd/user/glitched.service and enables it.
func Install(ctx context.Context, opts Options) (Status, error) {
	opts, err := resolveOptions(opts)
	if err != nil {
		return Status{}, err
	}
	if err := os.MkdirAll(opts.Dir, 0o700); err != nil {
		return Status{}, err
	}
	data, err := RenderSystemdUnit(opts)
	if err != nil {
		return Status{}, err
	}
	path := unitPath(opts.Home)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return Status{}, err
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return Status{}, err
	}
	_, _ = exec.CommandContext(ctx, "systemctl", "--user", "daemon-reload").CombinedOutput()
	if out, err := exec.CommandContext(ctx, "systemctl", "--user", "enable", "--now", unitName).CombinedOutput(); err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return Inspect(ctx, opts), fmt.Errorf("service: systemctl enable: %s", msg)
	}
	return Inspect(ctx, opts), nil
}

// Uninstall disables the systemd user unit and removes the unit file.
func Uninstall(ctx context.Context, opts Options) (Status, error) {
	opts, err := resolveOptions(opts)
	if err != nil {
		return Status{}, err
	}
	_, _ = exec.CommandContext(ctx, "systemctl", "--user", "disable", "--now", unitName).CombinedOutput()
	if err := os.Remove(unitPath(opts.Home)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return Inspect(ctx, opts), err
	}
	_, _ = exec.CommandContext(ctx, "systemctl", "--user", "daemon-reload").CombinedOutput()
	return Inspect(ctx, opts), nil
}

// Inspect checks whether the systemd user unit exists and is active.
func Inspect(ctx context.Context, opts Options) Status {
	home := opts.Home
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	st := Status{
		Supported: true,
		UnitPath:  unitPath(home),
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
	out, err := exec.CommandContext(ctx, "systemctl", "--user", "show", unitName, "--property=ActiveState,MainPID").CombinedOutput()
	if err != nil {
		return st
	}
	for _, line := range strings.Split(string(out), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch k {
		case "ActiveState":
			st.Running = v == "active" || v == "activating"
		case "MainPID":
			if pid, err := strconv.Atoi(v); err == nil && pid > 0 {
				st.PID = pid
			}
		}
	}
	return st
}

package service

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const unitName = "glitched.service"

// systemd runs the service as a systemd user unit.
type systemd struct{}

// path is in the user's config directory, which XDG_CONFIG_HOME moves, as
// systemd reads it.
func (systemd) path(home string) string {
	config := os.Getenv("XDG_CONFIG_HOME")
	if !filepath.IsAbs(config) {
		config = filepath.Join(home, ".config")
	}
	return filepath.Join(config, "systemd", "user", unitName)
}

func (systemd) render(opts Options) ([]byte, error) { return RenderSystemdUnit(opts) }

func (systemd) parse(data []byte) (binary, dir, log string) {
	for _, line := range strings.Split(string(data), "\n") {
		if exec, ok := strings.CutPrefix(strings.TrimSpace(line), "ExecStart="); ok {
			binary, dir = serveOf(unitWords(exec))
		}
	}
	return binary, dir, ""
}

// unitWords splits an ExecStart line into its arguments as systemd does,
// for the quoting unitQuote writes.
func unitWords(s string) []string {
	s = strings.NewReplacer("%%", "%", "$$", "$").Replace(s)
	var words []string
	var w strings.Builder
	in, quoted := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\\' && i+1 < len(s):
			i++
			w.WriteByte(s[i])
			in = true
		case c == '"':
			quoted, in = !quoted, true
		case c == ' ' && !quoted:
			if in {
				words = append(words, w.String())
				w.Reset()
				in = false
			}
		default:
			w.WriteByte(c)
			in = true
		}
	}
	if in {
		words = append(words, w.String())
	}
	return words
}

func systemctl(ctx context.Context, args ...string) error {
	_, err := command(ctx, "systemctl", append([]string{"--user"}, args...)...)
	return err
}

func (systemd) load(ctx context.Context, _ string, restart bool) error {
	if err := systemctl(ctx, "daemon-reload"); err != nil {
		return err
	}
	if err := systemctl(ctx, "enable", unitName); err != nil {
		return err
	}
	if restart {
		return systemctl(ctx, "restart", unitName)
	}
	// Starts the unit if it stopped; one that runs keeps running.
	return systemctl(ctx, "start", unitName)
}

func (systemd) unload(ctx context.Context) error {
	return systemctl(ctx, "disable", "--now", unitName)
}

func (systemd) state(ctx context.Context) (running bool, pid int) {
	out, err := command(ctx, "systemctl", "--user", "show", unitName, "--property=ActiveState,MainPID")
	if err != nil {
		return false, 0
	}
	for _, line := range strings.Split(string(out), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch k {
		case "ActiveState":
			running = v == "active" || v == "activating"
		case "MainPID":
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				pid = n
			}
		}
	}
	return running, pid
}

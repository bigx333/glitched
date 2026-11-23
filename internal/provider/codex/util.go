package codex

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

// tail keeps the last tailLimit bytes written to it.
type tail struct {
	mu  sync.Mutex
	buf []byte
}

const tailLimit = 8 << 10

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - tailLimit; over > 0 {
		t.buf = t.buf[over:]
	}
	return len(p), nil
}

func (t *tail) reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = nil
}

// last is the last line written that says something.
func (t *tail) last() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	lines := strings.Split(strings.TrimSpace(string(t.buf)), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// openWire is the file wireEnv names, or a writer that keeps nothing.
// What is written once it is closed is dropped.
func openWire() io.WriteCloser {
	path := os.Getenv(wireEnv)
	if path == "" {
		return nopWire{}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		slog.Warn("codex: no wire capture", "err", err)
		return nopWire{}
	}
	return &lockedWriter{f: f}
}

type nopWire struct{}

func (nopWire) Write(p []byte) (int, error) { return len(p), nil }
func (nopWire) Close() error                { return nil }

type lockedWriter struct {
	mu sync.Mutex
	f  *os.File // nil once closed
}

func (l *lockedWriter) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	f := l.f
	l.f = nil
	if f == nil {
		return nil
	}
	return f.Close()
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return len(p), nil
	}
	return l.f.Write(p)
}

// tap writes what goes to w to wire too, each write after prefix.
type tap struct {
	w      io.Writer
	wire   io.Writer
	prefix string
}

func (t tap) Write(p []byte) (int, error) {
	if t.wire != (nopWire{}) {
		t.wire.Write(append([]byte(t.prefix), p...))
	}
	return t.w.Write(p)
}

// argsText is a tool's input as the card shows it: a line per field, its
// key and its value as JSON.
func argsText(input json.RawMessage) string {
	d := json.NewDecoder(bytes.NewReader(input))
	if tok, err := d.Token(); err != nil || tok != json.Delim('{') {
		return strings.TrimSpace(string(input))
	}
	var lines []string
	for d.More() {
		k, err := d.Token()
		var v json.RawMessage
		if err != nil || d.Decode(&v) != nil {
			return strings.TrimSpace(string(input))
		}
		var c bytes.Buffer
		json.Compact(&c, v)
		lines = append(lines, fmt.Sprintf("%s: %s", k, c.String()))
	}
	return strings.Join(lines, "\n")
}

// rel is path inside workspace, as the card and the steps show it, or as
// it is outside. Codex may name the workspace by the path its links
// resolve to, as macOS's /var is /private/var.
func rel(workspace, path string) string {
	// Past a symlink, .. leaves the link's target, not its directory: such
	// a path is shown as Codex gave it.
	if slices.Contains(strings.Split(filepath.ToSlash(path), "/"), "..") {
		return path
	}
	resolved, _ := filepath.EvalSymlinks(workspace)
	for _, w := range []string{workspace, resolved} {
		if r, err := filepath.Rel(w, path); w != "" && err == nil && filepath.IsLocal(r) {
			return r
		}
	}
	return path
}

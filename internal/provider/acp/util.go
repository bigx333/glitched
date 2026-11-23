package acp

import (
	"context"
	"io"
	"log/slog"
	"maps"
	"os"
	"slices"
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

func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}

func sortedKeys(m map[string]string) []string { return slices.Sorted(maps.Keys(m)) }

// openWire is the file wireEnv names, or a writer that keeps nothing.
func openWire() io.Writer {
	path := os.Getenv(wireEnv)
	if path == "" {
		return io.Discard
	}
	// A capture only a reader drains, as a FIFO, would hold the bridge
	// from the agent's stdout, and Close with it.
	if fi, err := os.Stat(path); err == nil && !fi.Mode().IsRegular() {
		slog.Warn("acp: no wire capture, as it goes only to a regular file", "path", path)
		return io.Discard
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		slog.Warn("acp: no wire capture", "err", err)
		return io.Discard
	}
	return &lockedWriter{w: f}
}

type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// tap writes what goes to w to wire too, each write after prefix.
// sent, if set, hears each write that went through.
type tap struct {
	w      io.Writer
	wire   io.Writer
	prefix string
	sent   func([]byte)
}

func (t tap) Write(p []byte) (int, error) {
	if t.wire != io.Discard {
		t.wire.Write(append([]byte(t.prefix), p...))
	}
	n, err := t.w.Write(p)
	if err == nil && t.sent != nil {
		t.sent(p)
	}
	return n, err
}

// redacted is the SDK's log, less what the agent sent: a line it could
// not parse goes to the log whole, and may hold the user's words or a
// sign-in link. Only warnings and errors pass.
type redacted struct{ slog.Handler }

func (r redacted) Enabled(ctx context.Context, l slog.Level) bool {
	return l >= slog.LevelWarn && r.Handler.Enabled(ctx, l)
}

func (r redacted) Handle(ctx context.Context, rec slog.Record) error {
	out := slog.NewRecord(rec.Time, rec.Level, "acp sdk: "+rec.Message, rec.PC)
	rec.Attrs(func(a slog.Attr) bool {
		if a.Key == "raw" {
			a = slog.Int("raw bytes", len(a.Value.String()))
		}
		out.AddAttrs(a)
		return true
	})
	return r.Handler.Handle(ctx, out)
}

func (r redacted) WithAttrs(as []slog.Attr) slog.Handler { return redacted{r.Handler.WithAttrs(as)} }

func (r redacted) WithGroup(name string) slog.Handler { return redacted{r.Handler.WithGroup(name)} }

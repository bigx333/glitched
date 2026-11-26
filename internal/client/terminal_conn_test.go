package client

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/plugins/terminal"

	"glitched/internal/api"
	"glitched/internal/pty"
)

var errCut = errors.New("test: the connection is cut")

// flaky serves the terminal calls of a remote engine from a pty.Manager
// over a connection the test can cut, and restore to another engine.
type flaky struct {
	api.Endpoint
	mu          sync.Mutex
	be          *pty.Manager
	down        bool
	dropReplyOn bool
	streams     []*cutStream
	refused     chan struct{}
}

func newFlaky(be *pty.Manager) *flaky {
	return &flaky{be: be, refused: make(chan struct{}, 1)}
}

func (f *flaky) backend() (*pty.Manager, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		select {
		case f.refused <- struct{}{}:
		default:
		}
		return nil, errCut
	}
	return f.be, nil
}

func (f *flaky) cut() {
	f.mu.Lock()
	f.down = true
	streams := f.streams
	f.streams = nil
	f.mu.Unlock()
	for _, st := range streams {
		st.cut()
	}
}

// restore ends the cut, to be another engine unless be is nil.
func (f *flaky) restore(be *pty.Manager) {
	f.mu.Lock()
	f.down = false
	if be != nil {
		f.be = be
	}
	f.mu.Unlock()
}

func (f *flaky) OpenTerminal(ctx context.Context, req api.TerminalOpen) (api.TerminalStream, error) {
	be, err := f.backend()
	if err != nil {
		return nil, err
	}
	st, err := be.OpenTerminal(ctx, req)
	if err != nil {
		return nil, err
	}
	cs := &cutStream{TerminalStream: st}
	f.mu.Lock()
	f.streams = append(f.streams, cs)
	f.mu.Unlock()
	return cs, nil
}

func (f *flaky) TerminalInput(ctx context.Context, req api.TerminalInput) error {
	be, err := f.backend()
	if err != nil {
		return err
	}
	err = be.TerminalInput(ctx, req)
	f.mu.Lock()
	drop := f.dropReplyOn
	f.dropReplyOn = false
	f.mu.Unlock()
	if err == nil && drop {
		return errCut
	}
	return err
}

func (f *flaky) TerminalResize(ctx context.Context, req api.TerminalResize) error {
	be, err := f.backend()
	if err != nil {
		return err
	}
	return be.TerminalResize(ctx, req)
}

func (f *flaky) TerminalClaim(ctx context.Context, req api.TerminalClaim) (api.TerminalLease, error) {
	be, err := f.backend()
	if err != nil {
		return api.TerminalLease{}, err
	}
	return be.TerminalClaim(ctx, req)
}

type cutStream struct {
	api.TerminalStream
	mu     sync.Mutex
	broken bool
}

func (s *cutStream) cut() {
	s.mu.Lock()
	s.broken = true
	s.mu.Unlock()
	s.TerminalStream.Close()
}

func (s *cutStream) Next(ctx context.Context) (api.TerminalItem, error) {
	it, err := s.TerminalStream.Next(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.broken {
		return api.TerminalItem{}, errCut
	}
	return it, err
}

// viewer is a GUI with its own client ID that reaches f as a remote.
type viewer struct {
	m   *Manager
	env *Env
}

func newViewer(t *testing.T, f *flaky) viewer {
	t.Helper()
	m := New(Config{Post: func(fn func()) { fn() }})
	t.Cleanup(func() { m.cancel(); m.wg.Wait() })
	return viewer{m: m, env: &Env{Kind: Remote, Status: Online{}, ctx: m.ctx, session: &session{ctx: m.ctx, ep: f}}}
}

func (v viewer) open(t *testing.T, ws string, cols, rows int) *TerminalConn {
	t.Helper()
	conn, err := v.m.OpenTerminal(v.env, ws, cols, rows, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func write(t *testing.T, conn *TerminalConn, s string) {
	t.Helper()
	if n, err := conn.Write([]byte(s)); err != nil || n != len(s) {
		t.Fatalf("Write(%q) = %d, %v", s, n, err)
	}
}

// waitSize waits until the engine's PTY in ws is cols×rows.
func waitSize(t *testing.T, mgr *pty.Manager, ws string, cols, rows int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	st, err := mgr.OpenTerminal(ctx, api.TerminalOpen{Workspace: ws, Session: mgr.SessionID(ws), After: 1 << 40})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	last := ""
	for {
		it, err := st.Next(ctx)
		if err != nil {
			t.Fatalf("the PTY is not %dx%d (last %s): %v", cols, rows, last, err)
		}
		if it.Cols > 0 {
			last = fmt.Sprintf("%dx%d", it.Cols, it.Rows)
		}
		if it.Cols == cols && it.Rows == rows {
			return
		}
	}
}

func shPTYs(t *testing.T, opts pty.Options) *pty.Manager {
	t.Helper()
	if opts.Command == nil {
		opts.Command = []string{"/bin/sh"}
	}
	mgr := pty.New(opts)
	t.Cleanup(func() { mgr.Close() })
	return mgr
}

func TestWatcherKeepsSeeingTheLeaseHolder(t *testing.T) {
	f := newFlaky(shPTYs(t, pty.Options{}))
	ws := t.TempDir()
	a := newViewer(t, f).open(t, ws, 80, 24)
	write(t, a, "echo a-$((1+1))\n")
	readConnUntil(t, a, "a-2")

	b := newViewer(t, f).open(t, ws, 80, 24)
	readConnUntil(t, b, "a-2")
	if _, held := b.LeaseStatus(); !held {
		t.Fatal("after the replay, B does not see that A holds the lease")
	}
	write(t, a, "echo more-$((2+2))\n")
	readConnUntil(t, b, "more-4")
	if holder, held := b.LeaseStatus(); !held || holder != a.m.clientID {
		t.Fatalf("after A's next output, B's LeaseStatus = (%q, %v), want (%q, true)", holder, held, a.m.clientID)
	}
}

func TestWriteDuringAnOutageKeepsTheInputAlive(t *testing.T) {
	f := newFlaky(shPTYs(t, pty.Options{}))
	ws := t.TempDir()
	conn := newViewer(t, f).open(t, ws, 80, 24)
	write(t, conn, "echo up-$((1+1))\n")
	readConnUntil(t, conn, "up-2")

	f.cut()
	write(t, conn, "echo dropped\n")
	f.restore(nil)
	write(t, conn, "echo back-$((2+3))\n")
	readConnUntil(t, conn, "back-5")
}

func TestWriteLostReplyDoesNotDuplicateCommand(t *testing.T) {
	f := newFlaky(shPTYs(t, pty.Options{}))
	ws := t.TempDir()
	outFile := filepath.Join(ws, "count.txt")
	conn := newViewer(t, f).open(t, ws, 80, 24)
	write(t, conn, "echo ready-$((1+1))\n")
	readConnUntil(t, conn, "ready-2")

	f.mu.Lock()
	f.dropReplyOn = true
	f.mu.Unlock()
	write(t, conn, fmt.Sprintf("echo line >> %q\n", outFile))
	write(t, conn, "echo done-$((3+4))\n")
	readConnUntil(t, conn, "done-7")

	b, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("read %s: %v", outFile, err)
	}
	if lines := strings.Split(strings.TrimSpace(string(b)), "\n"); len(lines) != 1 {
		t.Fatalf("command ran %d times (%q), want 1", len(lines), string(b))
	}
}

func TestOfflineWriteRespectsDeadline(t *testing.T) {
	prev := terminalWriteTimeout
	terminalWriteTimeout = 300 * time.Millisecond
	t.Cleanup(func() { terminalWriteTimeout = prev })

	f := newFlaky(shPTYs(t, pty.Options{}))
	ws := t.TempDir()
	conn := newViewer(t, f).open(t, ws, 80, 24)
	write(t, conn, "echo up-$((1+1))\n")
	readConnUntil(t, conn, "up-2")

	f.cut()
	// Start Read reconnecting in the background while the engine stays down.
	go func() {
		buf := make([]byte, 256)
		_, _ = conn.Read(buf)
	}()
	<-f.refused

	start := time.Now()
	write(t, conn, "x")
	if took := time.Since(start); took > time.Second {
		t.Fatalf("offline Write took %v, want <= 1s (write timeout is %v)", took, terminalWriteTimeout)
	}
}

func TestTakeControlResizesToThisView(t *testing.T) {
	mgr := shPTYs(t, pty.Options{})
	f := newFlaky(mgr)
	ws := t.TempDir()
	a := newViewer(t, f).open(t, ws, 80, 24)
	write(t, a, "echo a-$((1+1))\n")
	readConnUntil(t, a, "a-2")

	b := newViewer(t, f).open(t, ws, 120, 40)
	readConnUntil(t, b, "a-2")
	b.TakeControl()
	waitSize(t, mgr, ws, 120, 40)
}

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func TestTypingAfterTheLeaseLapsesResizesToThisView(t *testing.T) {
	clk := &clock{now: time.Unix(1e9, 0)}
	mgr := shPTYs(t, pty.Options{LeaseTTL: time.Minute, Now: clk.Now})
	f := newFlaky(mgr)
	ws := t.TempDir()
	a := newViewer(t, f).open(t, ws, 80, 24)
	write(t, a, "echo a-$((1+1))\n")
	readConnUntil(t, a, "a-2")

	b := newViewer(t, f).open(t, ws, 120, 40)
	readConnUntil(t, b, "a-2")
	clk.advance(2 * time.Minute)
	write(t, b, "echo b-$((3+4))\n")
	readConnUntil(t, b, "b-7")
	waitSize(t, mgr, ws, 120, 40)
}

func TestReconnectToANewSessionResumesWhereItLeft(t *testing.T) {
	first, second := shPTYs(t, pty.Options{}), shPTYs(t, pty.Options{})
	f := newFlaky(first)
	ws := t.TempDir()
	conn := newViewer(t, f).open(t, ws, 80, 24)
	write(t, conn, "printf '%2000s\\n' first-$((1+1))\n")
	readConnUntil(t, conn, "first-2")

	// The engine restarts: the reconnect finds a new, shorter session.
	f.cut()
	f.restore(second)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go func() {
		for second.SessionID(ws).IsZero() && ctx.Err() == nil {
			time.Sleep(10 * time.Millisecond)
		}
		second.TerminalInput(ctx, api.TerminalInput{Workspace: ws, Data: []byte("echo second-$((2*2))\n")})
	}()
	readConnUntil(t, conn, "second-4")

	// Another drop, during which the shell prints more.
	f.cut()
	if err := second.TerminalInput(ctx, api.TerminalInput{Workspace: ws, Data: []byte("echo missed-$((3*3))\n")}); err != nil {
		t.Fatal(err)
	}
	watch, err := second.OpenTerminal(ctx, api.TerminalOpen{Workspace: ws})
	if err != nil {
		t.Fatal(err)
	}
	for seen := ""; !strings.Contains(seen, "missed-9"); {
		it, err := watch.Next(ctx)
		if err != nil {
			t.Fatal(err)
		}
		seen += string(it.Data)
	}
	watch.Close()
	f.restore(nil)

	out := readConnUntil(t, conn, "missed-9")
	if strings.Contains(out, "second-4") {
		t.Fatalf("the reconnect replayed the session from its start: %q", out)
	}
}

// waitScreen waits until the terminal's screen satisfies ok.
func waitScreen(t *testing.T, term *terminal.Terminal, what string, ok func(string) bool) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		s := term.Text()
		if ok(s) {
			return s
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s; the terminal shows:\n%s", what, s)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestReattachTypesNoQueryAnswers(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("no bash:", err)
	}
	if err := terminal.Load(); err != nil {
		t.Skip("libghostty-vt unavailable:", err)
	}
	f := newFlaky(shPTYs(t, pty.Options{Command: []string{"bash", "--norc", "--noprofile"}}))
	v := newViewer(t, f)
	ws := t.TempDir()
	attach := func() *terminal.Terminal {
		term, err := terminal.New(terminal.Options{Conn: v.open(t, ws, 80, 24)})
		if err != nil {
			t.Fatal(err)
		}
		return term
	}
	ask := func(marker string) []byte {
		return []byte(`printf '\033[c'; IFS= read -rs -t 5 -d c reply; echo ` + marker + `-${#reply}` + "\n")
	}
	answered := func(marker string) func(string) bool {
		return regexp.MustCompile(marker + `-[1-9]`).MatchString
	}

	term1 := attach()
	term1.Send(ask("answered"))
	waitScreen(t, term1, "the first answer", answered("answered"))
	term1.Send([]byte("clear; cat -v\n\x01up\n"))
	waitScreen(t, term1, "cat to run", func(s string) bool { return strings.Contains(s, "^Aup") })
	term1.Close()

	term2 := attach()
	defer term2.Close()
	waitScreen(t, term2, "the replay", func(s string) bool { return strings.Contains(s, "^Aup") })
	term2.Send([]byte("typed-after-reattach\n"))
	screen := waitScreen(t, term2, "cat to echo", func(s string) bool { return strings.Count(s, "typed-after-reattach") >= 2 })
	if strings.Contains(screen, "^[") {
		t.Fatalf("reattaching typed a query's answer into cat:\n%s", screen)
	}

	term2.Send([]byte("\x04"))
	term2.Send(ask("again"))
	waitScreen(t, term2, "a live query's answer after reattaching", answered("again"))
}

func TestSplitQueryAcrossDisconnectTypesNoAnswer(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("no bash:", err)
	}
	if err := terminal.Load(); err != nil {
		t.Skip("libghostty-vt unavailable:", err)
	}
	mgr := shPTYs(t, pty.Options{Command: []string{"bash", "--norc", "--noprofile"}})
	f := newFlaky(mgr)
	v := newViewer(t, f)
	ws := t.TempDir()
	conn := v.open(t, ws, 80, 24)
	term, err := terminal.New(terminal.Options{Conn: conn})
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()

	// Emit the start of a DA1 query (`\x1b[`), let Ghostty receive it, cut the
	// link, then finish the query (`c`) while offline and start `cat -v`.
	fifo := filepath.Join(ws, "gate")
	cmd := fmt.Sprintf("mkfifo %q; printf 'SPLIT_START\\033['; read _ < %q; printf 'c'; clear; cat -v\n", fifo, fifo)
	term.Send([]byte(cmd))
	waitScreen(t, term, "SPLIT_START", func(s string) bool { return strings.Contains(s, "SPLIT_START") })

	// Give Read a moment to ingest the trailing `\x1b[` right after SPLIT_START.
	time.Sleep(150 * time.Millisecond)
	f.cut()

	// Unblock the shell so it emits `c` and starts `cat -v` while offline.
	if err := os.WriteFile(fifo, []byte("go\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)

	f.restore(nil)
	term.Send([]byte("after-split\n"))
	screen := waitScreen(t, term, "after-split echoed by cat", func(s string) bool {
		return strings.Count(s, "after-split") >= 2
	})
	if strings.Contains(screen, "^[[?") {
		t.Fatalf("split DA1 query across disconnect injected an answer into cat:\n%s", screen)
	}
}

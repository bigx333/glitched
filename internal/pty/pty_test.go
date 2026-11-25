package pty

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"glitched/internal/api"
)

func TestRingBufferWrapAndReplay(t *testing.T) {
	r := newRing(5)
	if got := r.write([]byte("abc")); got != 3 {
		t.Fatalf("end = %d, want 3", got)
	}
	if out, end, trunc := r.readFrom(0); trunc || end != 3 || string(out) != "abc" {
		t.Fatalf("readFrom(0) = (%q, %d, %v), want (abc, 3, false)", out, end, trunc)
	}
	if got := r.write([]byte("defg")); got != 7 {
		t.Fatalf("end = %d, want 7", got)
	}
	if out, end, trunc := r.readFrom(1); !trunc || end != 7 || string(out) != "cdefg" {
		t.Fatalf("readFrom(1) = (%q, %d, %v), want (cdefg, 7, true)", out, end, trunc)
	}
	if out, end, trunc := r.readFrom(4); trunc || end != 7 || string(out) != "efg" {
		t.Fatalf("readFrom(4) = (%q, %d, %v), want (efg, 7, false)", out, end, trunc)
	}
	if got := r.write([]byte("1234567")); got != 14 {
		t.Fatalf("end = %d, want 14", got)
	}
	if out, end, trunc := r.readFrom(8); !trunc || end != 14 || string(out) != "34567" {
		t.Fatalf("readFrom(8) = (%q, %d, %v), want (34567, 14, true)", out, end, trunc)
	}
}

func readUntilContains(t *testing.T, ctx context.Context, st api.TerminalStream, want string) (string, api.TerminalItem) {
	t.Helper()
	var buf strings.Builder
	for {
		it, err := st.Next(ctx)
		if err != nil {
			t.Fatalf("stream Next error before seeing %q (got %q): %v", want, buf.String(), err)
		}
		buf.Write(it.Data)
		if strings.Contains(buf.String(), want) {
			return buf.String(), it
		}
	}
}

func TestPTYPersistsAcrossDetachAndReplays(t *testing.T) {
	ws := t.TempDir()
	mgr := New(Options{Command: []string{"/bin/sh"}})
	defer mgr.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	st1, err := mgr.OpenTerminal(ctx, api.TerminalOpen{
		Workspace: ws,
		Cols:      80,
		Rows:      24,
		Client:    "gui-1",
	})
	if err != nil {
		t.Fatalf("OpenTerminal 1: %v", err)
	}

	// Read initial metadata/prompt item so we learn Session ID.
	first, err := st1.Next(ctx)
	if err != nil {
		t.Fatalf("Next initial: %v", err)
	}
	if first.Session.IsZero() {
		t.Fatal("expected non-zero session ID")
	}

	if err := mgr.TerminalInput(ctx, api.TerminalInput{
		Workspace: ws,
		Session:   first.Session,
		Client:    "gui-1",
		Data:      []byte("echo marker-one-123\n"),
	}); err != nil {
		t.Fatalf("TerminalInput: %v", err)
	}

	out1, last1 := readUntilContains(t, ctx, st1, "marker-one-123")
	if last1.Offset == 0 {
		t.Fatalf("expected non-zero byte offset, got %d", last1.Offset)
	}
	// Close first stream (detaches GUI without stopping PTY).
	if err := st1.Close(); err != nil {
		t.Fatalf("st1 Close: %v", err)
	}

	// While detached, send another command into the surviving PTY? First verify
	// reopening from offset 0 replays marker-one-123 and keeps exact session ID.
	st2, err := mgr.OpenTerminal(ctx, api.TerminalOpen{
		Workspace: ws,
		Session:   first.Session,
		After:     0,
		Client:    "gui-2",
	})
	if err != nil {
		t.Fatalf("OpenTerminal 2: %v", err)
	}
	defer st2.Close()

	out2, last2 := readUntilContains(t, ctx, st2, "marker-one-123")
	if last2.Session != first.Session {
		t.Fatalf("session = %s, want %s (same PTY)", last2.Session, first.Session)
	}
	_ = out1

	// Resume from last1.Offset and run a second command.
	if err := st2.Close(); err != nil {
		t.Fatalf("st2 Close: %v", err)
	}
	st3, err := mgr.OpenTerminal(ctx, api.TerminalOpen{
		Workspace: ws,
		Session:   first.Session,
		After:     last2.Offset,
		Client:    "gui-1",
	})
	if err != nil {
		t.Fatalf("OpenTerminal 3: %v", err)
	}
	defer st3.Close()

	if err := mgr.TerminalInput(ctx, api.TerminalInput{
		Workspace: ws,
		Session:   first.Session,
		Client:    "gui-1",
		Data:      []byte("echo marker-two-456\n"),
	}); err != nil {
		t.Fatalf("TerminalInput 2: %v", err)
	}
	_, _ = readUntilContains(t, ctx, st3, "marker-two-456")
	_ = out2
}

func TestFlowControlPauseAndResume(t *testing.T) {
	ws := t.TempDir()
	mgr := New(Options{
		Command:       []string{"/bin/sh", "-c", "sleep 60"},
		ReplayBytes:   64 << 10,
		HighWatermark: 1024,
		LowWatermark:  512,
	})
	defer mgr.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	st, err := mgr.OpenTerminal(ctx, api.TerminalOpen{Workspace: ws, Client: "c1"})
	if err != nil {
		t.Fatalf("OpenTerminal: %v", err)
	}
	defer st.Close()
	_, err = st.Next(ctx) // drain initial frame
	if err != nil {
		t.Fatalf("initial Next: %v", err)
	}

	sub := st.(*subStream)
	sess := sub.sess

	// Simulate slow consumer: push 4000 bytes in 500-byte writes without calling Next.
	total := bytes.Repeat([]byte("A"), 4000)
	for i := 0; i < len(total); i += 500 {
		chunk := total[i : i+500]
		sess.mu.Lock()
		end := sess.ring.write(chunk)
		start := end - uint64(len(chunk))
		if !sub.paused {
			sub.enqueueBytesLocked(chunk, start, false, false, sess.cols, sess.rows, sess.leaseClient)
			sub.signalLocked()
		}
		sess.mu.Unlock()
	}

	sess.mu.Lock()
	paused := sub.paused
	queued := sub.queuedBytes
	sess.mu.Unlock()
	if !paused {
		t.Fatal("expected slow subscriber to pause at high watermark")
	}
	if queued > 1024 {
		t.Fatalf("queuedBytes = %d, want <= 1024", queued)
	}

	// Drain until all 4000 bytes arrive cleanly without loss.
	var got bytes.Buffer
	var lastOffset uint64
	for got.Len() < 4000 {
		it, err := st.Next(ctx)
		if err != nil {
			t.Fatalf("Next during drain: %v", err)
		}
		if it.Offset < lastOffset {
			t.Fatalf("non-monotonic offset %d < %d", it.Offset, lastOffset)
		}
		lastOffset = it.Offset
		got.Write(it.Data)
	}
	if got.Len() != 4000 || !bytes.Equal(got.Bytes(), total) {
		t.Fatalf("draining yielded %d bytes, want exact 4000 bytes", got.Len())
	}
}

func TestInputLeaseExpiringAndClaim(t *testing.T) {
	ws := t.TempDir()
	var (
		mu  sync.Mutex
		now = time.Unix(1000, 0)
	)
	mgr := New(Options{
		Command:  []string{"/bin/sh", "-c", "sleep 60"},
		LeaseTTL: 5 * time.Second,
		Now: func() time.Time {
			mu.Lock()
			defer mu.Unlock()
			return now
		},
	})
	defer mgr.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	st, err := mgr.OpenTerminal(ctx, api.TerminalOpen{Workspace: ws, Client: "client-a"})
	if err != nil {
		t.Fatalf("OpenTerminal: %v", err)
	}
	defer st.Close()
	first, err := st.Next(ctx)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if first.LeaseClient != "client-a" {
		t.Fatalf("LeaseClient = %q, want client-a", first.LeaseClient)
	}

	// client-b cannot write or resize while client-a holds active lease.
	err = mgr.TerminalInput(ctx, api.TerminalInput{Workspace: ws, Client: "client-b", Data: []byte("ls\n")})
	if !errors.Is(err, api.ErrLeaseHeld) {
		t.Fatalf("client-b input err = %v, want ErrLeaseHeld", err)
	}
	err = mgr.TerminalResize(ctx, api.TerminalResize{Workspace: ws, Client: "client-b", Cols: 100, Rows: 30})
	if !errors.Is(err, api.ErrLeaseHeld) {
		t.Fatalf("client-b resize err = %v, want ErrLeaseHeld", err)
	}

	// Non-forced claim fails while lease active, forced claim succeeds.
	if _, err := mgr.TerminalClaim(ctx, api.TerminalClaim{Workspace: ws, Client: "client-b", Force: false}); !errors.Is(err, api.ErrLeaseHeld) {
		t.Fatalf("unforced claim err = %v, want ErrLeaseHeld", err)
	}
	lease, err := mgr.TerminalClaim(ctx, api.TerminalClaim{Workspace: ws, Client: "client-b", Force: true})
	if err != nil || lease.Client != "client-b" {
		t.Fatalf("forced claim = (%+v, %v), want client-b", lease, err)
	}
	if err := mgr.TerminalInput(ctx, api.TerminalInput{Workspace: ws, Client: "client-a", Data: []byte("ls\n")}); !errors.Is(err, api.ErrLeaseHeld) {
		t.Fatalf("client-a after takeover err = %v, want ErrLeaseHeld", err)
	}

	// Advance clock past TTL -> client-a can write again once expired.
	mu.Lock()
	now = now.Add(6 * time.Second)
	mu.Unlock()
	if err := mgr.TerminalInput(ctx, api.TerminalInput{Workspace: ws, Client: "client-a", Data: []byte("\n")}); err != nil {
		t.Fatalf("client-a after expiry err = %v", err)
	}
}

func TestPTYResizeAndExit(t *testing.T) {
	ws := t.TempDir()
	mgr := New(Options{Command: []string{"/bin/sh"}})
	defer mgr.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	st, err := mgr.OpenTerminal(ctx, api.TerminalOpen{Workspace: ws, Cols: 80, Rows: 24, Client: "c1"})
	if err != nil {
		t.Fatalf("OpenTerminal: %v", err)
	}
	defer st.Close()
	first, err := st.Next(ctx)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if err := mgr.TerminalResize(ctx, api.TerminalResize{Workspace: ws, Session: first.Session, Client: "c1", Cols: 111, Rows: 37}); err != nil {
		t.Fatalf("TerminalResize: %v", err)
	}
	if err := mgr.TerminalInput(ctx, api.TerminalInput{Workspace: ws, Session: first.Session, Client: "c1", Data: []byte("exit 7\n")}); err != nil {
		t.Fatalf("TerminalInput exit: %v", err)
	}

	var exitItem api.TerminalItem
	for {
		it, err := st.Next(ctx)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Next until exit: %v", err)
		}
		if it.Exited {
			exitItem = it
		}
	}
	if !exitItem.Exited || exitItem.ExitCode != 7 {
		t.Fatalf("exitItem = %+v, want Exited=true ExitCode=7", exitItem)
	}
}

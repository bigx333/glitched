package client

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"glitched/internal/api"
	"glitched/internal/transport"
)

type terminals interface {
	OpenTerminal(ctx context.Context, req api.TerminalOpen) (api.TerminalStream, error)
	TerminalInput(ctx context.Context, req api.TerminalInput) error
	TerminalResize(ctx context.Context, req api.TerminalResize) error
	TerminalClaim(ctx context.Context, req api.TerminalClaim) (api.TerminalLease, error)
}

type termTarget struct {
	ctx context.Context
	fn  terminals
}

// TerminalConn adapts an engine-owned PTY stream to an io.ReadWriteCloser
// with Resize support for mygo/plugins/terminal. Closing it detaches from the
// stream without stopping the engine PTY.
type TerminalConn struct {
	m         *Manager
	env       *Env
	workspace string
	onStatus  func()

	ctx    context.Context
	cancel context.CancelFunc

	openMu      sync.Mutex
	mu          sync.Mutex
	target      termTarget
	stream      api.TerminalStream
	session     api.ID
	offset      uint64
	cols        int
	rows        int
	ptyCols     int // the PTY's size the engine last reported; 0 before
	ptyRows     int
	leaseHolder string
	heldByOther bool
	exited      bool
	exitCode    int
	buf         []byte
	closed      bool
}

func (m *Manager) terminalTarget(e *Env) termTarget {
	if e == nil || e.Status != Status(Online{}) {
		return termTarget{}
	}
	if e.Kind == Local && m.embedded != nil {
		return termTarget{ctx: e.ctx, fn: m.embedded}
	}
	if e.session != nil {
		if tm, ok := e.session.ep.(terminals); ok {
			return termTarget{ctx: e.session.ctx, fn: tm}
		}
	}
	return termTarget{}
}

// OpenTerminal returns a TerminalConn attached to workspace's engine-owned PTY
// on env. Must be called on the Manager's Post thread; it performs no blocking I/O.
func (m *Manager) OpenTerminal(env *Env, workspace string, cols, rows int, onStatus func()) (*TerminalConn, error) {
	if m.closed {
		return nil, errors.New("client: manager closed")
	}
	tg := m.terminalTarget(env)
	if tg.fn == nil {
		return nil, errors.New("client: engine offline")
	}
	if cols <= 0 {
		cols = 80
	}
	if rows <= 0 {
		rows = 24
	}
	ctx, cancel := context.WithCancel(m.ctx)
	return &TerminalConn{
		m:         m,
		env:       env,
		workspace: workspace,
		onStatus:  onStatus,
		ctx:       ctx,
		cancel:    cancel,
		target:    tg,
		cols:      cols,
		rows:      rows,
	}, nil
}

func (tc *TerminalConn) notifyStatus() {
	if tc.onStatus == nil {
		return
	}
	tc.m.post(tc.ctx, tc.onStatus)
}

func (tc *TerminalConn) resolveTarget() (termTarget, bool) {
	ch := make(chan termTarget, 1)
	tc.m.post(tc.ctx, func() {
		ch <- tc.m.terminalTarget(tc.env)
	})
	select {
	case tg := <-ch:
		return tg, tg.fn != nil
	case <-tc.ctx.Done():
		return termTarget{}, false
	}
}

func (tc *TerminalConn) ensureStream(ctx context.Context) (api.TerminalStream, termTarget, api.ID, error) {
	tc.openMu.Lock()
	defer tc.openMu.Unlock()

	backoff := 250 * time.Millisecond
	for {
		tc.mu.Lock()
		if tc.closed || tc.exited {
			tc.mu.Unlock()
			return nil, termTarget{}, api.ID{}, io.EOF
		}
		st := tc.stream
		tg := tc.target
		sess := tc.session
		after := tc.offset
		cols := tc.cols
		rows := tc.rows
		tc.mu.Unlock()

		if st != nil {
			return st, tg, sess, nil
		}
		if tg.fn == nil {
			resolved, ok := tc.resolveTarget()
			if !ok {
				if ctx.Err() != nil || tc.ctx.Err() != nil {
					return nil, termTarget{}, api.ID{}, io.EOF
				}
				select {
				case <-time.After(backoff):
					backoff = min(backoff*2, 2*time.Second)
					continue
				case <-ctx.Done():
					return nil, termTarget{}, api.ID{}, ctx.Err()
				case <-tc.ctx.Done():
					return nil, termTarget{}, api.ID{}, io.EOF
				}
			}
			tg = resolved
			tc.mu.Lock()
			tc.target = tg
			tc.mu.Unlock()
		}

		opened, err := tg.fn.OpenTerminal(tc.ctx, api.TerminalOpen{
			Workspace: tc.workspace,
			Session:   sess,
			After:     after,
			Cols:      cols,
			Rows:      rows,
			Client:    tc.m.clientID,
		})
		if err != nil {
			if errors.Is(err, transport.ErrUnknownMethod) || errors.Is(err, api.ErrNotAllowed) {
				return nil, termTarget{}, api.ID{}, err
			}
			if ctx.Err() != nil || tc.ctx.Err() != nil {
				return nil, termTarget{}, api.ID{}, io.EOF
			}
			tc.mu.Lock()
			tc.target = termTarget{}
			tc.mu.Unlock()
			select {
			case <-time.After(backoff):
				backoff = min(backoff*2, 2*time.Second)
				continue
			case <-ctx.Done():
				return nil, termTarget{}, api.ID{}, ctx.Err()
			case <-tc.ctx.Done():
				return nil, termTarget{}, api.ID{}, io.EOF
			}
		}

		tc.mu.Lock()
		if tc.closed {
			tc.mu.Unlock()
			_ = opened.Close()
			return nil, termTarget{}, api.ID{}, io.EOF
		}
		tc.stream = opened
		curCols, curRows := tc.cols, tc.rows
		curSess := tc.session
		tc.mu.Unlock()

		if curCols != cols || curRows != rows {
			rctx, rcancel := context.WithTimeout(tc.ctx, 2*time.Second)
			_ = tg.fn.TerminalResize(rctx, api.TerminalResize{
				Workspace: tc.workspace,
				Session:   curSess,
				Client:    tc.m.clientID,
				Cols:      curCols,
				Rows:      curRows,
			})
			rcancel()
		}
		return opened, tg, curSess, nil
	}
}

func (tc *TerminalConn) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	backoff := 250 * time.Millisecond
	for {
		tc.mu.Lock()
		if len(tc.buf) > 0 {
			n := copy(p, tc.buf)
			tc.buf = tc.buf[n:]
			tc.mu.Unlock()
			return n, nil
		}
		if tc.closed || tc.exited {
			tc.mu.Unlock()
			return 0, io.EOF
		}
		tc.mu.Unlock()

		st, _, _, err := tc.ensureStream(tc.ctx)
		if err != nil {
			return 0, err
		}

		item, err := st.Next(tc.ctx)
		if err != nil {
			_ = st.Close()
			tc.mu.Lock()
			current := tc.stream == st
			if current {
				tc.stream = nil
				tc.target = termTarget{}
			}
			done := tc.exited || tc.closed
			tc.mu.Unlock()
			switch {
			case done:
				return 0, io.EOF
			case !current: // Write reconnected meanwhile
				continue
			case errors.Is(err, io.EOF):
				return 0, io.EOF
			case errors.Is(err, transport.ErrUnknownMethod) || errors.Is(err, api.ErrNotAllowed):
				return 0, err
			case tc.ctx.Err() != nil:
				return 0, io.EOF
			}
			select {
			case <-time.After(backoff):
				backoff = min(backoff*2, 2*time.Second)
				continue
			case <-tc.ctx.Done():
				return 0, io.EOF
			}
		}
		backoff = 250 * time.Millisecond

		tc.mu.Lock()
		fresh := !item.Session.IsZero() && item.Session != tc.session
		if fresh {
			tc.session = item.Session
			tc.offset = item.Offset
			tc.ptyCols, tc.ptyRows = 0, 0
		} else if item.Offset > tc.offset || (item.Truncated && item.Offset > 0) {
			tc.offset = item.Offset
		}
		if item.Cols > 0 && item.Rows > 0 {
			tc.ptyCols, tc.ptyRows = item.Cols, item.Rows
		}
		newHeld := item.LeaseClient != "" && item.LeaseClient != tc.m.clientID
		changed := tc.leaseHolder != item.LeaseClient || tc.heldByOther != newHeld
		tc.leaseHolder = item.LeaseClient
		tc.heldByOther = newHeld
		if item.Exited {
			tc.exited = true
			tc.exitCode = item.ExitCode
		}
		n := copy(p, item.Data)
		if n < len(item.Data) {
			tc.buf = append(tc.buf[:0], item.Data[n:]...)
		}
		exited := item.Exited
		tc.mu.Unlock()

		if changed {
			tc.notifyStatus()
		}
		if fresh {
			tc.syncSize()
		}
		if n > 0 {
			return n, nil
		}
		if exited {
			return 0, io.EOF
		}
	}
}

// Write types p into the PTY. While the engine is out of reach it gives up
// after a few seconds and drops p rather than failing, as a failed write
// would end the terminal's input for good; Read reconnects meanwhile.
func (tc *TerminalConn) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	ctx, cancel := context.WithTimeout(tc.ctx, 5*time.Second)
	defer cancel()
	data := bytes.Clone(p)
	for retried := false; ; retried = true {
		st, tg, sess, err := tc.ensureStream(ctx)
		if err == nil {
			err = tg.fn.TerminalInput(ctx, api.TerminalInput{
				Workspace: tc.workspace,
				Session:   sess,
				Client:    tc.m.clientID,
				Data:      data,
			})
		}
		switch {
		case err == nil:
			tc.mu.Lock()
			changed := tc.heldByOther || tc.leaseHolder != tc.m.clientID
			tc.heldByOther = false
			tc.leaseHolder = tc.m.clientID
			tc.mu.Unlock()
			if changed {
				tc.notifyStatus()
				tc.syncSize()
			}
			return len(p), nil
		case errors.Is(err, api.ErrLeaseHeld):
			tc.mu.Lock()
			changed := !tc.heldByOther
			tc.heldByOther = true
			tc.mu.Unlock()
			if changed {
				tc.notifyStatus()
			}
			return len(p), nil
		case errors.Is(err, transport.ErrUnknownMethod) || errors.Is(err, api.ErrNotAllowed):
			return 0, err
		case tc.ended():
			return 0, io.ErrClosedPipe
		case retried || st == nil || ctx.Err() != nil:
			return len(p), nil
		}
		tc.forget(st, sess, err)
	}
}

// ended reports whether the conn is closed or its session exited.
func (tc *TerminalConn) ended() bool {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	return tc.closed || tc.exited || tc.ctx.Err() != nil
}

// forget drops st, whose call failed with err, so that the next call
// reconnects, and sess with it when the engine no longer has it.
func (tc *TerminalConn) forget(st api.TerminalStream, sess api.ID, err error) {
	tc.mu.Lock()
	current := tc.stream == st
	if current {
		tc.stream = nil
		tc.target = termTarget{}
	}
	if errors.Is(err, api.ErrNotFound) && tc.session == sess {
		tc.session = api.ID{}
		tc.offset = 0
	}
	tc.mu.Unlock()
	if current {
		_ = st.Close()
	}
}

// syncSize resizes the PTY to this view when this client holds the input
// lease and the engine reported another size. It runs as this client takes
// the lease or learns the session, which a resize before it skipped.
func (tc *TerminalConn) syncSize() {
	tc.mu.Lock()
	tg, sess, cols, rows := tc.target, tc.session, tc.cols, tc.rows
	need := !tc.closed && !tc.exited && tg.fn != nil &&
		!tc.heldByOther && tc.leaseHolder == tc.m.clientID &&
		tc.ptyCols > 0 && (tc.ptyCols != cols || tc.ptyRows != rows)
	tc.mu.Unlock()
	if !need {
		return
	}
	ctx, cancel := context.WithTimeout(tc.ctx, 2*time.Second)
	defer cancel()
	err := tg.fn.TerminalResize(ctx, api.TerminalResize{
		Workspace: tc.workspace,
		Session:   sess,
		Client:    tc.m.clientID,
		Cols:      cols,
		Rows:      rows,
	})
	if err == nil {
		tc.mu.Lock()
		if tc.session == sess {
			tc.ptyCols, tc.ptyRows = cols, rows
		}
		tc.mu.Unlock()
	}
}

func (tc *TerminalConn) Resize(cols, rows int) error {
	if cols <= 0 || rows <= 0 {
		return nil
	}
	tc.mu.Lock()
	tc.cols = cols
	tc.rows = rows
	tg := tc.target
	sess := tc.session
	closed := tc.closed || tc.exited
	tc.mu.Unlock()
	if closed || tg.fn == nil || sess.IsZero() {
		return nil
	}
	ctx, cancel := context.WithTimeout(tc.ctx, 5*time.Second)
	defer cancel()
	err := tg.fn.TerminalResize(ctx, api.TerminalResize{
		Workspace: tc.workspace,
		Session:   sess,
		Client:    tc.m.clientID,
		Cols:      cols,
		Rows:      rows,
	})
	if errors.Is(err, api.ErrLeaseHeld) {
		tc.mu.Lock()
		changed := !tc.heldByOther
		tc.heldByOther = true
		tc.mu.Unlock()
		if changed {
			tc.notifyStatus()
		}
		return nil
	}
	return err
}

// TakeControl force-claims the active input lease for this client.
func (tc *TerminalConn) TakeControl() {
	tc.mu.Lock()
	tg := tc.target
	sess := tc.session
	closed := tc.closed || tc.exited
	tc.mu.Unlock()
	if closed || tg.fn == nil {
		return
	}
	tc.m.wg.Go(func() {
		ctx, cancel := context.WithTimeout(tc.ctx, 5*time.Second)
		defer cancel()
		lease, err := tg.fn.TerminalClaim(ctx, api.TerminalClaim{
			Workspace: tc.workspace,
			Session:   sess,
			Client:    tc.m.clientID,
			Force:     true,
		})
		if err != nil {
			return
		}
		tc.mu.Lock()
		tc.leaseHolder = lease.Client
		tc.heldByOther = lease.Client != "" && lease.Client != tc.m.clientID
		tc.mu.Unlock()
		tc.notifyStatus()
		tc.syncSize()
	})
}

// LeaseStatus reports the current lease holder ID and whether another client
// holds the active input lease.
func (tc *TerminalConn) LeaseStatus() (holder string, heldByOther bool) {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	return tc.leaseHolder, tc.heldByOther
}

func (tc *TerminalConn) Close() error {
	tc.cancel()
	tc.mu.Lock()
	tc.closed = true
	st := tc.stream
	tc.stream = nil
	tc.mu.Unlock()
	if st != nil {
		return st.Close()
	}
	return nil
}

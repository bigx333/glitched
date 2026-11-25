package pty

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"glitched/internal/api"
)

const (
	// DefaultReplayBytes is the per-session output replay ring size (4 MiB).
	DefaultReplayBytes = 4 << 20
	// DefaultHighWatermark pauses subscriber delivery once buffered unread output reaches 1 MiB.
	DefaultHighWatermark = 1 << 20
	// DefaultLowWatermark resumes subscriber delivery once buffered unread output drains to 512 KiB.
	DefaultLowWatermark = 512 << 10
	// DefaultLeaseTTL is how long an idle client holds the terminal input lease.
	DefaultLeaseTTL = 30 * time.Second

	chunkSize = 64 << 10
)

// Options configures a PTY Manager.
type Options struct {
	Command       []string
	ReplayBytes   int
	HighWatermark int
	LowWatermark  int
	LeaseTTL      time.Duration
	Now           func() time.Time
}

// Manager owns workspace PTY sessions independent of any attached GUI client.
type Manager struct {
	opts          Options
	replayBytes   int
	highWatermark int
	lowWatermark  int
	leaseTTL      time.Duration
	now           func() time.Time

	mu       sync.Mutex
	sessions map[string]*session
	closed   bool
	wg       sync.WaitGroup
}

// New constructs an engine PTY Manager.
func New(opts Options) *Manager {
	replay := opts.ReplayBytes
	if replay <= 0 {
		replay = DefaultReplayBytes
	}
	high := opts.HighWatermark
	if high <= 0 {
		high = DefaultHighWatermark
	}
	low := opts.LowWatermark
	if low <= 0 || low > high {
		low = DefaultLowWatermark
		if low > high {
			low = high / 2
		}
	}
	ttl := opts.LeaseTTL
	if ttl <= 0 {
		ttl = DefaultLeaseTTL
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Manager{
		opts:          opts,
		replayBytes:   replay,
		highWatermark: high,
		lowWatermark:  low,
		leaseTTL:      ttl,
		now:           now,
		sessions:      map[string]*session{},
	}
}

type session struct {
	mgr       *Manager
	workspace string
	id        api.ID
	proc      proc

	mu          sync.Mutex
	ring        *ringBuf
	cols        int
	rows        int
	leaseClient string
	leaseUntil  time.Time
	exited      bool
	exitCode    int
	subs        map[*subStream]struct{}
	readDone    chan struct{}
}

func normalizeWorkspace(ws string) string {
	ws = strings.TrimSpace(ws)
	if ws == "" || ws == "~" {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			return filepath.Clean(home)
		}
		return "~"
	}
	if strings.HasPrefix(ws, "~/") {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			ws = filepath.Join(home, ws[2:])
		}
	}
	return filepath.Clean(ws)
}

func (m *Manager) effectiveCols(cols int) int {
	if cols <= 0 {
		return 80
	}
	return cols
}

func (m *Manager) effectiveRows(rows int) int {
	if rows <= 0 {
		return 24
	}
	return rows
}

func (m *Manager) OpenTerminal(ctx context.Context, req api.TerminalOpen) (api.TerminalStream, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key := normalizeWorkspace(req.Workspace)
	cols := m.effectiveCols(req.Cols)
	rows := m.effectiveRows(req.Rows)

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, errors.New("pty: manager closed")
	}
	sess := m.sessions[key]
	needSpawn := sess == nil
	if !needSpawn {
		sess.mu.Lock()
		exited := sess.exited
		sameSession := req.Session != (api.ID{}) && req.Session == sess.id
		sess.mu.Unlock()
		if exited && !sameSession {
			needSpawn = true
		}
	}
	if needSpawn {
		id := api.NewID()
		p, err := startProc(key, m.opts.Command, cols, rows)
		if err != nil {
			m.mu.Unlock()
			return nil, err
		}
		sess = &session{
			mgr:       m,
			workspace: key,
			id:        id,
			proc:      p,
			ring:      newRing(m.replayBytes),
			cols:      cols,
			rows:      rows,
			subs:      map[*subStream]struct{}{},
			readDone:  make(chan struct{}),
		}
		m.sessions[key] = sess
		m.wg.Add(1)
		go sess.readLoop()
	}
	m.mu.Unlock()

	sub := &subStream{
		sess: sess,
		wake: make(chan struct{}, 1),
		done: make(chan struct{}),
	}

	now := m.now()
	sess.mu.Lock()
	defer sess.mu.Unlock()

	after := req.After
	if req.Session != (api.ID{}) && req.Session != sess.id {
		after = 0
	} else if req.Session == (api.ID{}) && after > sess.ring.end() {
		after = 0
	}

	if !sess.exited && req.Client != "" && (sess.leaseClient == "" || !now.Before(sess.leaseUntil)) {
		prev := sess.leaseClient
		sess.leaseClient = req.Client
		sess.leaseUntil = now.Add(m.leaseTTL)
		if prev != req.Client {
			for other := range sess.subs {
				other.notifyLeaseLocked(sess)
			}
		}
	}

	sess.subs[sub] = struct{}{}
	data, end, truncated := sess.ring.readFrom(after)
	startOff := end - uint64(len(data))
	sub.resumeOffset = startOff
	if len(data) == 0 {
		sub.pushItemLocked(api.TerminalItem{
			Session:     sess.id,
			Offset:      end,
			Truncated:   truncated,
			Cols:        sess.cols,
			Rows:        sess.rows,
			LeaseClient: sess.leaseClient,
		})
	} else {
		sub.enqueueBytesLocked(data, startOff, truncated, true, sess.cols, sess.rows, sess.leaseClient)
	}
	if sess.exited && !sub.paused && sub.resumeOffset >= sess.ring.end() && !sub.sentExit {
		sub.sentExit = true
		sub.pushItemLocked(api.TerminalItem{
			Session:     sess.id,
			Offset:      sess.ring.end(),
			Cols:        sess.cols,
			Rows:        sess.rows,
			LeaseClient: sess.leaseClient,
			Exited:      true,
			ExitCode:    sess.exitCode,
		})
	}
	sub.signalLocked()
	return sub, nil
}

func (m *Manager) lookupLive(workspace string, sessID api.ID) (*session, error) {
	key := normalizeWorkspace(workspace)
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, api.ErrNotFound
	}
	sess := m.sessions[key]
	m.mu.Unlock()
	if sess == nil {
		return nil, api.ErrNotFound
	}
	sess.mu.Lock()
	if sess.exited || (sessID != (api.ID{}) && sessID != sess.id) {
		sess.mu.Unlock()
		return nil, api.ErrNotFound
	}
	return sess, nil
}

func (s *session) checkOrRenewLeaseLocked(client string, now time.Time) bool {
	if client == "" {
		return true
	}
	if s.leaseClient != "" && s.leaseClient != client && now.Before(s.leaseUntil) {
		return false
	}
	prev := s.leaseClient
	s.leaseClient = client
	s.leaseUntil = now.Add(s.mgr.leaseTTL)
	if prev != client {
		for sub := range s.subs {
			sub.notifyLeaseLocked(s)
		}
	}
	return true
}

func (m *Manager) TerminalInput(ctx context.Context, req api.TerminalInput) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	sess, err := m.lookupLive(req.Workspace, req.Session)
	if err != nil {
		return err
	}
	if !sess.checkOrRenewLeaseLocked(req.Client, m.now()) {
		sess.mu.Unlock()
		return api.ErrLeaseHeld
	}
	p := sess.proc
	sess.mu.Unlock()

	if len(req.Data) == 0 {
		return nil
	}
	_, err = p.Write(req.Data)
	return err
}

func (m *Manager) TerminalResize(ctx context.Context, req api.TerminalResize) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	sess, err := m.lookupLive(req.Workspace, req.Session)
	if err != nil {
		return err
	}
	if !sess.checkOrRenewLeaseLocked(req.Client, m.now()) {
		sess.mu.Unlock()
		return api.ErrLeaseHeld
	}
	cols := max(req.Cols, 1)
	rows := max(req.Rows, 1)
	changed := sess.cols != cols || sess.rows != rows
	sess.cols = cols
	sess.rows = rows
	if changed {
		for sub := range sess.subs {
			sub.notifyLeaseLocked(sess)
		}
	}
	p := sess.proc
	sess.mu.Unlock()

	return p.Resize(cols, rows)
}

func (m *Manager) TerminalClaim(ctx context.Context, req api.TerminalClaim) (api.TerminalLease, error) {
	if err := ctx.Err(); err != nil {
		return api.TerminalLease{}, err
	}
	sess, err := m.lookupLive(req.Workspace, req.Session)
	if err != nil {
		return api.TerminalLease{}, err
	}
	defer sess.mu.Unlock()

	now := m.now()
	if !req.Force && sess.leaseClient != "" && sess.leaseClient != req.Client && now.Before(sess.leaseUntil) {
		return api.TerminalLease{
			Session: sess.id,
			Client:  sess.leaseClient,
			Until:   sess.leaseUntil,
		}, api.ErrLeaseHeld
	}
	prev := sess.leaseClient
	sess.leaseClient = req.Client
	sess.leaseUntil = now.Add(m.leaseTTL)
	if prev != req.Client {
		for sub := range sess.subs {
			sub.notifyLeaseLocked(sess)
		}
	}
	return api.TerminalLease{
		Session: sess.id,
		Client:  sess.leaseClient,
		Until:   sess.leaseUntil,
	}, nil
}

// ReleaseTerminalClient releases any active input lease held by client across
// all live sessions (for instance when a transport connection closes or is revoked).
func (m *Manager) ReleaseTerminalClient(client string) {
	if client == "" {
		return
	}
	m.mu.Lock()
	var list []*session
	for _, s := range m.sessions {
		list = append(list, s)
	}
	m.mu.Unlock()

	for _, s := range list {
		s.mu.Lock()
		if !s.exited && s.leaseClient == client {
			s.leaseClient = ""
			s.leaseUntil = time.Time{}
			for sub := range s.subs {
				sub.notifyLeaseLocked(s)
			}
		}
		s.mu.Unlock()
	}
}

// SessionID returns the active session ID for workspace, if any.
func (m *Manager) SessionID(workspace string) api.ID {
	key := normalizeWorkspace(workspace)
	m.mu.Lock()
	sess := m.sessions[key]
	m.mu.Unlock()
	if sess == nil {
		return api.ID{}
	}
	return sess.id
}

// Close shuts down all active PTY sessions and waits for their readers to exit.
func (m *Manager) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		m.wg.Wait()
		return nil
	}
	m.closed = true
	var list []*session
	for _, s := range m.sessions {
		list = append(list, s)
	}
	m.mu.Unlock()

	var errs []error
	for _, s := range list {
		if err := s.proc.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	m.wg.Wait()
	return errors.Join(errs...)
}

func (s *session) readLoop() {
	defer s.mgr.wg.Done()
	defer close(s.readDone)

	buf := make([]byte, chunkSize)
	for {
		n, err := s.proc.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			s.mu.Lock()
			end := s.ring.write(chunk)
			start := end - uint64(len(chunk))
			for sub := range s.subs {
				if sub.paused {
					continue
				}
				sub.enqueueBytesLocked(chunk, start, false, false, s.cols, s.rows, s.leaseClient)
				sub.signalLocked()
			}
			s.mu.Unlock()
		}
		if err != nil {
			break
		}
	}

	code, _ := s.proc.Wait()
	s.mu.Lock()
	s.exited = true
	s.exitCode = code
	end := s.ring.end()
	for sub := range s.subs {
		if !sub.paused && sub.resumeOffset >= end && !sub.sentExit {
			sub.sentExit = true
			sub.pushItemLocked(api.TerminalItem{
				Session:     s.id,
				Offset:      end,
				Cols:        s.cols,
				Rows:        s.rows,
				LeaseClient: s.leaseClient,
				Exited:      true,
				ExitCode:    code,
			})
			sub.signalLocked()
		}
	}
	s.mu.Unlock()
}

type subStream struct {
	sess         *session
	items        []api.TerminalItem
	queuedBytes  int
	paused       bool
	resumeOffset uint64
	sentExit     bool
	eof          bool
	closed       bool
	wake         chan struct{}
	done         chan struct{}
}

func (sub *subStream) signalLocked() {
	select {
	case sub.wake <- struct{}{}:
	default:
	}
}

func (sub *subStream) pushItemLocked(it api.TerminalItem) {
	sub.items = append(sub.items, it)
	sub.queuedBytes += len(it.Data)
}

func (sub *subStream) notifyLeaseLocked(s *session) {
	if sub.paused || sub.closed || sub.sentExit {
		return
	}
	sub.pushItemLocked(api.TerminalItem{
		Session:     s.id,
		Offset:      sub.resumeOffset,
		Cols:        s.cols,
		Rows:        s.rows,
		LeaseClient: s.leaseClient,
	})
	sub.signalLocked()
}

func (sub *subStream) enqueueBytesLocked(p []byte, startOff uint64, truncated bool, withMeta bool, cols, rows int, leaseClient string) {
	high := sub.sess.mgr.highWatermark
	stepCap := min(chunkSize, max(1, high/2))
	first := true
	for len(p) > 0 {
		avail := high - sub.queuedBytes
		if avail <= 0 {
			sub.paused = true
			return
		}
		n := min(len(p), min(stepCap, avail))
		cp := make([]byte, n)
		copy(cp, p[:n])
		startOff += uint64(n)
		sub.resumeOffset = startOff
		it := api.TerminalItem{
			Session: sub.sess.id,
			Offset:  startOff,
			Data:    cp,
		}
		if first {
			it.Truncated = truncated
			if withMeta {
				it.Cols = cols
				it.Rows = rows
				it.LeaseClient = leaseClient
			}
			first = false
		}
		sub.pushItemLocked(it)
		p = p[n:]
		if sub.queuedBytes >= high {
			sub.paused = true
			return
		}
	}
}

func (sub *subStream) Next(ctx context.Context) (api.TerminalItem, error) {
	s := sub.sess
	for {
		s.mu.Lock()
		if sub.closed {
			s.mu.Unlock()
			return api.TerminalItem{}, io.EOF
		}
		if len(sub.items) > 0 {
			it := sub.items[0]
			sub.items[0] = api.TerminalItem{}
			sub.items = sub.items[1:]
			if len(sub.items) == 0 {
				sub.items = nil
			}
			sub.queuedBytes -= len(it.Data)
			if sub.paused && sub.queuedBytes <= s.mgr.lowWatermark {
				sub.paused = false
				tail, tailEnd, truncated := s.ring.readFrom(sub.resumeOffset)
				if truncated {
					sub.resumeOffset = tailEnd - uint64(len(tail))
				}
				if len(tail) > 0 {
					sub.enqueueBytesLocked(tail, sub.resumeOffset, truncated, true, s.cols, s.rows, s.leaseClient)
				} else if truncated {
					sub.pushItemLocked(api.TerminalItem{
						Session:     s.id,
						Offset:      tailEnd,
						Truncated:   true,
						Cols:        s.cols,
						Rows:        s.rows,
						LeaseClient: s.leaseClient,
					})
					sub.resumeOffset = tailEnd
				}
				if s.exited && !sub.paused && sub.resumeOffset >= tailEnd && !sub.sentExit {
					sub.sentExit = true
					sub.pushItemLocked(api.TerminalItem{
						Session:     s.id,
						Offset:      tailEnd,
						Cols:        s.cols,
						Rows:        s.rows,
						LeaseClient: s.leaseClient,
						Exited:      true,
						ExitCode:    s.exitCode,
					})
				}
			}
			if it.Exited {
				sub.eof = true
			}
			s.mu.Unlock()
			return it, nil
		}
		if sub.eof {
			s.mu.Unlock()
			return api.TerminalItem{}, io.EOF
		}
		s.mu.Unlock()

		select {
		case <-sub.wake:
		case <-sub.done:
			return api.TerminalItem{}, io.EOF
		case <-ctx.Done():
			return api.TerminalItem{}, ctx.Err()
		}
	}
}

func (sub *subStream) Close() error {
	s := sub.sess
	s.mu.Lock()
	if !sub.closed {
		sub.closed = true
		sub.items = nil
		sub.queuedBytes = 0
		delete(s.subs, sub)
		close(sub.done)
	}
	s.mu.Unlock()
	return nil
}

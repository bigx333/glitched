package claude

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"

	"glitched/internal/provider"
)

var errSessionInUse = errors.New("claude: the session is in use")

// closeGrace is how long Close lets Claude Code end on its own once stdin
// closes, before it kills the process.
var closeGrace = 2 * time.Second

const initRequest = "glitched-init"

// Agent is one run's claude process. One goroutine reads its stdout into
// lines; Next alone translates them, so the turn needs no lock.
type Agent struct {
	cmd     *exec.Cmd
	stderr  *tail
	lines   chan []byte   // stdout, closed once the process is gone
	gone    chan struct{} // closed once the process is gone
	exitErr error         // set before gone closes
	done    chan struct{} // closed by Close
	close   sync.Once

	turn    *turn
	pending []provider.Event

	mu      sync.Mutex
	stdin   io.WriteCloser
	started bool
}

var _ provider.Agent = (*Agent)(nil)

// start runs claude with args and waits for its answer to initialize, which
// it gives only after it has checked the session flag.
func start(ctx context.Context, bin string, spec provider.SessionSpec, args []string) (*Agent, error) {
	// Open's ctx bounds only the start; the process lives until Close.
	cmd := Command(context.Background(), bin, args...)
	cmd.Dir = spec.Workspace
	cmd.WaitDelay = time.Second
	g := &Agent{
		cmd: cmd, stderr: &tail{},
		lines: make(chan []byte), gone: make(chan struct{}), done: make(chan struct{}),
		turn: newTurn(spec.Workspace),
	}
	cmd.Stderr = g.stderr
	var err error
	if g.stdin, err = cmd.StdinPipe(); err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start Claude Code: %w", err)
	}
	go g.read(stdout)
	if err := g.handshake(ctx); err != nil {
		g.Close(context.Background())
		return nil, err
	}
	return g, nil
}

func (g *Agent) read(stdout io.Reader) {
	r := bufio.NewReader(stdout)
	for {
		line, err := r.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			select {
			case g.lines <- line:
			case <-g.done:
			}
		}
		if err != nil {
			break
		}
	}
	g.exitErr = g.cmd.Wait()
	close(g.gone)
	close(g.lines)
}

func (g *Agent) handshake(ctx context.Context) error {
	// A process that already failed refuses the write; its exit says why.
	g.send(map[string]any{"type": "control_request", "request_id": initRequest, "request": map[string]string{"subtype": "initialize"}})
	for {
		select {
		case line, ok := <-g.lines:
			if !ok {
				return g.exited()
			}
			var m message
			if json.Unmarshal(line, &m) != nil || m.Type != "control_response" || m.Response == nil || m.Response.RequestID != initRequest {
				continue
			}
			if m.Response.Subtype != "success" {
				return fmt.Errorf("Claude Code refused to start: %s", m.Response.Error)
			}
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// exited is why the process is gone, from what it printed last.
func (g *Agent) exited() error {
	said := strings.TrimSpace(g.stderr.String())
	if strings.Contains(said, "is already in use") {
		return errSessionInUse
	}
	if said == "" && g.exitErr != nil {
		said = g.exitErr.Error()
	}
	return fmt.Errorf("Claude Code exited: %s", cmp.Or(said, "no reason given"))
}

func (g *Agent) Do(_ context.Context, act provider.Action) error {
	s, ok := act.(provider.Start)
	if !ok {
		return provider.ErrUnsupported
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.started {
		return errors.New("claude: the run already started")
	}
	g.started = true
	return g.sendLocked(map[string]any{"type": "user", "session_id": "", "parent_tool_use_id": nil,
		"message": map[string]string{"role": "user", "content": s.Text}})
}

func (g *Agent) Next(ctx context.Context) (provider.Event, error) {
	for len(g.pending) == 0 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		select {
		case line, ok := <-g.lines:
			if !ok {
				return nil, g.exited()
			}
			var m message
			if json.Unmarshal(line, &m) == nil {
				g.pending = g.turn.read(m)
			}
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	e := g.pending[0]
	g.pending = g.pending[1:]
	return e, nil
}

// Close ends stdin, which ends Claude Code once it is idle, and kills a
// process that is still busy after closeGrace.
func (g *Agent) Close(ctx context.Context) error {
	g.close.Do(func() {
		close(g.done)
		g.mu.Lock()
		g.stdin.Close()
		g.mu.Unlock()
	})
	select {
	case <-g.gone:
		return nil
	case <-time.After(closeGrace):
	case <-ctx.Done():
	}
	g.cmd.Process.Kill()
	select {
	case <-g.gone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (g *Agent) send(v any) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.sendLocked(v)
}

func (g *Agent) sendLocked(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = g.stdin.Write(append(b, '\n'))
	return err
}

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

func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}

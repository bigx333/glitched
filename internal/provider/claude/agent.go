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
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"glitched/internal/provider"
)

var errSessionInUse = errors.New("claude: the session is in use")

// closeGrace is how long Close lets Claude Code end on its own once stdin
// closes, before it asks the process to stop.
var closeGrace = 2 * time.Second

// drainGrace is how long, once claude has exited, its output may stay
// quiet before the adapter stops reading it: a process claude left behind
// can hold the pipes open for good.
const drainGrace = 250 * time.Millisecond

const initRequest = "glitched-init"

// Agent is one run's claude process. One goroutine reads its stdout into
// lines; Next alone translates them, so the turn needs no lock.
type Agent struct {
	cmd     *exec.Cmd
	stop    context.CancelFunc // asks claude to stop, as Command says
	stderr  *tail
	lines   chan []byte   // stdout, closed once the process is gone and its output read
	reaped  chan struct{} // closed once the process is reaped
	exitErr error         // set before reaped closes
	done    chan struct{} // closed by Close
	close   sync.Once

	turn    *turn
	pending []provider.Event

	mu      sync.Mutex // one write to stdin at a time
	stdin   *os.File
	cut     error // set once a line went out cut short, which fails every later write
	started bool

	pmu     sync.Mutex
	prompts map[string]*prompt // by Claude Code's request id
}

var _ provider.Agent = (*Agent)(nil)

// start runs claude with args and waits for its answer to initialize, which
// it gives only after it has checked the session flag.
func start(ctx context.Context, bin string, spec provider.SessionSpec, args []string) (*Agent, error) {
	// Open's ctx bounds only the start; the process lives until Close.
	life, stop := context.WithCancel(context.Background())
	cmd := Command(life, bin, args...)
	cmd.Dir = spec.Workspace
	g := &Agent{
		cmd: cmd, stop: stop, stderr: &tail{},
		lines: make(chan []byte), reaped: make(chan struct{}), done: make(chan struct{}),
		turn: newTurn(spec.Workspace), prompts: map[string]*prompt{},
	}
	// The adapter's own pipes, unlike exec's, leave Wait nothing to wait
	// for once claude exits, whoever else still holds them.
	inR, inW, err1 := os.Pipe()
	outR, outW, err2 := os.Pipe()
	errR, errW, err3 := os.Pipe()
	if err := errors.Join(err1, err2, err3); err != nil {
		for _, f := range []*os.File{inR, inW, outR, outW, errR, errW} {
			f.Close()
		}
		stop()
		return nil, err
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = inR, outW, errW
	err := cmd.Start()
	inR.Close()
	outW.Close()
	errW.Close()
	if err != nil {
		inW.Close()
		outR.Close()
		errR.Close()
		stop()
		return nil, fmt.Errorf("start Claude Code: %w", err)
	}
	g.stdin = inW
	go g.watch(outR, errR)
	if err := g.handshake(ctx); err != nil {
		g.Close(context.Background())
		return nil, err
	}
	return g, nil
}

// watch reaps claude as it exits, and reads its output until it ends or
// has been quiet for drainGrace since claude exited.
func (g *Agent) watch(stdout, stderr *os.File) {
	go func() {
		g.exitErr = g.cmd.Wait()
		g.stop()
		close(g.reaped)
		stdout.SetReadDeadline(time.Now().Add(drainGrace))
		stderr.SetReadDeadline(time.Now().Add(drainGrace))
	}()
	stderrRead := make(chan struct{})
	go func() {
		io.Copy(g.stderr, drain{stderr, g.reaped})
		stderr.Close()
		close(stderrRead)
	}()
	r := bufio.NewReader(drain{stdout, g.reaped})
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
	stdout.Close()
	<-g.reaped
	<-stderrRead
	close(g.lines)
}

// drain reads a pipe of claude's. Once claude has exited, each read gives
// up after drainGrace with nothing to read.
type drain struct {
	f      *os.File
	reaped <-chan struct{}
}

func (d drain) Read(p []byte) (int, error) {
	select {
	case <-d.reaped:
		d.f.SetReadDeadline(time.Now().Add(drainGrace))
	default:
	}
	return d.f.Read(p)
}

func (g *Agent) handshake(ctx context.Context) error {
	// A process that already failed refuses the write; its exit says why.
	g.send(ctx, map[string]any{"type": "control_request", "request_id": initRequest, "request": map[string]string{"subtype": "initialize"}})
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

func (g *Agent) Do(ctx context.Context, act provider.Action) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	switch a := act.(type) {
	case provider.Start:
		if g.started {
			return errors.New("claude: the run already started")
		}
		g.started = true
		return g.sendLocked(ctx, map[string]any{"type": "user", "session_id": "", "parent_tool_use_id": nil,
			"message": map[string]string{"role": "user", "content": a.Text}})
	case provider.Respond:
		return g.respondLocked(ctx, a)
	}
	return provider.ErrUnsupported
}

// respondLocked sends the one reply a prompt gets. A prompt already
// answered takes nothing more, and one Claude Code stopped waiting on
// takes no answer.
func (g *Agent) respondLocked(ctx context.Context, a provider.Respond) error {
	g.pmu.Lock()
	p, ok := g.prompts[a.Request]
	state := asking
	if ok {
		state = p.state
	}
	g.pmu.Unlock()
	switch {
	case !ok:
		return errNoPrompt
	case state == answered:
		return nil
	case state == gone:
		return errGone
	}
	reply, err := p.reply(a.Request, a.Answer)
	if err != nil {
		return err
	}
	if err := g.sendLocked(ctx, reply); err != nil {
		return err
	}
	g.pmu.Lock()
	p.state = answered
	g.pmu.Unlock()
	return nil
}

func (g *Agent) Next(ctx context.Context) (provider.Event, error) {
	for len(g.pending) == 0 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		select {
		case line, ok := <-g.lines:
			if !ok {
				if g.pending = g.turn.unfinished(); len(g.pending) > 0 {
					continue
				}
				return nil, g.exited()
			}
			var m message
			if json.Unmarshal(line, &m) == nil {
				g.pending = g.read(ctx, m)
			}
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	e := g.pending[0]
	g.pending = g.pending[1:]
	return e, nil
}

// read is what a message means for the run: the control messages that
// open and close prompts, and the turn's own.
func (g *Agent) read(ctx context.Context, m message) []provider.Event {
	switch m.Type {
	case "control_request":
		if m.Request == nil {
			return nil
		}
		if m.Request.Subtype != "can_use_tool" {
			g.send(ctx, refusal(m.RequestID, m.Request.Subtype))
			return nil
		}
		g.pmu.Lock()
		defer g.pmu.Unlock()
		if _, seen := g.prompts[m.RequestID]; seen {
			return nil
		}
		e, p := g.turn.ask(m.RequestID, m.Request.canUseTool)
		g.prompts[m.RequestID] = p
		return []provider.Event{e}
	case "control_cancel_request":
		g.pmu.Lock()
		defer g.pmu.Unlock()
		if p := g.prompts[m.RequestID]; p != nil && p.state == asking {
			p.state = gone
			return []provider.Event{provider.RequestWithdrawn{ID: m.RequestID, Reason: withdrawnReason}}
		}
		return nil
	case "result":
		g.pmu.Lock()
		for _, p := range g.prompts {
			if p.state == asking {
				p.state = gone
			}
		}
		g.pmu.Unlock()
	}
	return g.turn.read(m)
}

// Close ends stdin, which ends Claude Code once it is idle, and asks a
// process still busy after closeGrace, or when ctx ends first, to stop.
func (g *Agent) Close(ctx context.Context) error {
	g.close.Do(func() {
		close(g.done)
		g.stdin.Close()
	})
	select {
	case <-g.reaped:
		return nil
	case <-time.After(closeGrace):
	case <-ctx.Done():
	}
	g.stop()
	select {
	case <-g.reaped:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (g *Agent) send(ctx context.Context, v any) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.sendLocked(ctx, v)
}

// sendLocked writes v as a line to claude's stdin, and gives up when ctx
// ends. A line cut short leaves the stream broken, so every later write
// fails too.
func (g *Agent) sendLocked(ctx context.Context, v any) error {
	if g.cut != nil {
		return g.cut
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	line := append(b, '\n')
	expired := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		g.stdin.SetWriteDeadline(time.Now())
		close(expired)
	})
	n, err := g.stdin.Write(line)
	if !stop() {
		<-expired
	}
	g.stdin.SetWriteDeadline(time.Time{})
	if err == nil {
		return nil
	}
	if errors.Is(err, os.ErrDeadlineExceeded) {
		err = cmp.Or(ctx.Err(), err)
	}
	if n > 0 {
		g.cut = fmt.Errorf("claude: a line to Claude Code was cut short: %w", err)
	}
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

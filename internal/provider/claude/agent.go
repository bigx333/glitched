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
	"maps"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"glitched/internal/api"
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

const (
	initRequest      = "glitched-init"
	interruptRequest = "glitched-interrupt"
)

// cancelQueued is the capability of an interrupt that cancels the messages
// Claude Code queued. Claude Code 2.1.317 lists it in its system init, not
// in its answer to initialize.
const cancelQueued = "interrupt_cancel_queued_v1"

// interruptGrace is how long Claude Code has to end the turn once asked to
// interrupt it, before it is stopped.
var interruptGrace = 10 * time.Second

// Agent is one run's claude process. One goroutine reads its stdout into
// lines; Next alone translates them, so the turn needs no lock.
type Agent struct {
	cmd     *exec.Cmd
	run     string             // the run's id, its processes' marker
	stop    context.CancelFunc // asks claude to stop, as Command says
	stderr  *tail
	lines   chan []byte   // stdout, closed once the process is gone and its output read
	reaped  chan struct{} // closed once the process is reaped
	exitErr error         // set before reaped closes
	done    chan struct{} // closed by Close
	close   sync.Once

	turn    *turn
	pending []provider.Event

	mu          sync.Mutex // one write to stdin at a time, and the turn's messages
	stdin       *os.File
	cut         error           // set once a line went out cut short, which fails every later write
	started     bool            // the turn's first message went out
	outstanding map[string]bool // messages sent, by uuid, that no result has finished yet, true for a steer
	cancels     bool            // an interrupt can cancel the queued messages
	interrupted bool            // an interrupt went out, or claude was stopped in its place
	ended       bool            // the turn's end was read
	deadline    *time.Timer     // stops claude if an interrupt does not end the turn

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
	cmd.Env = append(slices.DeleteFunc(cmd.Env, func(kv string) bool { return strings.HasPrefix(kv, runEnv+"=") }),
		runEnv+"="+spec.Run.String())
	g := &Agent{
		cmd: cmd, run: spec.Run.String(), stop: stop, stderr: &tail{},
		lines: make(chan []byte), reaped: make(chan struct{}), done: make(chan struct{}),
		turn: newTurn(spec.Workspace), prompts: map[string]*prompt{}, outstanding: map[string]bool{},
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
	started := make(chan error)
	go g.supervise(started)
	err := <-started
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

// supervise starts claude, says how that went, and reaps it, all on one OS
// thread: Linux sends claude its parent-death signal when the thread that
// started it exits, which must be when the engine does.
func (g *Agent) supervise(started chan<- error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	err := g.cmd.Start()
	started <- err
	if err != nil {
		return
	}
	g.exitErr = g.cmd.Wait()
	g.stop()
	close(g.reaped)
}

// watch reads claude's output until it ends or has been quiet for
// drainGrace since claude exited.
func (g *Agent) watch(stdout, stderr *os.File) {
	go func() {
		<-g.reaped
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
			g.learn(m.Response.Response.Capabilities)
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
		return g.userLocked(ctx, a.Text, false)
	case provider.Steer:
		switch {
		case !g.started:
			return errors.New("claude: the run has not started")
		case g.ended || g.interrupted:
			return errors.New("claude: the turn is over")
		}
		return g.userLocked(ctx, a.Text, true)
	case provider.Interrupt:
		if g.interrupted || g.ended {
			return nil
		}
		// Claude Code takes a message the stopped turn did not get to as a
		// turn of its own, 18 ms after the turn's result, unless the
		// interrupt cancels it. One that can't cancel it is stopped instead.
		if !g.cancels && slices.Contains(slices.Collect(maps.Values(g.outstanding)), true) {
			g.interrupted = true
			g.stop()
			return nil
		}
		err := g.sendLocked(ctx, map[string]any{"type": "control_request", "request_id": interruptRequest,
			"request": map[string]any{"subtype": "interrupt", "cancel_queued": true}})
		if err == nil {
			g.interrupted = true
			g.deadline = time.AfterFunc(interruptGrace, g.stop)
		}
		return err
	case provider.Respond:
		return g.respondLocked(ctx, a)
	}
	return provider.ErrUnsupported
}

// userLocked sends a user message. Claude Code takes one sent mid-turn at
// its next model call, and lists each message a result finishes by uuid.
func (g *Agent) userLocked(ctx context.Context, text string, steer bool) error {
	id := uuidOf(api.NewID())
	g.outstanding[id] = steer
	err := g.sendLocked(ctx, map[string]any{"type": "user", "uuid": id, "session_id": "", "parent_tool_use_id": nil,
		"message": map[string]string{"role": "user", "content": text}})
	if err != nil {
		delete(g.outstanding, id)
	}
	return err
}

func (g *Agent) learn(capabilities []string) {
	if slices.Contains(capabilities, cancelQueued) {
		g.mu.Lock()
		g.cancels = true
		g.mu.Unlock()
	}
}

// over reads a result into the turn's messages, and reports whether it
// ends the turn: it finished every message sent, or an interrupt went out.
// A result that lists no messages finishes them all. The interrupt cut
// the turn short unless Claude Code says it completed the turn first.
func (g *Agent) over(m message) (over, interrupted bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if m.UserMessageUUIDs == nil {
		clear(g.outstanding)
	}
	for _, id := range m.UserMessageUUIDs {
		delete(g.outstanding, id)
	}
	g.ended = g.interrupted || len(g.outstanding) == 0
	if g.ended && g.deadline != nil {
		g.deadline.Stop()
	}
	return g.ended, g.interrupted && m.IsError
}

// cutOff is whether an interrupt went out on a turn that has not ended,
// which it now has.
func (g *Agent) cutOff() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	cut := g.interrupted && !g.ended
	g.ended = true
	return cut
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
		return provider.ErrWithdrawn
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
				if g.cutOff() {
					g.pending = []provider.Event{provider.TurnEnded{Outcome: api.OutcomeInterrupted}}
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
		over, interrupted := g.over(m)
		if !over {
			g.turn.tally(m)
			return nil
		}
		g.turn.interrupted = interrupted
	case "system":
		if m.Subtype == "init" {
			g.learn(m.Capabilities)
		}
	}
	return g.turn.read(m)
}

// Close ends stdin, which ends Claude Code once it is idle, and asks a
// process still busy after closeGrace, or when ctx ends first, to stop.
// Once claude is reaped, whatever it left running is stopped too.
func (g *Agent) Close(ctx context.Context) error {
	g.close.Do(func() {
		close(g.done)
		g.stdin.Close()
	})
	select {
	case <-g.reaped:
		_, err := stopRun(ctx, g.run)
		return err
	case <-time.After(closeGrace):
	case <-ctx.Done():
	}
	g.stop()
	select {
	case <-g.reaped:
		_, err := stopRun(ctx, g.run)
		return err
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

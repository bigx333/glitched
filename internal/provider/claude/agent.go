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
	"log/slog"
	"maps"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"glitched/internal/api"
	"glitched/internal/provider"
	"glitched/internal/provider/park"
)

var (
	errSessionInUse = errors.New("claude: the session is in use")
	errNoSession    = errors.New("claude: the session does not exist")
)

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

// proc is one claude process. It runs one thread's turns, an Agent at a
// time, and waits parked between them. One goroutine reads its stdout into
// lines.
type proc struct {
	cmd     *exec.Cmd
	mark    mark               // its processes' mark
	stop    context.CancelFunc // asks claude to stop, as Command says
	halted  atomic.Bool        // stop was called: it takes no more runs
	stderr  *tail
	lines   chan []byte   // stdout, closed once the process is gone and its output read
	reaped  chan struct{} // closed once the process is reaped
	exitErr error         // set before reaped closes
	done    chan struct{} // closed by Close
	close   sync.Once

	mu      sync.Mutex // one write to stdin at a time
	stdin   *os.File
	cut     error // set once a line went out cut short, which fails every later write
	cancels bool  // an interrupt can cancel the queued messages

	wake chan struct{} // closed by Wake, to end the parked watch
	woke chan bool     // what the parked watch found: true when claude kept quiet
}

var _ park.Process = (*proc)(nil)

// Agent is one run on a claude process. Next alone translates the
// process's lines, so the turn needs no lock.
type Agent struct {
	p    *proc
	keep func(*proc) // parks p for the thread's next run
	once sync.Once
	kept bool

	turn    *turn
	pending []provider.Event

	mu          sync.Mutex      // the turn's messages; taken before p.mu
	started     bool            // the turn's first message went out
	outstanding map[string]bool // messages sent, by uuid, that no result has finished yet, true for a steer
	interrupted bool            // an interrupt went out, or claude was stopped in its place
	ended       bool            // the turn's end was read
	deadline    *time.Timer     // stops claude if an interrupt does not end the turn

	// pmu is taken after mu, never before: a path that holds pmu sends
	// nothing, as a send takes mu.
	pmu     sync.Mutex
	prompts map[string]*prompt // by Claude Code's request id
}

var _ provider.Agent = (*Agent)(nil)

func newAgent(spec provider.SessionSpec, p *proc, keep func(*proc)) *Agent {
	return &Agent{p: p, keep: keep, turn: newTurn(spec.Workspace), prompts: map[string]*prompt{}, outstanding: map[string]bool{}}
}

// launch runs claude with args for spec's thread and waits for its answer
// to initialize, which it gives only after it has checked the session flag.
func launch(ctx context.Context, bin string, spec provider.SessionSpec, args []string) (*proc, error) {
	// Open's ctx bounds only the start; the process lives until Close.
	life, stop := context.WithCancel(context.Background())
	cmd := Command(life, bin, args...)
	cmd.Dir = spec.Workspace
	m := launchMark(spec.Thread, api.NewID())
	cmd.Env = append(slices.DeleteFunc(cmd.Env, func(kv string) bool { return strings.HasPrefix(kv, procEnv+"=") }),
		procEnv+"="+string(m))
	contain(cmd, m)
	p := &proc{
		cmd: cmd, mark: m, stop: stop, stderr: &tail{},
		lines: make(chan []byte), reaped: make(chan struct{}), done: make(chan struct{}),
	}
	// The adapter's own pipes, unlike exec's, leave Wait nothing to wait
	// for once claude exits, whoever else still holds them.
	inR, inW, err1 := pipe(true)
	outW, outR, err2 := pipe(false)
	errW, errR, err3 := pipe(false)
	if err := errors.Join(err1, err2, err3); err != nil {
		for _, f := range []*os.File{inR, inW, outR, outW, errR, errW} {
			f.Close()
		}
		stop()
		return nil, err
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = inR, outW, errW
	started := make(chan error)
	go p.supervise(started)
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
	p.stdin = inW
	go p.watch(outR, errR)
	if err := p.handshake(ctx); err != nil {
		p.Close(context.Background())
		return nil, err
	}
	return p, nil
}

// supervise starts claude, says how that went, and reaps it, all on one OS
// thread: Linux sends claude its parent-death signal when the thread that
// started it exits, which must be when the engine does.
func (p *proc) supervise(started chan<- error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	err := p.cmd.Start()
	if err == nil {
		if err = confine(p.cmd, p.mark); err != nil {
			p.cmd.Process.Kill()
			p.cmd.Wait()
		}
	}
	started <- err
	if err != nil {
		return
	}
	p.exitErr = p.cmd.Wait()
	p.stop()
	close(p.reaped)
}

// watch reads claude's output until it ends or has been quiet for
// drainGrace since claude exited.
func (p *proc) watch(stdout, stderr *os.File) {
	go func() {
		<-p.reaped
		stdout.SetReadDeadline(time.Now().Add(drainGrace))
		stderr.SetReadDeadline(time.Now().Add(drainGrace))
	}()
	stderrRead := make(chan struct{})
	go func() {
		io.Copy(p.stderr, drain{stderr, p.reaped})
		stderr.Close()
		close(stderrRead)
	}()
	r := bufio.NewReader(drain{stdout, p.reaped})
	for {
		line, err := r.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			select {
			case p.lines <- line:
			case <-p.done:
			}
		}
		if err != nil {
			break
		}
	}
	stdout.Close()
	<-p.reaped
	<-stderrRead
	close(p.lines)
}

// drain reads a pipe of claude's. Once claude has exited, each read gives
// up after drainGrace with nothing to read.
type drain struct {
	f      *os.File
	reaped <-chan struct{}
}

func (d drain) Read(b []byte) (int, error) {
	select {
	case <-d.reaped:
		d.f.SetReadDeadline(time.Now().Add(drainGrace))
	default:
	}
	return d.f.Read(b)
}

func (p *proc) handshake(ctx context.Context) error {
	// A process that already failed refuses the write; its exit says why.
	p.send(ctx, map[string]any{"type": "control_request", "request_id": initRequest, "request": map[string]string{"subtype": "initialize"}})
	for {
		select {
		case line, ok := <-p.lines:
			if !ok {
				return p.exited()
			}
			var m message
			if json.Unmarshal(line, &m) != nil || m.Type != "control_response" || m.Response == nil || m.Response.RequestID != initRequest {
				continue
			}
			if m.Response.Subtype != "success" {
				return fmt.Errorf("Claude Code refused to start: %s", m.Response.Error)
			}
			p.learn(m.Response.Response.Capabilities)
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// exited is why the process is gone, from what it printed last.
func (p *proc) exited() error {
	said := strings.TrimSpace(p.stderr.String())
	switch {
	case strings.Contains(said, "is already in use"):
		return errSessionInUse
	case strings.Contains(said, "No conversation found"):
		return errNoSession
	}
	if said == "" && p.exitErr != nil {
		said = p.exitErr.Error()
	}
	return fmt.Errorf("Claude Code exited: %s", cmp.Or(said, "no reason given"))
}

func (p *proc) learn(capabilities []string) {
	if slices.Contains(capabilities, cancelQueued) {
		p.mu.Lock()
		p.cancels = true
		p.mu.Unlock()
	}
}

// halt asks claude to stop, and keeps it from taking another run.
func (p *proc) halt() {
	p.halted.Store(true)
	p.stop()
}

// park has claude wait for its thread's next run. Claude Code says nothing
// between turns, so a line it writes while parked is a turn of its own the
// next run would read as its own: claude is stopped instead.
func (p *proc) park() {
	p.wake, p.woke = make(chan struct{}), make(chan bool, 1)
	go func() {
		for {
			select {
			case line, ok := <-p.lines:
				if !ok {
					p.woke <- false
					return
				}
				// Claude Code reports a command's end after its result.
				var m struct{ Type, State string }
				if json.Unmarshal(line, &m) == nil && m.Type == "command_lifecycle" && m.State == "completed" {
					continue
				}
				slog.Warn("claude: a parked claude spoke, so it is stopped", "line", string(bytes.TrimSpace(line[:min(len(line), 200)])))
				p.halt()
				p.woke <- false
				return
			case <-p.wake:
				p.woke <- true
				return
			}
		}
	}()
}

// Wake ends the parked watch, and is whether claude can take a run. What
// claude said on stderr before belongs to the runs it served.
func (p *proc) Wake() bool {
	close(p.wake)
	if !<-p.woke || p.halted.Load() {
		return false
	}
	select {
	case <-p.reaped:
		return false
	default:
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stderr.reset()
	return p.cut == nil
}

func (p *proc) Done() <-chan struct{} { return p.reaped }

// Close ends stdin, which ends Claude Code once it is idle, and asks a
// process still busy after closeGrace, or when ctx ends first, to stop.
// Once claude is reaped, whatever it left running is stopped too.
func (p *proc) Close(ctx context.Context) error {
	p.close.Do(func() {
		close(p.done)
		p.stdin.Close()
	})
	select {
	case <-p.reaped:
		_, err := stopRun(ctx, p.mark)
		return err
	case <-time.After(closeGrace):
	case <-ctx.Done():
	}
	p.halt()
	select {
	case <-p.reaped:
		_, err := stopRun(ctx, p.mark)
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *proc) send(ctx context.Context, v any) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.sendLocked(ctx, v)
}

// sendLocked writes v as a line to claude's stdin, and gives up when ctx
// ends. A line cut short leaves the stream broken, so every later write
// fails too.
func (p *proc) sendLocked(ctx context.Context, v any) error {
	if p.cut != nil {
		return p.cut
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
		p.stdin.SetWriteDeadline(time.Now())
		close(expired)
	})
	n, err := p.stdin.Write(line)
	if !stop() {
		<-expired
	}
	p.stdin.SetWriteDeadline(time.Time{})
	if err == nil {
		return nil
	}
	cut := n > 0 || mayBeCut(err)
	if errors.Is(err, os.ErrDeadlineExceeded) {
		err = cmp.Or(ctx.Err(), err)
	}
	if cut {
		p.cut = fmt.Errorf("claude: a line to Claude Code was cut short: %w", err)
	}
	return err
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
		g.p.mu.Lock()
		cancels := g.p.cancels
		g.p.mu.Unlock()
		if !cancels && slices.Contains(slices.Collect(maps.Values(g.outstanding)), true) {
			g.interrupted = true
			g.p.halt()
			return nil
		}
		err := g.p.send(ctx, map[string]any{"type": "control_request", "request_id": interruptRequest,
			"request": map[string]any{"subtype": "interrupt", "cancel_queued": true}})
		if err == nil {
			g.interrupted = true
			g.deadline = time.AfterFunc(interruptGrace, g.p.halt)
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
	err := g.p.send(ctx, map[string]any{"type": "user", "uuid": id, "session_id": "", "parent_tool_use_id": nil,
		"message": map[string]string{"role": "user", "content": text}})
	if err != nil {
		delete(g.outstanding, id)
	}
	return err
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

// foreign is whether a result finishes only messages this run never sent,
// as one a kept claude's earlier run left would.
func (g *Agent) foreign(m message) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return m.UserMessageUUIDs != nil && !slices.ContainsFunc(m.UserMessageUUIDs, func(id string) bool {
		_, ok := g.outstanding[id]
		return ok
	})
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
	if err := g.p.send(ctx, reply); err != nil {
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
		case line, ok := <-g.p.lines:
			if !ok {
				if g.pending = g.turn.unfinished(); len(g.pending) > 0 {
					continue
				}
				if g.cutOff() {
					g.pending = []provider.Event{provider.TurnEnded{Outcome: api.OutcomeInterrupted}}
					continue
				}
				return nil, g.p.exited()
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
			g.p.send(ctx, refusal(m.RequestID, m.Request.Subtype))
			return nil
		}
		events, deny := g.open(m.RequestID, m.Request.canUseTool)
		if deny != nil {
			g.p.send(ctx, deny)
		}
		return events
	case "control_cancel_request":
		g.pmu.Lock()
		defer g.pmu.Unlock()
		if p := g.prompts[m.RequestID]; p != nil && p.state == asking {
			p.state = gone
			return []provider.Event{provider.RequestWithdrawn{ID: m.RequestID, Reason: withdrawnReason}}
		}
		return nil
	case "result":
		if g.foreign(m) {
			slog.Warn("claude: a result for messages this run did not send, which it ignores", "messages", m.UserMessageUUIDs)
			return nil
		}
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
			g.p.learn(m.Capabilities)
		}
	}
	return g.turn.read(m)
}

// open records the prompt a can_use_tool opens, and is the request the
// user sees, or the deny Claude Code gets instead. A request seen before
// opens nothing.
func (g *Agent) open(id string, r canUseTool) ([]provider.Event, map[string]any) {
	g.pmu.Lock()
	defer g.pmu.Unlock()
	if _, seen := g.prompts[id]; seen {
		return nil, nil
	}
	if r.ToolName != askTool {
		e, p := g.turn.ask(id, r)
		g.prompts[id] = p
		return []provider.Event{e}, nil
	}
	e, p, err := question(id, r.Input)
	if err != nil {
		// Questions the card can't show would hang the turn; Claude Code
		// hears why instead, and the user sees nothing to answer.
		g.prompts[id] = &prompt{kind: api.KindQuestion, state: answered}
		return nil, misfit(id, err)
	}
	g.prompts[id] = p
	return []provider.Event{e}, nil
}

// Close is the run's end. A claude whose turn ended on its own is parked
// for the thread's next run, once the tool shells the turn left are
// stopped, as a turn's end stops them. Any other is stopped, with all it
// started: one the user stopped may still take a message the stop did
// not get to as a turn of its own.
func (g *Agent) Close(ctx context.Context) error {
	g.once.Do(func() {
		g.mu.Lock()
		over := g.started && g.ended && !g.interrupted
		g.mu.Unlock()
		if !over || g.keep == nil || g.p.halted.Load() {
			return
		}
		select {
		case <-g.p.reaped:
			return
		default:
		}
		if err := stopTools(ctx, g.p.mark, g.p.cmd.Process.Pid); err != nil {
			slog.Warn("claude: a tool the turn left still runs, so claude is stopped", "err", err)
			return
		}
		g.p.park()
		g.keep(g.p)
		g.kept = true
	})
	if g.kept {
		return nil
	}
	return g.p.Close(ctx)
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

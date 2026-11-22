package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"

	"glitched/internal/api"
	"glitched/internal/provider"
)

// Agent is one run on an agent process. The SDK's handlers queue its
// events; Next hands them out in order.
type Agent struct {
	p    *proc
	spec provider.SessionSpec
	keep func(*proc) // parks p for the thread's next run
	once sync.Once
	kept bool

	// prompt ends the turn's session/prompt; only Close calls it.
	prompt context.Context
	cancel context.CancelFunc
	sent   chan struct{} // closes once the prompt is written
	done   chan struct{} // closes once the prompt is answered

	mu          sync.Mutex
	more        chan struct{} // has a value while out has news for Next
	out         []provider.Event
	started     bool
	interrupted bool
	ended       bool  // TurnEnded is queued; nothing is queued after it
	failed      error // the turn's prompt failed
	deadline    *time.Timer
	steps       map[acpsdk.ToolCallId]*step
	order       []acpsdk.ToolCallId
	asks        map[string]*ask
	asked       int
}

var (
	_ provider.Agent     = (*Agent)(nil)
	_ provider.Sessioned = (*Agent)(nil)
)

// ask is a request the agent waits on: a permission request, or a
// question it asks with a form. It gets one reply: the first answer, in the
// agent's shape, or nil once the agent or the turn stops waiting.
type ask struct {
	options []acpsdk.PermissionOption // a permission request's
	form    *form                     // a question's
	state   askState
	reply   chan any // takes the one reply
}

type askState int

const (
	asking askState = iota
	answered
	gone
)

var errNoAsk = errors.New("acp: the agent made no such request")

func newAgent(spec provider.SessionSpec, p *proc, keep func(*proc)) *Agent {
	g := &Agent{p: p, spec: spec, keep: keep, more: make(chan struct{}, 1), sent: make(chan struct{}), done: make(chan struct{}),
		steps: map[acpsdk.ToolCallId]*step{}, asks: map[string]*ask{}}
	g.prompt, g.cancel = context.WithCancel(context.Background())
	p.attach(g)
	return g
}

// notice tells the user, as a finished step of the turn's, what the agent
// can't.
func (g *Agent) notice(text string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.queueLocked(provider.Work{Item: "glitched-notice", Kind: "tool", Title: text, Status: api.WorkCompleted})
}

// Session is the agent's id for the thread's conversation.
func (g *Agent) Session() string { return g.p.sessionID() }

// queueLocked adds events for Next. g.mu is held.
func (g *Agent) queueLocked(events ...provider.Event) {
	if len(events) == 0 {
		return
	}
	g.out = append(g.out, events...)
	select {
	case g.more <- struct{}{}:
	default:
	}
}

func (g *Agent) Do(ctx context.Context, act provider.Action) error {
	switch a := act.(type) {
	case provider.Start:
		return g.start(a.Text)
	case provider.Interrupt:
		return g.interrupt(ctx)
	case provider.Respond:
		return g.respond(a)
	case provider.Steer:
		return fmt.Errorf("%w: %s can't take a message mid-turn yet. Send it once the turn ends.", provider.ErrUnsupported, g.p.profile.Name)
	}
	return provider.ErrUnsupported
}

// start sends the turn's prompt. Its answer, at the turn's end, is the
// run's TurnEnded. The session's first prompt carries the instructions of
// the run's MCP servers in front, as ACP has no other place for them.
func (g *Agent) start(text string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.started {
		return errors.New("acp: the run already started")
	}
	g.started = true
	if !g.p.briefed {
		g.p.briefed = true
		for i := len(g.spec.MCP) - 1; i >= 0; i-- {
			if s := g.spec.MCP[i].Instructions; s != "" {
				text = "<instructions>\n" + s + "\n</instructions>\n\n" + text
			}
		}
	}
	req := acpsdk.PromptRequest{SessionId: g.p.session, Prompt: []acpsdk.ContentBlock{acpsdk.TextBlock(text)}}
	g.p.expectPrompt(g.sent)
	go func() {
		defer close(g.done)
		r, err := g.p.conn.Prompt(g.prompt, req)
		g.finish(r, err)
	}()
	return nil
}

// finish ends the turn with the prompt's answer. The SDK handles every
// update the agent sent before it first, so nothing of the turn follows.
// Requests still open are withdrawn: the agent no longer waits on them.
func (g *Agent) finish(r acpsdk.PromptResponse, err error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.ended {
		return
	}
	g.withdrawLocked("The turn ended before the answer reached " + g.p.profile.Name + ".")
	end := provider.TurnEnded{Outcome: api.OutcomeCompleted}
	switch {
	case g.interrupted:
		end.Outcome = api.OutcomeInterrupted
	case err != nil:
		g.failed = err
		end.Outcome, end.Error = api.OutcomeFailed, g.p.failure("finish the turn", err).Error()
	case r.StopReason == acpsdk.StopReasonCancelled:
		end.Outcome = api.OutcomeInterrupted
	case r.StopReason == acpsdk.StopReasonMaxTokens:
		end.Outcome, end.Error = api.OutcomeFailed, g.p.profile.Name+" stopped: the reply reached its length limit."
	case r.StopReason == acpsdk.StopReasonMaxTurnRequests:
		end.Outcome, end.Error = api.OutcomeFailed, g.p.profile.Name+" stopped: the turn reached its limit of model requests."
	case r.StopReason == acpsdk.StopReasonRefusal:
		end.Outcome, end.Error = api.OutcomeFailed, g.p.profile.Name+" refused to go on."
	case r.StopReason != acpsdk.StopReasonEndTurn:
		g.failed = fmt.Errorf("stop reason %q", r.StopReason)
		end.Outcome, end.Error = api.OutcomeFailed, fmt.Sprintf("%s ended the turn for a reason Glitched doesn't know: %q.", g.p.profile.Name, r.StopReason)
	}
	for _, id := range g.order {
		if s := g.steps[id]; s.open() {
			s.status = map[bool]acpsdk.ToolCallStatus{true: acpsdk.ToolCallStatusCompleted, false: acpsdk.ToolCallStatusFailed}[end.Outcome == api.OutcomeCompleted]
			g.queueLocked(s.work(g.spec.Workspace))
		}
	}
	g.ended = true
	if g.deadline != nil {
		g.deadline.Stop()
	}
	g.queueLocked(end)
}

// interrupt cancels the turn. The agent ends it with stopReason cancelled,
// once it hears every request it waits on answered cancelled; one that
// has not ended it after interruptGrace is stopped.
func (g *Agent) interrupt(ctx context.Context) error {
	g.mu.Lock()
	if !g.started || g.interrupted || g.ended {
		g.mu.Unlock()
		return nil
	}
	g.interrupted = true
	g.withdrawLocked("The turn was stopped.")
	g.deadline = time.AfterFunc(interruptGrace, g.p.halt)
	g.mu.Unlock()
	// A cancel that reached the agent ahead of its prompt would cancel
	// nothing, and the turn would run on.
	select {
	case <-g.sent:
	case <-g.done:
		return nil
	case <-ctx.Done():
		g.p.halt()
		return nil
	}
	if err := g.p.conn.Cancel(ctx, acpsdk.CancelNotification{SessionId: g.p.session}); err != nil {
		slog.Warn("acp: the agent didn't take the cancel, so it is stopped", "agent", g.p.profile.Name, "err", err)
		g.p.halt()
	}
	return nil
}

// withdrawLocked answers every open request cancelled, and tells the user
// why. g.mu is held.
func (g *Agent) withdrawLocked(reason string) {
	for i := 1; i <= g.asked; i++ {
		id := askID(i)
		if a := g.asks[id]; a != nil && a.state == asking {
			a.state = gone
			a.reply <- nil
			g.queueLocked(provider.RequestWithdrawn{ID: id, Reason: reason})
		}
	}
}

func askID(n int) string { return "acp-" + strconv.Itoa(n) }

// permission is the answer to a request of the agent's: allow once for a
// tool of a trusted server's, whose engine decides each call itself, else
// the user's, from Glitched's own card. The SDK runs each request on its
// own goroutine, so this one waits for the answer.
func (g *Agent) permission(ctx context.Context, r acpsdk.RequestPermissionRequest) acpsdk.RequestPermissionOutcome {
	g.mu.Lock()
	if g.ended || g.interrupted {
		g.mu.Unlock()
		return acpsdk.NewRequestPermissionOutcomeCancelled()
	}
	if g.trusted(r.ToolCall) {
		if id, ok := pick(r.Options, acpsdk.PermissionOptionKindAllowOnce); ok {
			g.mu.Unlock()
			return acpsdk.NewRequestPermissionOutcomeSelected(id)
		}
	}
	g.asked++
	id := askID(g.asked)
	s := g.steps[r.ToolCall.ToolCallId]
	if s == nil {
		s = &step{id: r.ToolCall.ToolCallId}
	}
	merged := *s
	merged.apply(r.ToolCall)
	g.queueLocked(provider.RequestOpened{ID: id, Kind: api.KindApproval, Body: merged.approval(g.spec.Workspace, g.p.profile.Name)})
	// Glitched allows a call only once. A request that can't be allowed
	// so is declined, and the card says why, so the turn goes on.
	if _, ok := pick(r.Options, acpsdk.PermissionOptionKindAllowOnce); !ok {
		g.queueLocked(provider.RequestWithdrawn{ID: id, Reason: fmt.Sprintf("%s offers no way to allow this only once, so Glitched declined it. Allow it in %s itself to let it run.", g.p.profile.Name, g.p.profile.Name)})
		g.mu.Unlock()
		if reject, ok := pick(r.Options, acpsdk.PermissionOptionKindRejectOnce); ok {
			return acpsdk.NewRequestPermissionOutcomeSelected(reject)
		}
		return acpsdk.NewRequestPermissionOutcomeCancelled()
	}
	if o, ok := g.await(ctx, id, &ask{options: r.Options}).(acpsdk.RequestPermissionOutcome); ok {
		return o
	}
	return acpsdk.NewRequestPermissionOutcomeCancelled()
}

// question is the answer to a question the agent asks with a form, as agy
// does for its ask_question tool: Glitched's own question card, its
// options the form's, and the user's picks and words sent back in the
// form's fields. A form the card can't show is cancelled.
func (g *Agent) question(ctx context.Context, f acpsdk.CreateElicitationForm) acpsdk.CreateElicitationResponse {
	cancelled := acpsdk.CreateElicitationResponse{Cancel: &acpsdk.CreateElicitationCancel{Action: "cancel"}}
	fm, body, err := parseForm(f)
	if err != nil {
		slog.Warn("acp: a question Glitched's card can't show, so it is cancelled", "agent", g.p.profile.Name, "err", err)
		return cancelled
	}
	g.mu.Lock()
	if g.ended || g.interrupted {
		g.mu.Unlock()
		return cancelled
	}
	g.asked++
	id := askID(g.asked)
	g.queueLocked(provider.RequestOpened{ID: id, Kind: api.KindQuestion, Body: body})
	if r, ok := g.await(ctx, id, &ask{form: fm}).(acpsdk.CreateElicitationResponse); ok {
		return r
	}
	return cancelled
}

// await opens a and waits for its reply, which is nil when the agent
// withdrew the request or the connection ended. g.mu is held, and released.
func (g *Agent) await(ctx context.Context, id string, a *ask) any {
	a.reply = make(chan any, 1)
	g.asks[id] = a
	g.mu.Unlock()
	select {
	case o := <-a.reply:
		return o
	case <-ctx.Done():
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if a.state == asking {
		a.state = gone
		g.queueLocked(provider.RequestWithdrawn{ID: id, Reason: g.p.profile.Name + " no longer asks."})
	}
	return nil
}

// trusted is whether call is a tool of one of the run's trusted MCP
// servers, by the server the agent names in its input.
func (g *Agent) trusted(call acpsdk.ToolCallUpdate) bool {
	raw, ok := call.RawInput.Value()
	if !ok {
		return false
	}
	var in struct{ ServerName, Server string }
	json.Unmarshal(raw, &in)
	for _, s := range g.spec.MCP {
		if s.Trusted && (in.ServerName == s.Name || in.Server == s.Name) {
			return true
		}
	}
	return false
}

func pick(options []acpsdk.PermissionOption, kind acpsdk.PermissionOptionKind) (acpsdk.PermissionOptionId, bool) {
	for _, o := range options {
		if o.Kind == kind {
			return o.OptionId, true
		}
	}
	return "", false
}

// respond sends a request's one reply: a question's answers in its form's
// fields, or for a permission request, allow picks the agent's allow-once
// option and deny its reject-once one; Glitched never picks an option that
// lasts past the call. A request already answered takes nothing more, and
// one the agent stopped waiting on takes no answer.
func (g *Agent) respond(r provider.Respond) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	a, ok := g.asks[r.Request]
	switch {
	case !ok:
		return errNoAsk
	case a.state == answered:
		return nil
	case a.state == gone:
		return provider.ErrWithdrawn
	}
	var reply any
	var err error
	if a.form != nil {
		reply, err = a.form.answer(r.Answer)
	} else {
		reply, err = g.allow(a, r.Answer)
	}
	if err != nil {
		return err
	}
	a.state = answered
	a.reply <- reply
	return nil
}

// allow is the outcome of a permission request the answer gives.
func (g *Agent) allow(a *ask, raw json.RawMessage) (acpsdk.RequestPermissionOutcome, error) {
	var answer struct {
		Allow bool `json:"allow"`
	}
	if err := json.Unmarshal(raw, &answer); err != nil {
		return acpsdk.RequestPermissionOutcome{}, fmt.Errorf("acp: an answer the agent can't take: %w", err)
	}
	if !answer.Allow {
		if id, ok := pick(a.options, acpsdk.PermissionOptionKindRejectOnce); ok {
			return acpsdk.NewRequestPermissionOutcomeSelected(id), nil
		}
		return acpsdk.NewRequestPermissionOutcomeCancelled(), nil
	}
	id, ok := pick(a.options, acpsdk.PermissionOptionKindAllowOnce)
	if !ok {
		return acpsdk.RequestPermissionOutcome{}, fmt.Errorf("%s offers no way to allow this only once. Deny it, or allow it in %s itself.", g.p.profile.Name, g.p.profile.Name)
	}
	return acpsdk.NewRequestPermissionOutcomeSelected(id), nil
}

// update is what one of the turn's updates means for the run: text, or a
// step of the agent's.
func (g *Agent) update(u acpsdk.SessionUpdate) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.started || g.ended {
		g.p.retire(kindOf(u))
		return
	}
	switch {
	case u.AgentMessageChunk != nil:
		if t := u.AgentMessageChunk.Content.Text; t != nil && t.Text != "" {
			g.queueLocked(provider.TextDelta{Text: t.Text})
		}
	case u.ToolCall != nil:
		c := u.ToolCall
		s := &step{id: c.ToolCallId, title: c.Title, kind: c.Kind, status: c.Status, locations: c.Locations, input: c.RawInput, content: c.Content, output: c.RawOutput}
		if _, seen := g.steps[c.ToolCallId]; !seen {
			g.order = append(g.order, c.ToolCallId)
		}
		g.steps[c.ToolCallId] = s
		if s.shown() {
			g.queueLocked(s.work(g.spec.Workspace))
		}
	case u.ToolCallUpdate != nil:
		c := u.ToolCallUpdate
		s := g.steps[c.ToolCallId]
		if s == nil {
			s = &step{id: c.ToolCallId}
			g.steps[c.ToolCallId] = s
			g.order = append(g.order, c.ToolCallId)
		}
		s.apply(acpsdk.ToolCallUpdate{Content: c.Content, Kind: c.Kind, Locations: c.Locations, RawInput: c.RawInput,
			RawOutput: c.RawOutput, Status: c.Status, Title: c.Title, ToolCallId: c.ToolCallId})
		if s.shown() {
			g.queueLocked(s.work(g.spec.Workspace))
		}
	}
}

func (g *Agent) Next(ctx context.Context) (provider.Event, error) {
	for {
		g.mu.Lock()
		if len(g.out) > 0 {
			e := g.out[0]
			g.out = g.out[1:]
			g.mu.Unlock()
			return e, nil
		}
		started := g.started
		g.mu.Unlock()
		var gone <-chan struct{}
		if !started {
			gone = g.p.reaped
		}
		select {
		case <-g.more:
		case <-gone:
			return nil, g.p.exited()
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// Close is the run's end. An agent whose turn ended on its own is parked
// for the thread's next run. Any other is stopped, with all it started:
// one the user stopped may still be at work on the turn.
func (g *Agent) Close(ctx context.Context) error {
	g.once.Do(func() {
		g.mu.Lock()
		over := g.started && g.ended && !g.interrupted && g.failed == nil
		g.mu.Unlock()
		g.cancel()
		if !over || g.keep == nil || g.p.halted.Load() {
			g.p.attach(nil)
			return
		}
		select {
		case <-g.p.reaped:
			g.p.attach(nil)
			return
		case <-g.p.conn.Done():
			g.p.attach(nil)
			return
		default:
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

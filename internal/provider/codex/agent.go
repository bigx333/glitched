package codex

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"glitched/internal/api"
	"glitched/internal/provider"
)

// Agent is one run on a thread's app-server: one turn, with the requests
// Codex makes in it. The connection's reader hands it notifications and
// requests; Next hands out its events in order.
type Agent struct {
	p    *proc
	spec provider.SessionSpec
	keep func(*proc) // parks p for the thread's next run
	once sync.Once
	kept bool
	// native is that the run resumed the conversation the thread's rewind
	// forked.
	native bool

	// ctx ends the turn's calls; only Close cancels it.
	ctx    context.Context
	cancel context.CancelFunc
	// announced closes once Codex says the turn started: a steer or an
	// interrupt before then reaches no turn.
	announced chan struct{}
	done      chan struct{} // closes once the turn ended

	mu          sync.Mutex
	more        chan struct{} // has a value while out has news for Next
	out         []provider.Event
	started     bool
	interrupted bool
	ended       bool               // TurnEnded is queued; nothing is queued after it
	ending      bool               // the turn is over, and its end waits on what Codex says of the usage limit
	endRead     context.CancelFunc // gives up that read
	failed      bool               // the turn did not end on its own
	turn        string             // Codex's id of the turn
	problem     string             // the last error Codex said it won't retry
	deadline    *time.Timer
	items       map[string]*item
	order       []string
	texts       map[string]int       // how much of each agent message went out
	last        string               // the agent message the last text was of
	subagents   map[string]*subagent // native subagents, by their thread
	pending     map[string][]message // notifications of threads no spawn named yet
	held        int                  // the bytes pending holds
	notices     int
	tokens      int64 // what the turn's model requests used, so far
	asks        map[string]*ask
	asked       int
	// since is how many times Codex had said what the thread runs at as
	// the run took its app-server, none for one it started, and asked as
	// it sent the turn, which asks for the effort. told is whether the run
	// has reported it.
	since, effortSince int
	told               bool
	granted            *provider.Granted
	ran                *provider.Ran
}

var (
	_ provider.Agent     = (*Agent)(nil)
	_ provider.Sessioned = (*Agent)(nil)
	_ provider.Rewinding = (*Agent)(nil)
)

// Session is Codex's id of the thread's conversation.
func (g *Agent) Session() string { return g.p.threadID() }

// Native is whether the run resumed the conversation the thread's rewind
// forked: not where the rewind was portable, or Codex lost the fork.
func (g *Agent) Native() bool { return g.native }

// ask is a request Codex waits on. It gets one reply: the user's answer,
// or cancel once the turn stops waiting.
type ask struct {
	rpc       json.RawMessage
	method    string
	question  bool            // the question card, not the approval card
	ids       []string        // Codex's ids of the questions, in order
	tool      bool            // an MCP tool's approval, asked as a question
	questions api.Questions   // what the card shows, for a question
	wanted    json.RawMessage // the permissions asked, for a permissions request
	deny      string          // the decision that denies a command, of those Codex offers
	session   bool            // Codex offers to allow such calls for its session
	state     askState
}

type askState int

const (
	asking askState = iota
	answered
	gone
)

var errNoAsk = errors.New("codex: Codex made no such request")

// toolApproval starts the id of a question Codex asks to approve an MCP
// tool, and accept and refuse are its answers (codex-rs
// core/src/mcp_tool_call.rs).
const (
	toolApproval = "mcp_tool_call_approval"
	toolAccept   = "Allow"
	toolSession  = "Allow for this session"
	toolRefuse   = "Cancel"
)

func newAgent(spec provider.SessionSpec, p *proc, keep func(*proc)) *Agent {
	g := &Agent{p: p, spec: spec, keep: keep, more: make(chan struct{}, 1), announced: make(chan struct{}), done: make(chan struct{}),
		items: map[string]*item{}, texts: map[string]int{}, asks: map[string]*ask{}}
	g.ctx, g.cancel = context.WithCancel(context.Background())
	p.attach(g)
	return g
}

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

// noticeLocked tells the user, as a finished step of the turn's, what
// Codex can't. g.mu is held.
func (g *Agent) noticeLocked(text string) {
	g.notices++
	g.queueLocked(provider.Work{Item: "glitched-notice-" + strconv.Itoa(g.notices), Kind: "tool", Title: text, Status: api.WorkCompleted})
}

func (g *Agent) Do(ctx context.Context, act provider.Action) error {
	switch a := act.(type) {
	case provider.Start:
		return g.start(ctx, a.Text)
	case provider.Steer:
		return g.steer(ctx, a.Text)
	case provider.Interrupt:
		return g.interrupt(ctx)
	case provider.Respond:
		return g.respond(ctx, a)
	}
	return provider.ErrUnsupported
}

func input(text string) []map[string]any {
	return []map[string]any{{"type": "text", "text": text}}
}

// start sends the turn. Codex answers turn/start at once, and the turn's
// end arrives as turn/completed. Each setting is sent on every turn, as
// Codex keeps the last one a turn set.
func (g *Agent) start(ctx context.Context, text string) error {
	g.mu.Lock()
	if g.started {
		g.mu.Unlock()
		return errors.New("codex: the run already started")
	}
	g.started = true
	_, g.effortSince = g.p.settled()
	g.mu.Unlock()
	params := map[string]any{
		"threadId":          g.p.threadID(),
		"input":             input(text),
		"approvalPolicy":    levels[g.spec.Access].approval,
		"approvalsReviewer": g.p.reviewer,
		"sandboxPolicy":     levels[g.spec.Access].policy,
		"summary":           "auto",
		"serviceTier":       nil,
	}
	if g.spec.Model.Fast {
		params["serviceTier"] = fastTier
	}
	if m := g.spec.Model.Model; m != "" {
		params["model"] = m
	}
	if e := g.spec.Model.Effort; e != "" {
		params["effort"] = e
	}
	// A turn cancelled before it is queued is never sent; one queued is
	// the interrupt's to stop.
	wait, err := g.p.conn.request(ctx, "turn/start", params)
	if err != nil {
		g.mu.Lock()
		g.started = false
		g.mu.Unlock()
		return err
	}
	go g.watch()
	go func() {
		var r struct {
			Turn struct {
				ID string `json:"id"`
			} `json:"turn"`
		}
		err := wait(g.ctx, &r)
		g.mu.Lock()
		defer g.mu.Unlock()
		if err != nil {
			g.endLocked(provider.TurnEnded{Outcome: api.OutcomeFailed, Error: g.p.failure("start the turn", err).Error()})
			return
		}
		if g.turn == "" {
			g.turn = r.Turn.ID
		}
		if !g.ended {
			g.tellLocked()
		}
		if g.ended && g.turn != "" {
			g.p.ended(g.turn)
		}
	}()
	return nil
}

// fastTier is the service tier of Glitched's Fast: every model Codex
// lists offers it, by this id.
const fastTier = "priority"

// watch fails the turn when app-server goes before it ends.
func (g *Agent) watch() {
	select {
	case <-g.done:
		return
	case <-g.ctx.Done():
		return
	case <-g.p.conn.Done():
	}
	err := g.p.failure("finish the turn", errClosed)
	g.mu.Lock()
	defer g.mu.Unlock()
	end := provider.TurnEnded{Outcome: api.OutcomeFailed, Error: err.Error()}
	if g.interrupted {
		end = provider.TurnEnded{Outcome: api.OutcomeInterrupted}
	}
	g.endLocked(end)
}

// endLocked ends the turn with end, once. Requests still open are
// withdrawn, and steps still open end with it. g.mu is held.
func (g *Agent) endLocked(end provider.TurnEnded) {
	if g.ended {
		return
	}
	g.withdrawLocked("The turn ended before the answer reached Codex.")
	g.failed = end.Outcome != api.OutcomeCompleted
	for _, thread := range slices.Sorted(maps.Keys(g.subagents)) {
		g.subagents[thread].closeLocked(g)
	}
	for _, id := range g.order {
		if it := g.items[id]; it.open() {
			it.Status = map[bool]string{true: "completed", false: "failed"}[end.Outcome == api.OutcomeCompleted]
			g.queueLocked(it.work(g.spec.Workspace))
		}
	}
	end.Tokens, end.Head = g.tokens, g.turn
	g.ended = true
	if g.turn != "" {
		g.p.ended(g.turn)
	}
	if g.deadline != nil {
		g.deadline.Stop()
	}
	g.queueLocked(end)
	close(g.done)
}

// steer adds text to the turn under way.
func (g *Agent) steer(ctx context.Context, text string) error {
	select {
	case <-g.announced:
	case <-g.done:
		return errors.New("The turn ended before Codex took the message.")
	case <-ctx.Done():
		return ctx.Err()
	}
	g.mu.Lock()
	turn, ended := g.turn, g.ended
	g.mu.Unlock()
	if ended {
		return errors.New("The turn ended before Codex took the message.")
	}
	err := g.p.conn.call(ctx, "turn/steer", map[string]any{"threadId": g.p.threadID(), "expectedTurnId": turn, "input": input(text)}, nil)
	var re *rpcError
	if errors.As(err, &re) {
		return fmt.Errorf("Codex didn't take the message: %s", re.Message)
	}
	return err
}

// interrupt stops the turn. Codex ends it as interrupted; one that has
// not ended it after interruptGrace is stopped.
func (g *Agent) interrupt(ctx context.Context) error {
	g.mu.Lock()
	if !g.started || g.interrupted || g.ended {
		g.mu.Unlock()
		return nil
	}
	if g.ending {
		g.interrupted = true
		g.endRead()
		g.mu.Unlock()
		return nil
	}
	g.interrupted = true
	g.deadline = time.AfterFunc(interruptGrace, g.p.halt)
	g.withdrawLocked("The turn was stopped.")
	g.mu.Unlock()
	// Codex answers an interrupt before the turn started with "no active
	// turn", and the turn then runs on.
	select {
	case <-g.announced:
	case <-g.done:
		return nil
	case <-ctx.Done():
		g.p.halt()
		return nil
	}
	g.mu.Lock()
	turn := g.turn
	g.mu.Unlock()
	if err := g.p.conn.call(ctx, "turn/interrupt", map[string]any{"threadId": g.p.threadID(), "turnId": turn}, nil); err != nil {
		slog.Warn("codex: Codex didn't take the interrupt, so it is stopped", "err", err)
		g.p.halt()
	}
	return nil
}

// tokens is a count of Codex's, by kind.
type tokens struct {
	Total      int64 `json:"totalTokens"`
	Input      int64 `json:"inputTokens"`
	Cached     int64 `json:"cachedInputTokens"`
	CacheWrite int64 `json:"cacheWriteInputTokens"`
	Output     int64 `json:"outputTokens"`
	Reasoning  int64 `json:"reasoningOutputTokens"`
}

// tokenUsage is what Codex says the thread has spent, all told and on its
// last reading, and its model's window.
type tokenUsage struct {
	Total  tokens `json:"total"`
	Last   tokens `json:"last"`
	Window int64  `json:"modelContextWindow"`
}

// rateLimits is the account's usage windows as Codex says them, in
// account/rateLimits/updated and account/rateLimits/read; an update sends
// only the windows that moved.
type rateLimits struct {
	// LimitID names the allowance: "codex" is the account's, and another
	// is a model's own, which never stands for the account's.
	LimitID   string      `json:"limitId"`
	Primary   *rateWindow `json:"primary"`
	Secondary *rateWindow `json:"secondary"`
}

type rateWindow struct {
	UsedPercent float64 `json:"usedPercent"`
	ResetsAt    *int64  `json:"resetsAt"` // Unix seconds
}

// merged is l with the windows u sends.
func (l rateLimits) merged(u rateLimits) rateLimits {
	if u.Primary != nil {
		l.Primary = u.Primary
	}
	if u.Secondary != nil {
		l.Secondary = u.Secondary
	}
	return l
}

// resets is when the account may go on: once the last of its spent
// windows resets. known is false when none is spent, or one spent has no
// time, as T3 Code reads them.
func (l rateLimits) resets() (at time.Time, known bool) {
	var last int64
	for _, w := range []*rateWindow{l.Primary, l.Secondary} {
		if w == nil || w.UsedPercent < 100 {
			continue
		}
		if w.ResetsAt == nil || *w.ResetsAt <= 0 {
			return time.Time{}, false
		}
		last = max(last, *w.ResetsAt)
	}
	if last == 0 {
		return time.Time{}, false
	}
	return time.Unix(last, 0).UTC(), true
}

// limitInfos are the codexErrorInfo values of a turn a usage limit failed.
var limitInfos = map[string]bool{"usageLimitExceeded": true, "rateLimitExceeded": true}

// limitRead bounds the account/rateLimits/read a limited turn's end waits
// on, when no update said which window is spent.
const limitRead = 5 * time.Second

// notify is what a notification of the thread's means for the run.
func (g *Agent) notify(m message) {
	var n struct {
		TurnID    string          `json:"turnId"`
		ItemID    string          `json:"itemId"`
		Delta     string          `json:"delta"`
		Item      item            `json:"item"`
		Turn      json.RawMessage `json:"turn"`
		RequestID json.RawMessage `json:"requestId"`
		WillRetry bool            `json:"willRetry"`
		Error     *struct {
			Message string `json:"message"`
		} `json:"error"`
		TokenUsage tokenUsage `json:"tokenUsage"`
		Plan       []struct {
			Step   string `json:"step"`
			Status string `json:"status"`
		} `json:"plan"`
	}
	json.Unmarshal(m.Params, &n)
	var turn struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		Error  *struct {
			Message        string          `json:"message"`
			CodexErrorInfo json.RawMessage `json:"codexErrorInfo"`
		} `json:"error"`
	}
	json.Unmarshal(n.Turn, &turn)
	g.mu.Lock()
	defer g.mu.Unlock()
	if !speaks(m.Method) && m.Method != "serverRequest/resolved" {
		// How full the context is, as of the turn's last request: news
		// of the thread's, which this run reports while its turn is on.
		live := g.started && !g.ended && (n.TurnID == "" || n.TurnID == g.turn)
		usage := m.Method == "thread/tokenUsage/updated"
		u, prev := n.TokenUsage, tokenUsage{}
		var p *tokenUsage
		if usage {
			p = g.p.reading(n.TurnID, u, live)
		}
		if p != nil {
			prev = *p
		}
		// Codex sends its last reading again, as after a rate-limit
		// update or a response cut short, with whatever was spent since:
		// a request's own reading is new, or of another window, is all
		// that was spent, and holds its input. A request too big for the window has Codex
		// fill it, emptying every count but the totals.
		fresh := u.Last.Input > 0 && (u.Last != prev.Last || u.Window != prev.Window) && u.Total.Total-prev.Total.Total == u.Last.Total
		filled := u.Window > 0 && u.Total == tokens{Total: u.Window} && u.Last == tokens{Total: u.Last.Total}
		// The turn spent what the thread's total grew by since the last
		// reading, or, with none to go by, what its request did. A filled
		// window's last reading is made up, and no spend.
		switch grew := u.Total.Total - prev.Total.Total; {
		case !live || !usage || filled:
		case p != nil && grew >= 0:
			g.tokens += grew
		default:
			g.tokens += u.Last.Total
		}
		switch {
		case !live:
		case usage && filled:
			g.queueLocked(provider.Context{Used: u.Window, Window: u.Window})
		case usage && fresh:
			g.queueLocked(provider.Context{Used: u.Last.Total, Window: u.Window})
		case m.Method == "thread/compacted":
			g.queueLocked(provider.Context{Compacted: true})
		case m.Method == "thread/settings/updated" && g.told:
			g.tellLocked()
		}
		return
	}
	if n.TurnID == "" {
		n.TurnID = turn.ID
	}
	if g.ended || g.ending || !g.started || n.TurnID != "" && (g.p.isOver(n.TurnID) || g.turn != "" && n.TurnID != g.turn) {
		g.p.retire(m.Method)
		return
	}
	switch m.Method {
	case "turn/started":
		if g.turn == "" {
			g.turn = n.TurnID
		}
		g.tellLocked()
		select {
		case <-g.announced:
		default:
			close(g.announced)
		}
	case "item/agentMessage/delta":
		g.textLocked(n.ItemID, n.Delta)
	case "turn/plan/updated":
		// Codex's update_plan: the whole plan, each time.
		p := provider.Plan{Steps: make([]api.PlanStep, len(n.Plan))}
		for i, s := range n.Plan {
			p.Steps[i] = api.PlanStep{Text: s.Step, Status: provider.PlanStatus(s.Status)}
		}
		g.queueLocked(p)
	case "item/started", "item/completed":
		it := n.Item
		if it.Type == "agentMessage" && m.Method == "item/completed" {
			// A message whose deltas fell short, or that had none, goes
			// out whole.
			if sent := g.texts[it.ID]; sent < len(it.Text) {
				g.textLocked(it.ID, it.Text[sent:])
			}
		}
		if it.Type == "contextCompaction" && m.Method == "item/completed" {
			g.queueLocked(provider.Context{Compacted: true})
		}
		if it.ID == "" || !it.shown() {
			return
		}
		step, bound := g.spawnLocked(&it)
		if !step {
			return
		}
		if _, seen := g.items[it.ID]; !seen {
			g.order = append(g.order, it.ID)
		}
		g.items[it.ID] = &it
		g.queueLocked(it.work(g.spec.Workspace))
		for _, thread := range bound {
			g.drainLocked(thread)
		}
	case "error":
		if !n.WillRetry && n.Error != nil {
			g.problem = n.Error.Message
		}
	case "serverRequest/resolved":
		for i := 1; i <= g.asked; i++ {
			id := askID(i)
			if a := g.asks[id]; a != nil && a.state == asking && bytes.Equal(a.rpc, n.RequestID) {
				a.state = gone
				g.queueLocked(provider.RequestWithdrawn{ID: id, Reason: "Codex no longer asks."})
			}
		}
	case "turn/completed":
		end := provider.TurnEnded{Outcome: api.OutcomeCompleted}
		switch {
		case turn.Status == "interrupted" || g.interrupted:
			end.Outcome = api.OutcomeInterrupted
		case turn.Status == "failed":
			end.Outcome = api.OutcomeFailed
			var info string
			if turn.Error != nil {
				json.Unmarshal(turn.Error.CodexErrorInfo, &info)
				end.Error = turn.Error.Message
			}
			if end.Error == "" {
				end.Error = g.problem
			}
			if info == "unauthorized" {
				end.Error = signIn
			}
			if end.Error == "" {
				end.Error = "Codex ended the turn without saying why."
			}
			if limitInfos[info] {
				end.Limit = &api.UsageLimit{}
				if at, known := g.p.limitsNow().resets(); known {
					end.Limit.Resets = at
				} else {
					// The turn's first request can be refused before Codex
					// says which window it spent: ask, off the reader.
					ctx, cancel := context.WithTimeout(g.ctx, limitRead)
					g.ending, g.endRead = true, cancel
					go g.endLimited(ctx, end)
					return
				}
			}
		case turn.Status != "completed":
			end.Outcome, end.Error = api.OutcomeFailed, fmt.Sprintf("Codex ended the turn as %q, which Glitched doesn't know.", turn.Status)
		}
		g.endLocked(end)
	}
}

// endLimited ends the turn a usage limit failed, once Codex says when the
// spent window resets, or can't.
// What Codex said of the windows meanwhile counts, whether or not the read
// was answered. A Stop that came while it read ends the turn as stopped:
// Codex had already ended it, and no limit holds the thread.
func (g *Agent) endLimited(ctx context.Context, end provider.TurnEnded) {
	var r struct {
		RateLimits rateLimits `json:"rateLimits"`
	}
	if err := g.p.conn.call(ctx, "account/rateLimits/read", nil, &r); err == nil {
		g.p.limited(r.RateLimits)
	}
	end.Limit.Resets, _ = g.p.limitsNow().resets()
	g.mu.Lock()
	defer g.mu.Unlock()
	g.endRead()
	if g.interrupted {
		end = provider.TurnEnded{Outcome: api.OutcomeInterrupted}
	}
	g.endLocked(end)
}

// pendingLimit bounds, in bytes, the notifications of threads no spawn has
// named yet, which a quick child's can be: Codex may send its whole turn
// before the item that spawned it says whose it is. Zeron holds 4 MiB.
const pendingLimit = 4 << 20

// nestedMethods are the notifications of a child's thread the run maps;
// no other is held for one.
var nestedMethods = map[string]bool{
	"item/agentMessage/delta": true, "item/started": true, "item/completed": true,
	"thread/tokenUsage/updated": true, "turn/completed": true,
}

// subagent is a native subagent of the run's, a thread Codex spawned:
// parent is the item of the run's that started it, and the rest is what
// its own items need, as the run's do.
type subagent struct {
	parent string
	texts  map[string]int
	last   string
	items  map[string]*item
	order  []string
	tokens int64           // what its thread spent, all told, as Codex last said
	ended  map[string]bool // its turns that ended
}

// spawnLocked is whether it is a step of the run's, and binds the threads
// it spawned to it, newly bound. A subAgentActivity is a step only as the
// start of a thread no earlier item spawned, never the run's own thread;
// its other kinds are news of a thread already shown. g.mu is held.
func (g *Agent) spawnLocked(it *item) (step bool, bound []string) {
	if it.Type == "subAgentActivity" {
		thread := it.AgentThreadID
		if it.ActivityKind != "started" || thread == "" || thread == g.p.threadID() || it.AgentPath == "/" || it.AgentPath == "/root" {
			return false, nil
		}
		// Codex may say a spawn's start again: it is one step, told once.
		if g.subagents[thread] != nil {
			return false, nil
		}
	}
	for _, thread := range it.spawned() {
		if thread == g.p.threadID() || g.subagents[thread] != nil {
			continue
		}
		if g.subagents == nil {
			g.subagents = map[string]*subagent{}
		}
		g.subagents[thread] = &subagent{parent: it.ID, texts: map[string]int{}, items: map[string]*item{}, ended: map[string]bool{}}
		bound = append(bound, thread)
	}
	return true, bound
}

// drainLocked plays what thread sent before a spawn named it, in the order
// it came, after the spawn's own step. g.mu is held.
func (g *Agent) drainLocked(thread string) {
	held := g.pending[thread]
	delete(g.pending, thread)
	for _, m := range held {
		g.held -= len(m.Params)
	}
	for _, m := range held {
		g.nestedLocked(g.subagents[thread], m)
	}
}

// closeLocked fails the steps s still has open. g.mu is held.
func (s *subagent) closeLocked(g *Agent) {
	for _, id := range s.order {
		if it := s.items[id]; it.open() {
			it.Status = "failed"
			g.queueLocked(provider.Nested{Parent: s.parent, Event: it.work(g.spec.Workspace)})
		}
	}
}

// nested is what a notification of a native subagent's thread means for
// the run: its text and steps under the item that started it, and its end
// with what its thread spent. One of a thread no spawn named yet waits for
// one, within pendingLimit; a thread the run never spawns is nobody's.
func (g *Agent) nested(thread string, m message) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.started || g.ended {
		return
	}
	if s := g.subagents[thread]; s != nil {
		g.nestedLocked(s, m)
		return
	}
	if !nestedMethods[m.Method] || g.held+len(m.Params) > pendingLimit {
		return
	}
	if g.pending == nil {
		g.pending = map[string][]message{}
	}
	g.pending[thread] = append(g.pending[thread], m)
	g.held += len(m.Params)
}

func (g *Agent) nestedLocked(s *subagent, m message) {
	var n struct {
		TurnID     string     `json:"turnId"`
		ItemID     string     `json:"itemId"`
		Delta      string     `json:"delta"`
		Item       item       `json:"item"`
		TokenUsage tokenUsage `json:"tokenUsage"`
		Turn       struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"turn"`
	}
	json.Unmarshal(m.Params, &n)
	add := func(e provider.Event) { g.queueLocked(provider.Nested{Parent: s.parent, Event: e}) }
	text := func(itemID, t string) {
		if t == "" {
			return
		}
		if s.last != itemID && s.last != "" {
			add(provider.TextDelta{Text: "\n\n"})
		}
		s.last = itemID
		s.texts[itemID] += len(t)
		add(provider.TextDelta{Text: t})
	}
	switch m.Method {
	case "item/agentMessage/delta":
		text(n.ItemID, n.Delta)
	case "item/started", "item/completed":
		it := n.Item
		if it.Type == "agentMessage" && m.Method == "item/completed" {
			if sent := s.texts[it.ID]; sent < len(it.Text) {
				text(it.ID, it.Text[sent:])
			}
		}
		if !it.shown() || it.ID == "" || it.Type == "subAgentActivity" {
			return
		}
		if _, seen := s.items[it.ID]; !seen {
			s.order = append(s.order, it.ID)
		}
		s.items[it.ID] = &it
		add(it.work(g.spec.Workspace))
	case "thread/tokenUsage/updated":
		s.tokens = n.TokenUsage.Total.Total
	case "turn/completed":
		turn := cmp.Or(n.Turn.ID, n.TurnID)
		if s.ended[turn] {
			return
		}
		s.ended[turn] = true
		s.closeLocked(g)
		end := provider.TurnEnded{Outcome: api.OutcomeCompleted, Tokens: s.tokens}
		switch n.Turn.Status {
		case "interrupted":
			end.Outcome = api.OutcomeInterrupted
		case "failed":
			end.Outcome = api.OutcomeFailed
		}
		add(end)
	}
}

// tellLocked reports the access, model and effort Codex says the thread
// runs at, each part that changed since the run last did. Codex says them
// as it starts or resumes the thread, and again whenever a turn changes
// them, before it answers turn/start. A part is confirmed where Codex
// said it after the run asked for it, or where what it last said is what
// the run asks; any other goes unreported. g.mu is held.
func (g *Agent) tellLocked() {
	g.told = true
	s, _ := g.p.settled()
	if gr, ok := g.grantedOf(s); ok && (g.granted == nil || *g.granted != gr) {
		g.granted = &gr
		g.queueLocked(gr)
	}
	if r, ok := g.ranOf(s); ok && (g.ran == nil || *g.ran != r) {
		g.ran = &r
		g.queueLocked(r)
	}
}

// holds is whether x is confirmed for a run that asked for want,
// "" for anything, by Codex's saying since.
func holds(x part, want string, since int) bool {
	return x.said > since || want == "" || x.value == want
}

// grantedOf is the access s makes, read back through levels by its
// sandbox, with why it falls short of the run's. A reviewer Codex doesn't
// name is the user, as before Codex had auto-review.
func (g *Agent) grantedOf(s settings) (provider.Granted, bool) {
	asked := levels[g.spec.Access]
	approval, sandbox, reviewer := s.approval.value, s.sandbox.value, cmp.Or(s.reviewer.value, "user")
	if approval == "" || !holds(s.approval, asked.approval, g.since) || !holds(s.sandbox, asked.policy["type"].(string), g.since) ||
		s.reviewer.said <= g.since && reviewer != g.p.reviewer {
		return provider.Granted{}, false
	}
	access, found := api.Access(0), false
	for a, l := range levels {
		if l.policy["type"] == sandbox {
			access, found = a, true
		}
	}
	if !found {
		return provider.Granted{}, false
	}
	gr := provider.Granted{Access: access, Mode: strings.Join([]string{levels[access].sandbox, approval, reviewer}, ", ")}
	same := approval == asked.approval && reviewer == asked.reviewer
	switch {
	case access > g.spec.Access || access == g.spec.Access && same:
	case access == g.spec.Access && approval == asked.approval && asked.reviewer == autoReview:
		gr.Why = noAuto(g.p.version)
	default:
		gr.Why = "Codex runs " + gr.Mode
	}
	return gr, true
}

// ranOf is the model and effort s names, with why they aren't the run's.
// The turn asks for the effort, so Codex confirms it only from then: an
// effort it doesn't confirm is one it doesn't say.
func (g *Agent) ranOf(s settings) (provider.Ran, bool) {
	want := g.spec.Model
	if s.model.value == "" || !holds(s.model, want.Model, g.since) {
		return provider.Ran{}, false
	}
	r := provider.Ran{Model: s.model.value}
	if holds(s.effort, want.Effort, g.effortSince) {
		r.Effort = s.effort.value
	}
	if r.Model != want.Model || want.Effort != "" && r.Effort != "" && r.Effort != want.Effort {
		r.Why = "Codex runs " + r.Model
		if r.Effort != "" {
			r.Why += " at " + r.Effort
		}
	}
	return r, true
}

// textLocked hands out text of the agent message itemID. Codex puts
// nothing between two messages of a turn, so a new one starts after a
// blank line. g.mu is held.
func (g *Agent) textLocked(itemID, text string) {
	if text == "" {
		return
	}
	if g.last != itemID && g.last != "" {
		g.queueLocked(provider.TextDelta{Text: "\n\n"})
	}
	g.last = itemID
	g.texts[itemID] += len(text)
	g.queueLocked(provider.TextDelta{Text: text})
}

func askID(n int) string { return "codex-" + strconv.Itoa(n) }

// cancelled is the answer to a request the turn no longer waits on.
func cancelled(method string) any {
	switch method {
	case "item/permissions/requestApproval":
		return map[string]any{"permissions": map[string]any{}, "scope": "turn"}
	case "item/tool/requestUserInput":
		return map[string]any{"answers": map[string]any{}}
	case "mcpServer/elicitation/request":
		return map[string]any{"action": "cancel", "content": nil}
	}
	return map[string]any{"decision": "cancel"}
}

// request opens Glitched's card for a request of Codex's. Codex waits on
// it until respond answers, or the turn stops waiting.
func (g *Agent) request(m message) {
	var r struct {
		TurnID      string            `json:"turnId"`
		ItemID      string            `json:"itemId"`
		Command     string            `json:"command"`
		Cwd         string            `json:"cwd"`
		Reason      string            `json:"reason"`
		Permissions json.RawMessage   `json:"permissions"`
		Network     json.RawMessage   `json:"networkApprovalContext"`
		Additional  json.RawMessage   `json:"additionalPermissions"`
		Decisions   []json.RawMessage `json:"availableDecisions"`
		Questions   []struct {
			ID       string `json:"id"`
			Header   string `json:"header"`
			Question string `json:"question"`
			IsOther  bool   `json:"isOther"`
			IsSecret bool   `json:"isSecret"`
			Options  []struct {
				Label       string `json:"label"`
				Description string `json:"description"`
			} `json:"options"`
		} `json:"questions"`
		Mode string `json:"mode"`
		Meta struct {
			Persist json.RawMessage `json:"persist"`
		} `json:"_meta"`
		Message         string          `json:"message"`
		ServerName      string          `json:"serverName"`
		RequestedSchema json.RawMessage `json:"requestedSchema"`
	}
	json.Unmarshal(m.Params, &r)
	g.mu.Lock()
	defer g.mu.Unlock()
	switch m.Method {
	case "item/commandExecution/requestApproval", "item/fileChange/requestApproval", "item/permissions/requestApproval",
		"item/tool/requestUserInput", "mcpServer/elicitation/request":
	default:
		g.p.conn.refuse(m.ID, -32601, "Glitched can't answer "+m.Method)
		return
	}
	if r.TurnID != "" && g.p.isOver(r.TurnID) {
		g.p.retire(m.Method)
		g.p.conn.reply(m.ID, cancelled(m.Method))
		return
	}
	if !g.started || g.ended || g.ending || g.interrupted {
		g.p.conn.reply(m.ID, cancelled(m.Method))
		return
	}
	a := &ask{rpc: m.ID, method: m.Method}
	var b body
	switch m.Method {
	case "item/commandExecution/requestApproval":
		b = body{Command: r.Command, Cwd: r.Cwd, Reason: r.Reason}
		if it := g.items[r.ItemID]; it != nil && b.Command == "" {
			b.Command, b.Cwd = it.Command, it.Cwd
		}
		if b.Command == "" {
			b.Command = "a command Codex didn't name"
		}
		if extra := extraAccess(r.Network, r.Additional); extra != "" {
			b.Reason = strings.TrimSpace(b.Reason + "\n" + extra)
		}
		var offered []string
		for _, d := range r.Decisions {
			var name string
			if json.Unmarshal(d, &name) == nil {
				offered = append(offered, name)
			}
		}
		a.deny = denial(offered)
		// With no list, absent or null, Codex's own TUI offers the session
		// for a network prompt alone (codex-rs tui/src/app.rs).
		b.Session = slices.Contains(offered, "acceptForSession")
		if r.Decisions == nil {
			b.Session = len(r.Network) > 0 && string(r.Network) != "null"
		}
	case "item/fileChange/requestApproval":
		// Codex keeps a grant for the session only for a change it asks
		// about with no reason; one with a reason, even "", it asks again.
		var asked struct {
			Reason *string `json:"reason"`
		}
		json.Unmarshal(m.Params, &asked)
		b = body{Operation: "edit", Reason: r.Reason, Session: asked.Reason == nil}
		if it := g.items[r.ItemID]; it != nil {
			b.Path = strings.Join(it.paths(g.spec.Workspace), ", ")
		}
	case "item/permissions/requestApproval":
		a.wanted = r.Permissions
		b = body{Operation: "get more access", Reason: r.Reason, Args: argsText(r.Permissions), Session: true}
	case "mcpServer/elicitation/request":
		var schema struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		json.Unmarshal(r.RequestedSchema, &schema)
		if r.Mode != "form" || len(schema.Properties) > 0 {
			g.noticeLocked(fmt.Sprintf("%s asked for something Glitched can't show yet, so Glitched declined it.", r.ServerName))
			g.p.conn.reply(m.ID, map[string]any{"action": "decline", "content": nil})
			return
		}
		b = body{Operation: "answer " + r.ServerName, Reason: r.Message}
		// An MCP tool's approval names the grants Codex keeps in its
		// persist: "session", or a list of them.
		var one string
		var persist []string
		if json.Unmarshal(r.Meta.Persist, &one) == nil {
			persist = []string{one}
		} else {
			json.Unmarshal(r.Meta.Persist, &persist)
		}
		b.Session = slices.Contains(persist, "session")
	case "item/tool/requestUserInput":
		// A legacy MCP tool approval is Codex's closed question: the only
		// one, its id the prefix and the call's, Allow and Cancel among its
		// options, and no answer of the user's own.
		var labels []string
		if len(r.Questions) == 1 {
			for _, o := range r.Questions[0].Options {
				labels = append(labels, o.Label)
			}
		}
		if len(r.Questions) == 1 && r.Questions[0].ID == toolApproval+"_"+r.ItemID && !r.Questions[0].IsOther && !r.Questions[0].IsSecret &&
			slices.Contains(labels, toolAccept) && slices.Contains(labels, toolRefuse) {
			q := r.Questions[0]
			a.tool, a.ids = true, []string{q.ID}
			b = body{Operation: "use a tool", Reason: q.Question, Session: slices.Contains(labels, toolSession)}
			if it := g.items[r.ItemID]; it != nil {
				b.Operation, b.Args = "use "+it.Server+": "+it.Tool, argsText(it.Arguments)
			}
			break
		}
		a.question = true
		for _, q := range r.Questions {
			x := api.Question{Text: q.Question, Header: q.Header}
			for _, o := range q.Options {
				x.Options = append(x.Options, api.Option{Label: o.Label, About: o.Description})
			}
			a.questions.Questions = append(a.questions.Questions, x)
			a.ids = append(a.ids, q.ID)
		}
		raw, _ := json.Marshal(a.questions)
		_, err := api.ParseQuestions(raw)
		if err == nil {
			for _, q := range r.Questions {
				if q.IsSecret {
					err = errors.New("a secret")
				}
			}
		}
		if err != nil {
			slog.Info("codex: a question the card can't show", "err", err)
			g.noticeLocked("Codex asked a question Glitched can't show yet, so it went unanswered.")
			g.p.conn.reply(m.ID, map[string]any{"answers": map[string]any{}})
			return
		}
		g.asked++
		id := askID(g.asked)
		g.asks[id] = a
		g.queueLocked(provider.RequestOpened{ID: id, Kind: api.KindQuestion, Body: raw})
		return
	}
	a.session = b.Session
	g.asked++
	id := askID(g.asked)
	g.asks[id] = a
	g.queueLocked(provider.RequestOpened{ID: id, Kind: api.KindApproval, Body: b.json()})
}

// withdrawLocked answers every open request cancelled, and tells the
// user why. g.mu is held.
func (g *Agent) withdrawLocked(reason string) {
	for i := 1; i <= g.asked; i++ {
		id := askID(i)
		if a := g.asks[id]; a != nil && a.state == asking {
			a.state = gone
			g.p.conn.reply(a.rpc, cancelled(a.method))
			g.queueLocked(provider.RequestWithdrawn{ID: id, Reason: reason})
		}
	}
}

// respond sends a request's one reply. An approval allows this once, or
// for Codex's session where Codex offers that; Glitched never picks an
// answer that lasts past the session. A request already answered takes
// nothing more, and one Codex stopped waiting on takes no answer.
func (g *Agent) respond(ctx context.Context, r provider.Respond) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
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
	if a.question {
		answers, err := api.ParseAnswers(r.Answer)
		if err != nil {
			return err
		}
		if err := answers.Fit(a.questions); err != nil {
			return err
		}
		out := map[string]any{}
		for i, x := range answers.Answers {
			picked := append([]string{}, x.Picked...)
			if strings.TrimSpace(x.Other) != "" {
				picked = append(picked, x.Other)
			}
			out[a.ids[i]] = map[string]any{"answers": picked}
		}
		reply = map[string]any{"answers": out}
	} else {
		var answer struct {
			Allow   bool `json:"allow"`
			Session bool `json:"session"`
		}
		if err := json.Unmarshal(r.Answer, &answer); err != nil {
			return fmt.Errorf("codex: an answer Codex can't take: %w", err)
		}
		session := answer.Allow && answer.Session
		if session && !a.session {
			return errors.New("codex: Codex offers no way to allow this for the session. Allow it once, or deny it.")
		}
		reply = approve(a, answer.Allow, session)
	}
	// An answer cancelled before it is queued is never sent, and the ask
	// stays open for its turn's end to withdraw.
	if err := g.p.conn.put(ctx, map[string]any{"id": a.rpc, "result": reply}); err != nil {
		return err
	}
	a.state = answered
	return nil
}

// approve is the answer to an approval request: allow or deny, once, or
// allow for the session.
func approve(a *ask, allow, session bool) any {
	switch {
	case a.tool:
		pick := map[bool]string{true: toolAccept, false: toolRefuse}[allow]
		if session {
			pick = toolSession
		}
		return map[string]any{"answers": map[string]any{a.ids[0]: map[string]any{"answers": []string{pick}}}}
	case a.method == "item/permissions/requestApproval":
		granted := json.RawMessage("{}")
		if allow && len(a.wanted) > 0 && string(a.wanted) != "null" {
			granted = a.wanted
		}
		return map[string]any{"permissions": granted, "scope": map[bool]string{true: "session", false: "turn"}[session]}
	case a.method == "mcpServer/elicitation/request":
		if session {
			return map[string]any{"action": "accept", "content": map[string]any{}, "_meta": map[string]any{"persist": "session"}}
		}
		if allow {
			return map[string]any{"action": "accept", "content": map[string]any{}}
		}
		return map[string]any{"action": "decline", "content": nil}
	}
	switch {
	case session:
		return map[string]any{"decision": "acceptForSession"}
	case !allow && a.deny != "":
		return map[string]any{"decision": a.deny}
	}
	return map[string]any{"decision": map[bool]string{true: "accept", false: "decline"}[allow]}
}

// denial is the decision that denies a command once: decline, unless
// Codex offers only cancel, which also ends the turn.
func denial(offered []string) string {
	switch {
	case slices.Contains(offered, "decline"):
		return "decline"
	case slices.Contains(offered, "cancel"):
		return "cancel"
	}
	return ""
}

// extraAccess is the access a command asks for beyond the sandbox's, as
// the card shows it.
func extraAccess(network, additional json.RawMessage) string {
	var lines []string
	for _, f := range []struct {
		name string
		raw  json.RawMessage
	}{{"network", network}, {"permissions", additional}} {
		if len(f.raw) == 0 || string(f.raw) == "null" {
			continue
		}
		var c bytes.Buffer
		if json.Compact(&c, f.raw) != nil {
			continue
		}
		lines = append(lines, f.name+": "+c.String())
	}
	return strings.Join(lines, "\n")
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

// Close is the run's end. An app-server whose turn ended on its own is
// parked for the thread's next run. Any other is stopped, with all it
// started: one the user stopped may still be at work on the turn.
func (g *Agent) Close(ctx context.Context) error {
	g.once.Do(func() {
		g.mu.Lock()
		over := g.started && g.ended && !g.interrupted && !g.failed
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

package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
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
	ended       bool   // TurnEnded is queued; nothing is queued after it
	failed      bool   // the turn did not end on its own
	turn        string // Codex's id of the turn
	problem     string // the last error Codex said it won't retry
	deadline    *time.Timer
	items       map[string]*item
	order       []string
	texts       map[string]int // how much of each agent message went out
	last        string         // the agent message the last text was of
	notices     int
	asks        map[string]*ask
	asked       int
}

var (
	_ provider.Agent     = (*Agent)(nil)
	_ provider.Sessioned = (*Agent)(nil)
)

// Session is Codex's id of the thread's conversation.
func (g *Agent) Session() string { return g.p.threadID() }

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
		return g.start(a.Text)
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
func (g *Agent) start(text string) error {
	g.mu.Lock()
	if g.started {
		g.mu.Unlock()
		return errors.New("codex: the run already started")
	}
	g.started = true
	g.mu.Unlock()
	params := map[string]any{
		"threadId":          g.p.threadID(),
		"input":             input(text),
		"approvalPolicy":    askPolicy,
		"approvalsReviewer": "user",
		"sandboxPolicy":     map[string]any{"type": "readOnly", "networkAccess": false},
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
	go g.watch()
	go func() {
		var r struct {
			Turn struct {
				ID string `json:"id"`
			} `json:"turn"`
		}
		err := g.p.conn.call(g.ctx, "turn/start", params, &r)
		g.mu.Lock()
		defer g.mu.Unlock()
		if err != nil {
			g.endLocked(provider.TurnEnded{Outcome: api.OutcomeFailed, Error: g.p.failure("start the turn", err).Error()})
			return
		}
		if g.turn == "" {
			g.turn = r.Turn.ID
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
	for _, id := range g.order {
		if it := g.items[id]; it.open() {
			it.Status = map[bool]string{true: "completed", false: "failed"}[end.Outcome == api.OutcomeCompleted]
			g.queueLocked(it.work(g.spec.Workspace))
		}
	}
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
		if p := g.p.usage.Load(); usage && p != nil {
			prev = *p
		}
		if usage {
			g.p.usage.Store(&u)
		}
		// Codex sends its last reading again, as after a rate-limit
		// update or a response cut short, with whatever was spent since:
		// a request's own reading is new, is all that was spent, and
		// holds its input. A request too big for the window has Codex
		// fill it, emptying every count but the totals.
		fresh := u.Last.Input > 0 && u.Last != prev.Last && u.Total.Total-prev.Total.Total == u.Last.Total
		filled := u.Window > 0 && u.Total == tokens{Total: u.Window} && u.Last == tokens{Total: u.Last.Total}
		switch {
		case !live:
		case usage && filled:
			g.queueLocked(provider.Context{Used: u.Window, Window: u.Window})
		case usage && fresh:
			g.queueLocked(provider.Context{Used: u.Last.Total, Window: u.Window})
		case m.Method == "thread/compacted":
			g.queueLocked(provider.Context{Compacted: true})
		}
		return
	}
	if n.TurnID == "" {
		n.TurnID = turn.ID
	}
	if g.ended || !g.started || n.TurnID != "" && (g.p.isOver(n.TurnID) || g.turn != "" && n.TurnID != g.turn) {
		g.p.retire(m.Method)
		return
	}
	switch m.Method {
	case "turn/started":
		if g.turn == "" {
			g.turn = n.TurnID
		}
		select {
		case <-g.announced:
		default:
			close(g.announced)
		}
	case "item/agentMessage/delta":
		g.textLocked(n.ItemID, n.Delta)
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
		if !it.shown() || it.ID == "" {
			return
		}
		if _, seen := g.items[it.ID]; !seen {
			g.order = append(g.order, it.ID)
		}
		g.items[it.ID] = &it
		g.queueLocked(it.work(g.spec.Workspace))
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
		case turn.Status != "completed":
			end.Outcome, end.Error = api.OutcomeFailed, fmt.Sprintf("Codex ended the turn as %q, which Glitched doesn't know.", turn.Status)
		}
		g.endLocked(end)
	}
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
			IsSecret bool   `json:"isSecret"`
			Options  []struct {
				Label       string `json:"label"`
				Description string `json:"description"`
			} `json:"options"`
		} `json:"questions"`
		Mode            string          `json:"mode"`
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
	if !g.started || g.ended || g.interrupted {
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
		a.deny = denial(r.Decisions)
	case "item/fileChange/requestApproval":
		b = body{Operation: "edit", Reason: r.Reason}
		if it := g.items[r.ItemID]; it != nil {
			b.Path = strings.Join(it.paths(g.spec.Workspace), ", ")
		}
	case "item/permissions/requestApproval":
		a.wanted = r.Permissions
		b = body{Operation: "get more access", Reason: r.Reason, Args: argsText(r.Permissions)}
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
	case "item/tool/requestUserInput":
		if len(r.Questions) == 1 && strings.HasPrefix(r.Questions[0].ID, toolApproval) {
			q := r.Questions[0]
			a.tool, a.ids = true, []string{q.ID}
			b = body{Operation: "use a tool", Reason: q.Question}
			if it := g.items[strings.TrimPrefix(strings.TrimPrefix(q.ID, toolApproval), "_")]; it != nil {
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

// respond sends a request's one reply. An approval allows only this once;
// Glitched never picks an answer that lasts past the call. A request
// already answered takes nothing more, and one Codex stopped waiting on
// takes no answer.
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
			Allow bool `json:"allow"`
		}
		if err := json.Unmarshal(r.Answer, &answer); err != nil {
			return fmt.Errorf("codex: an answer Codex can't take: %w", err)
		}
		reply = approve(a, answer.Allow)
	}
	a.state = answered
	return g.p.conn.reply(a.rpc, reply)
}

// approve is the answer to an approval request: allow or deny, once.
func approve(a *ask, allow bool) any {
	switch {
	case a.tool:
		pick := map[bool]string{true: toolAccept, false: toolRefuse}[allow]
		return map[string]any{"answers": map[string]any{a.ids[0]: map[string]any{"answers": []string{pick}}}}
	case a.method == "item/permissions/requestApproval":
		granted := json.RawMessage("{}")
		if allow && len(a.wanted) > 0 && string(a.wanted) != "null" {
			granted = a.wanted
		}
		return map[string]any{"permissions": granted, "scope": "turn"}
	case a.method == "mcpServer/elicitation/request":
		if allow {
			return map[string]any{"action": "accept", "content": map[string]any{}}
		}
		return map[string]any{"action": "decline", "content": nil}
	}
	if !allow && a.deny != "" {
		return map[string]any{"decision": a.deny}
	}
	return map[string]any{"decision": map[bool]string{true: "accept", false: "decline"}[allow]}
}

// denial is the decision that denies a command once: decline, unless
// Codex offers only cancel, which also ends the turn.
func denial(offered []json.RawMessage) string {
	var cancel bool
	for _, d := range offered {
		switch string(bytes.TrimSpace(d)) {
		case `"decline"`:
			return "decline"
		case `"cancel"`:
			cancel = true
		}
	}
	if cancel {
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

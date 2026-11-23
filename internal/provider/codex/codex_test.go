package codex

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/provider"
	"glitched/internal/provider/codex/codextest"
)

func TestMain(m *testing.M) {
	if codextest.Main() {
		return
	}
	os.Exit(m.Run())
}

// fake is an adapter whose codex is the test binary playing scenario, and
// the log the fake writes what it received to.
func fake(t *testing.T, scenario string) (*Adapter, string) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(t.TempDir(), "wire.jsonl")
	t.Setenv(codextest.Scenario, scenario)
	t.Setenv(codextest.Log, log)
	a := &Adapter{Path: self}
	t.Cleanup(func() {
		if err := a.Close(); err != nil {
			t.Errorf("close the adapter: %v", err)
		}
	})
	return a, log
}

func spec(t *testing.T) provider.SessionSpec {
	return provider.SessionSpec{
		Thread: api.NewID(), Run: api.NewID(), Generation: 1, Workspace: t.TempDir(),
		Model: api.ModelRef{Provider: "codex", Model: "gpt-6-luna", Effort: "low"}, Access: api.AccessAsk,
		MCP: []provider.MCPServer{
			{Name: "glitched", Command: "/bin/glitched", Args: []string{"mcp"}, Env: map[string]string{"GLITCHED_THREAD": "t"}, Instructions: "Use delegate_task.", Trusted: true},
			{Name: "glitched-browser", Command: "/bin/glitched", Args: []string{"browser-mcp"}},
		},
	}
}

func open(t *testing.T, a *Adapter, s provider.SessionSpec) provider.Agent {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	g, err := a.Open(ctx, s)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return g
}

func do(t *testing.T, g provider.Agent, act provider.Action) {
	t.Helper()
	if err := g.Do(context.Background(), act); err != nil {
		t.Fatalf("%T: %v", act, err)
	}
}

func next(t *testing.T, g provider.Agent) provider.Event {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	e, err := g.Next(ctx)
	if err != nil {
		t.Fatalf("next: %v", err)
	}
	return e
}

// turn is every event of the run up to and with its TurnEnded.
func turn(t *testing.T, g provider.Agent) []provider.Event {
	t.Helper()
	var out []provider.Event
	for {
		e := next(t, g)
		out = append(out, e)
		if _, ok := e.(provider.TurnEnded); ok {
			return out
		}
	}
}

// until is the run's events up to and with the first that ok takes.
func until[E provider.Event](t *testing.T, g provider.Agent) ([]provider.Event, E) {
	t.Helper()
	var out []provider.Event
	for {
		e := next(t, g)
		out = append(out, e)
		if x, ok := e.(E); ok {
			return out, x
		}
		if _, ok := e.(provider.TurnEnded); ok {
			t.Fatalf("the turn ended before a %T: %+v", *new(E), out)
		}
	}
}

func text(events []provider.Event) string {
	var b strings.Builder
	for _, e := range events {
		if d, ok := e.(provider.TextDelta); ok {
			b.WriteString(d.Text)
		}
	}
	return b.String()
}

func ended(events []provider.Event) provider.TurnEnded {
	return events[len(events)-1].(provider.TurnEnded)
}

func closeRun(t *testing.T, g provider.Agent) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := g.Close(ctx); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func wire(t *testing.T, log string) []codextest.Entry {
	t.Helper()
	entries, err := codextest.Read(log)
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

// sent is the params of every message of method the fake received.
func sent(t *testing.T, log, method string) []json.RawMessage {
	t.Helper()
	var out []json.RawMessage
	for _, e := range wire(t, log) {
		if e.Method == method {
			out = append(out, e.Params)
		}
	}
	return out
}

func works(events []provider.Event) []provider.Work {
	var out []provider.Work
	for _, e := range events {
		if w, ok := e.(provider.Work); ok {
			out = append(out, w)
		}
	}
	return out
}

// A turn streams Codex's text and ends Completed, and the thread and turn
// carry what the run asked for: the workspace, the model and its effort,
// glitched mcp trusted with its instructions, the browser asking as
// Codex's own tools do.
func TestATurnStreamsCodexsText(t *testing.T) {
	a, log := fake(t, "hi")
	s := spec(t)
	g := open(t, a, s)
	do(t, g, provider.Start{Text: "say hi"})
	events := turn(t, g)
	closeRun(t, g)
	if end := ended(events); end.Outcome != api.OutcomeCompleted {
		t.Fatalf("the turn ended %+v", end)
	}
	if got := text(events); !strings.HasPrefix(got, "hi 1 from ") {
		t.Errorf("text %q", got)
	}
	var init struct {
		ClientInfo   struct{ Name string }
		Capabilities struct {
			ExperimentalAPI bool `json:"experimentalApi"`
		}
	}
	json.Unmarshal(sent(t, log, "initialize")[0], &init)
	if init.ClientInfo.Name != "glitched" || !init.Capabilities.ExperimentalAPI {
		t.Errorf("initialize %+v", init)
	}
	var start struct {
		Cwd, Model, ApprovalPolicy, Sandbox, DeveloperInstructions string
		Config                                                     struct {
			MCPServers map[string]map[string]any `json:"mcp_servers"`
		}
	}
	json.Unmarshal(sent(t, log, "thread/start")[0], &start)
	if start.Cwd != s.Workspace || start.Model != "gpt-6-luna" || start.ApprovalPolicy != "on-request" || start.Sandbox != "read-only" {
		t.Errorf("thread/start %+v", start)
	}
	if start.DeveloperInstructions != "Use delegate_task." {
		t.Errorf("developer instructions %q", start.DeveloperInstructions)
	}
	glitched, browser := start.Config.MCPServers["glitched"], start.Config.MCPServers["glitched-browser"]
	if glitched["command"] != "/bin/glitched" || glitched["default_tools_approval_mode"] != "approve" {
		t.Errorf("glitched mcp %+v", glitched)
	}
	if env, _ := glitched["env"].(map[string]any); env["GLITCHED_THREAD"] != "t" {
		t.Errorf("glitched mcp env %+v", glitched["env"])
	}
	if _, trusted := browser["default_tools_approval_mode"]; trusted || browser["command"] != "/bin/glitched" {
		t.Errorf("the browser %+v", browser)
	}
	var ts struct {
		ThreadID, Model, Effort, ApprovalPolicy, Summary string
		SandboxPolicy                                    struct{ Type string }
		Input                                            []struct{ Type, Text string }
	}
	json.Unmarshal(sent(t, log, "turn/start")[0], &ts)
	if ts.ThreadID == "" || ts.Model != "gpt-6-luna" || ts.Effort != "low" || ts.ApprovalPolicy != "on-request" || ts.SandboxPolicy.Type != "readOnly" || ts.Summary != "auto" {
		t.Errorf("turn/start %+v", ts)
	}
	if len(ts.Input) != 1 || ts.Input[0].Type != "text" || ts.Input[0].Text != "say hi" {
		t.Errorf("turn/start input %+v", ts.Input)
	}
}

// The thread's next run goes to the same app-server and Codex thread,
// with no new thread started.
func TestAThreadKeepsItsAppServer(t *testing.T) {
	a, log := fake(t, "hi")
	s := spec(t)
	var texts []string
	for range 2 {
		s.Run = api.NewID()
		g := open(t, a, s)
		do(t, g, provider.Start{Text: "hi"})
		texts = append(texts, text(turn(t, g)))
		closeRun(t, g)
	}
	pid := strings.TrimPrefix(texts[0], "hi 1 from ")
	if texts[1] != "hi 2 from "+pid {
		t.Errorf("the second turn said %q after %q", texts[1], texts[0])
	}
	if n := len(sent(t, log, "thread/start")); n != 1 {
		t.Errorf("%d threads started", n)
	}
}

// Describe lists the account's models, Codex's default first, each with
// its efforts, and none Codex hides, across every page.
func TestDescribeListsModelsAndEfforts(t *testing.T) {
	a, log := fake(t, "hi")
	info, err := a.Describe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.Name != "Codex" || info.Version != "0.171.0" {
		t.Errorf("info %+v", info)
	}
	var got []string
	for _, m := range info.Models {
		var efforts []string
		for _, e := range m.Efforts {
			efforts = append(efforts, e.ID)
		}
		got = append(got, m.ID+"="+m.Name+":"+strings.Join(efforts, ","))
	}
	if want := []string{"gpt-6.2-sol=GPT-6.2 Sol:low,medium,high,xhigh", "gpt-6-luna=GPT-6 Luna:low,medium"}; !slices.Equal(got, want) {
		t.Errorf("models %q, want %q", got, want)
	}
	if n := len(sent(t, log, "turn/start")); n != 0 {
		t.Errorf("Describe started %d turns", n)
	}
}

// Signed out, Describe lists nothing and Open runs nothing: both say how
// to sign in, though Codex would list its own catalog.
func TestSignedOut(t *testing.T) {
	a, log := fake(t, "signed-out")
	info, err := a.Describe(context.Background())
	if !errors.Is(err, provider.ErrSignedOut) || !strings.Contains(err.Error(), "codex login") || len(info.Models) != 0 {
		t.Errorf("describe: %+v, %v", info, err)
	}
	_, err = a.Open(context.Background(), spec(t))
	if !errors.Is(err, provider.ErrSignedOut) {
		t.Errorf("open: %v", err)
	}
	if n := len(sent(t, log, "model/list")) + len(sent(t, log, "thread/start")); n != 0 {
		t.Errorf("signed out, it still listed or started %d", n)
	}
}

// A turn Codex ends unauthorized says how to sign in.
func TestAnUnauthorizedTurnSaysHowToSignIn(t *testing.T) {
	a, _ := fake(t, "unauthorized")
	g := open(t, a, spec(t))
	do(t, g, provider.Start{Text: "hi"})
	if end := ended(turn(t, g)); end.Outcome != api.OutcomeFailed || end.Error != signIn {
		t.Errorf("the turn ended %+v", end)
	}
	closeRun(t, g)
}

// A failed turn says what Codex said.
func TestAFailedTurnSaysWhy(t *testing.T) {
	a, _ := fake(t, "turn-failed")
	g := open(t, a, spec(t))
	do(t, g, provider.Start{Text: "hi"})
	if end := ended(turn(t, g)); end.Outcome != api.OutcomeFailed || end.Error != "You've hit your usage limit." {
		t.Errorf("the turn ended %+v", end)
	}
	closeRun(t, g)
}

// Two messages of one turn don't run together, and a message Codex sends
// whole, with no deltas, still reaches the run.
func TestMessagesStayApart(t *testing.T) {
	for scenario, want := range map[string]string{"two-messages": "first\n\nsecond", "whole": "all at once"} {
		a, _ := fake(t, scenario)
		g := open(t, a, spec(t))
		do(t, g, provider.Start{Text: "hi"})
		if got := text(turn(t, g)); got != want {
			t.Errorf("%s: text %q, want %q", scenario, got, want)
		}
		closeRun(t, g)
	}
}

// What Codex answered a request of the fake's, as the log has it.
func answers(t *testing.T, log string) []json.RawMessage {
	t.Helper()
	var out []json.RawMessage
	for _, e := range wire(t, log) {
		if e.Method != "answer" {
			continue
		}
		var m struct {
			Result json.RawMessage
			Error  json.RawMessage
		}
		json.Unmarshal(e.Params, &m)
		out = append(out, append(m.Result, m.Error...))
	}
	return out
}

func compact(raw json.RawMessage) string {
	var v any
	json.Unmarshal(raw, &v)
	b, _ := json.Marshal(v)
	return string(b)
}

// approvalRun opens Codex's card in scenario, answers it, and returns the
// card's body, the run's events and what Codex was answered.
func approvalRun(t *testing.T, scenario string, allow bool) (map[string]string, []provider.Event, string) {
	t.Helper()
	a, log := fake(t, scenario)
	g := open(t, a, spec(t))
	do(t, g, provider.Start{Text: "go"})
	_, req := until[provider.RequestOpened](t, g)
	if req.Kind != api.KindApproval {
		t.Fatalf("a %s request", req.Kind)
	}
	var b map[string]string
	json.Unmarshal(req.Body, &b)
	do(t, g, provider.Respond{Request: req.ID, Answer: json.RawMessage(`{"allow":` + strconv.FormatBool(allow) + "}")})
	events := turn(t, g)
	closeRun(t, g)
	got := answers(t, log)
	if len(got) != 1 {
		t.Fatalf("answers %s", got)
	}
	return b, events, compact(got[0])
}

// A command Codex wants to run shows Glitched's card with the command and
// where, and the user's answer goes back once: accept or decline.
func TestACommandAsksTheUser(t *testing.T) {
	for _, allow := range []bool{true, false} {
		b, events, answer := approvalRun(t, "command", allow)
		if b["command"] != "ls -a" || b["cwd"] == "" || b["reason"] != "list the files" {
			t.Errorf("card %+v", b)
		}
		want := map[bool]string{true: `{"decision":"accept"}`, false: `{"decision":"decline"}`}[allow]
		if answer != want {
			t.Errorf("allow %v: answered %s, want %s", allow, answer, want)
		}
		ws := works(events)
		last := ws[len(ws)-1]
		if allow && (last.Status != api.WorkCompleted || last.Exit == nil || *last.Exit != 0 || last.Output != "a.txt\n" || last.Kind != "command") {
			t.Errorf("the step %+v", last)
		}
		if !allow && last.Status != api.WorkFailed {
			t.Errorf("the declined step %+v", last)
		}
	}
}

// A file change shows the paths of the change Codex announced, though
// its request names none.
func TestAFileChangeShowsItsPaths(t *testing.T) {
	b, _, answer := approvalRun(t, "file", true)
	if b["operation"] != "edit" || b["path"] != "a.txt" || b["reason"] != "write a.txt" {
		t.Errorf("card %+v", b)
	}
	if answer != `{"decision":"accept"}` {
		t.Errorf("answered %s", answer)
	}
}

// More access is granted for the turn only, or not at all.
func TestMoreAccessIsForTheTurn(t *testing.T) {
	for allow, want := range map[bool]string{
		true:  `{"permissions":{"network":{"enabled":true}},"scope":"turn"}`,
		false: `{"permissions":{},"scope":"turn"}`,
	} {
		b, _, answer := approvalRun(t, "permissions", allow)
		if b["reason"] != "needs the network" || !strings.Contains(b["args"], "network") {
			t.Errorf("card %+v", b)
		}
		if answer != want {
			t.Errorf("allow %v: answered %s, want %s", allow, answer, want)
		}
	}
}

// An MCP tool Codex asks to approve, as a question, shows the approval
// card with the tool and its arguments; Allow is Codex's Allow, never for
// the session, and Deny its Cancel.
func TestAnMCPToolAsksOnTheApprovalCard(t *testing.T) {
	for allow, pick := range map[bool]string{true: "Allow", false: "Cancel"} {
		b, _, answer := approvalRun(t, "mcp-approval", allow)
		if b["operation"] != "use glitched-browser: navigate" || !strings.Contains(b["args"], "https://example.com") {
			t.Errorf("card %+v", b)
		}
		if want := `{"answers":{"mcp_tool_call_approval_call-1":{"answers":["` + pick + `"]}}}`; answer != want {
			t.Errorf("allow %v: answered %s, want %s", allow, answer, want)
		}
	}
}

// An elicitation that asks for nothing is an approval; one that asks for
// fields is declined, and the run says why.
func TestElicitations(t *testing.T) {
	b, _, answer := approvalRun(t, "elicitation", true)
	if b["operation"] != "answer glitched-browser" || b["reason"] != "Open the page?" {
		t.Errorf("card %+v", b)
	}
	if answer != `{"action":"accept","content":{}}` {
		t.Errorf("answered %s", answer)
	}
	a, log := fake(t, "elicitation-fields")
	g := open(t, a, spec(t))
	do(t, g, provider.Start{Text: "go"})
	events := turn(t, g)
	closeRun(t, g)
	for _, e := range events {
		if _, ok := e.(provider.RequestOpened); ok {
			t.Errorf("a card for a form: %+v", e)
		}
	}
	if got := answers(t, log); len(got) != 1 || compact(got[0]) != `{"action":"decline","content":null}` {
		t.Errorf("answered %s", got)
	}
	if ws := works(events); len(ws) == 0 || !strings.Contains(ws[0].Title, "can't show") {
		t.Errorf("no notice: %+v", ws)
	}
}

// A question goes to the question card, and the picks back by Codex's
// question id; a dismissal answers nothing.
func TestAQuestionAsksTheUser(t *testing.T) {
	for _, tc := range []struct{ answer, want string }{
		{`{"answers":[{"picked":["Blue"]}]}`, `{"answers":{"color":{"answers":["Blue"]}}}`},
		{`{"answers":[{"other":"teal"}]}`, `{"answers":{"color":{"answers":["teal"]}}}`},
		{`{"answers":[]}`, `{"answers":{}}`},
	} {
		a, log := fake(t, "question")
		g := open(t, a, spec(t))
		do(t, g, provider.Start{Text: "go"})
		_, req := until[provider.RequestOpened](t, g)
		q, err := api.ParseQuestions(req.Body)
		if req.Kind != api.KindQuestion || err != nil || q.Questions[0].Text != "Which color?" || q.Questions[0].Header != "Color" ||
			q.Questions[0].Options[1].Label != "Blue" || q.Questions[0].Options[1].About != "cool" {
			t.Fatalf("question %s %s: %v", req.Kind, req.Body, err)
		}
		do(t, g, provider.Respond{Request: req.ID, Answer: json.RawMessage(tc.answer)})
		turn(t, g)
		closeRun(t, g)
		if got := answers(t, log); len(got) != 1 || compact(got[0]) != tc.want {
			t.Errorf("%s: answered %s, want %s", tc.answer, got, tc.want)
		}
	}
}

// A question the card can't show is answered empty at once, and the run
// says why.
func TestAQuestionTheCardCantShow(t *testing.T) {
	a, log := fake(t, "question-unshowable")
	g := open(t, a, spec(t))
	do(t, g, provider.Start{Text: "go"})
	events := turn(t, g)
	closeRun(t, g)
	for _, e := range events {
		if _, ok := e.(provider.RequestOpened); ok {
			t.Errorf("a card: %+v", e)
		}
	}
	if got := answers(t, log); len(got) != 1 || compact(got[0]) != `{"answers":{}}` {
		t.Errorf("answered %s", got)
	}
	if ws := works(events); len(ws) == 0 || !strings.Contains(ws[0].Title, "can't show") {
		t.Errorf("no notice: %+v", ws)
	}
}

// A request Glitched doesn't know is refused, so Codex never waits on it.
func TestAnUnknownRequestIsRefused(t *testing.T) {
	a, log := fake(t, "unknown-request")
	g := open(t, a, spec(t))
	do(t, g, provider.Start{Text: "go"})
	events := turn(t, g)
	closeRun(t, g)
	if end := ended(events); end.Outcome != api.OutcomeCompleted || text(events) != "still here" {
		t.Errorf("the turn %+v", events)
	}
	if got := answers(t, log); len(got) != 1 || !strings.Contains(string(got[0]), "-32601") {
		t.Errorf("answered %s", got)
	}
}

// A request Codex settled itself withdraws its card.
func TestARequestResolvedElsewhereIsWithdrawn(t *testing.T) {
	a, _ := fake(t, "resolved")
	g := open(t, a, spec(t))
	do(t, g, provider.Start{Text: "go"})
	_, req := until[provider.RequestOpened](t, g)
	_, gone := until[provider.RequestWithdrawn](t, g)
	if gone.ID != req.ID || gone.Reason != "Codex no longer asks." {
		t.Errorf("withdrawn %+v for %s", gone, req.ID)
	}
	if err := g.Do(context.Background(), provider.Respond{Request: req.ID, Answer: json.RawMessage(`{"allow":true}`)}); !errors.Is(err, provider.ErrWithdrawn) {
		t.Errorf("an answer to it: %v", err)
	}
	turn(t, g)
	closeRun(t, g)
}

// Stop interrupts the turn, and withdraws the card open in it; the next
// run starts another app-server, as one stopped may still be at work.
func TestStopInterruptsTheTurn(t *testing.T) {
	a, log := fake(t, "hang")
	s := spec(t)
	g := open(t, a, s)
	do(t, g, provider.Start{Text: "go"})
	until[provider.TextDelta](t, g)
	do(t, g, provider.Interrupt{})
	if end := ended(turn(t, g)); end.Outcome != api.OutcomeInterrupted {
		t.Errorf("the turn ended %+v", end)
	}
	closeRun(t, g)
	var in struct{ ThreadID, TurnID string }
	if got := sent(t, log, "turn/interrupt"); len(got) != 1 || json.Unmarshal(got[0], &in) != nil || in.TurnID != "turn-1" || in.ThreadID == "" {
		t.Errorf("turn/interrupt %s", got)
	}
	s.Run = api.NewID()
	g = open(t, a, s)
	closeRun(t, g)
	if n := len(sent(t, log, "initialize")); n != 2 {
		t.Errorf("%d app-servers for two runs, the first stopped", n)
	}
}

// A Stop right after Start waits for Codex to say the turn started: an
// interrupt before then reaches no turn, which would run on.
func TestAStopBeforeTheTurnStartsWaitsForIt(t *testing.T) {
	a, log := fake(t, "late-started")
	g := open(t, a, spec(t))
	do(t, g, provider.Start{Text: "go"})
	do(t, g, provider.Interrupt{})
	events := turn(t, g)
	closeRun(t, g)
	if end := ended(events); end.Outcome != api.OutcomeInterrupted || strings.Contains(text(events), "ran to its end") {
		t.Errorf("the turn %+v", events)
	}
	if got := sent(t, log, "refused"); len(got) != 0 {
		t.Errorf("Codex refused %s", got)
	}
}

// A message mid-turn steers it.
func TestAMessageMidTurnSteers(t *testing.T) {
	a, log := fake(t, "steer")
	g := open(t, a, spec(t))
	do(t, g, provider.Start{Text: "go"})
	until[provider.TextDelta](t, g)
	do(t, g, provider.Steer{Text: "and more"})
	events := turn(t, g)
	closeRun(t, g)
	if got := text(events); got != "\n\nsteered: and more" {
		t.Errorf("text %q", got)
	}
	var st struct {
		ExpectedTurnID string `json:"expectedTurnId"`
		Input          []struct{ Text string }
	}
	if got := sent(t, log, "turn/steer"); len(got) != 1 || json.Unmarshal(got[0], &st) != nil || st.ExpectedTurnID != "turn-1" || st.Input[0].Text != "and more" {
		t.Errorf("turn/steer %s", got)
	}
}

// A steer once the turn is over fails, and says so.
func TestASteerAfterTheTurnFails(t *testing.T) {
	a, _ := fake(t, "hi")
	g := open(t, a, spec(t))
	do(t, g, provider.Start{Text: "go"})
	turn(t, g)
	if err := g.Do(context.Background(), provider.Steer{Text: "late"}); err == nil {
		t.Error("a steer after the turn went through")
	}
	closeRun(t, g)
}

// An app-server that dies mid-turn fails it with what it printed last.
func TestACrashFailsTheTurn(t *testing.T) {
	a, _ := fake(t, "crash")
	g := open(t, a, spec(t))
	do(t, g, provider.Start{Text: "go"})
	if end := ended(turn(t, g)); end.Outcome != api.OutcomeFailed || !strings.Contains(end.Error, "boom: the fake crashed") {
		t.Errorf("the turn ended %+v", end)
	}
	closeRun(t, g)
}

// An app-server that speaks of a turn once it was parked is stopped, and
// the thread's next run gets another.
func TestAnAppServerThatSpeaksWhileParkedIsStopped(t *testing.T) {
	a, log := fake(t, "late-speech")
	s := spec(t)
	g := open(t, a, s)
	do(t, g, provider.Start{Text: "go"})
	turn(t, g)
	closeRun(t, g)
	time.Sleep(time.Second)
	s.Run = api.NewID()
	g = open(t, a, s)
	do(t, g, provider.Start{Text: "go"})
	got := text(turn(t, g))
	closeRun(t, g)
	if !strings.HasPrefix(got, "hi 1 from ") {
		t.Errorf("the next run went to the app-server that spoke: %q", got)
	}
	if n := len(sent(t, log, "initialize")); n != 2 {
		t.Errorf("%d app-servers", n)
	}
}

// With no codex found, Open says how to install it.
func TestAMissingCodexSaysHowToInstall(t *testing.T) {
	a := &Adapter{Path: filepath.Join(t.TempDir(), "no-codex")}
	if _, err := a.Open(context.Background(), spec(t)); err == nil || !strings.Contains(err.Error(), "npm install -g @openai/codex") {
		t.Errorf("open: %v", err)
	}
}

// A test can't run the user's real codex.
func TestATestCantRunTheRealCodex(t *testing.T) {
	defer func() {
		if r := recover(); r == nil || !strings.Contains(r.(string), "codexlive") {
			t.Errorf("recovered %v", r)
		}
	}()
	guard("/bin/sh")
}

// What Codex says of a turn that ended reaches no later run on the same
// app-server: an old ask is cancelled, unseen, and old text is dropped,
// though both come before Codex names the new turn.
func TestAnOldTurnReachesNoLaterRun(t *testing.T) {
	a, log := fake(t, "stale")
	s := spec(t)
	var second []provider.Event
	for i := range 2 {
		s.Run = api.NewID()
		g := open(t, a, s)
		do(t, g, provider.Start{Text: "go"})
		events := turn(t, g)
		closeRun(t, g)
		if i == 1 {
			second = events
		}
	}
	for _, e := range second {
		if _, ok := e.(provider.RequestOpened); ok {
			t.Fatalf("the second run showed the first turn's ask: %+v", e)
		}
	}
	if got := text(second); got != "new text, old ask cancel" {
		t.Errorf("the second run said %q", got)
	}
	if got := answers(t, log); len(got) != 1 || compact(got[0]) != `{"decision":"cancel"}` {
		t.Errorf("answered %s", got)
	}
}

// A command that asks for more than the sandbox gives shows what on the
// card, and Deny is the denial Codex offers.
func TestACommandShowsTheAccessItAsksFor(t *testing.T) {
	b, _, answer := approvalRun(t, "network", false)
	if !strings.Contains(b["reason"], "check status") || !strings.Contains(b["reason"], "status.example") {
		t.Errorf("card %+v", b)
	}
	if answer != `{"decision":"cancel"}` {
		t.Errorf("denied with %s", answer)
	}
}

// Fast is Codex's priority tier, and off is no tier, on every turn.
func TestFastIsCodexsPriorityTier(t *testing.T) {
	a, log := fake(t, "hi")
	s := spec(t)
	for _, fast := range []bool{true, false} {
		s.Run, s.Model.Fast = api.NewID(), fast
		g := open(t, a, s)
		do(t, g, provider.Start{Text: "hi"})
		turn(t, g)
		closeRun(t, g)
	}
	var tiers []string
	for _, p := range sent(t, log, "turn/start") {
		var v struct {
			ServiceTier       *string `json:"serviceTier"`
			ApprovalsReviewer string  `json:"approvalsReviewer"`
		}
		json.Unmarshal(p, &v)
		if !strings.Contains(string(p), `"serviceTier"`) || v.ApprovalsReviewer != "user" {
			t.Errorf("turn/start %s", p)
		}
		tier := "null"
		if v.ServiceTier != nil {
			tier = *v.ServiceTier
		}
		tiers = append(tiers, tier)
	}
	if strings.Join(tiers, " ") != "priority null" {
		t.Errorf("tiers %q", tiers)
	}
}

// An answer whose context ended before it went reaches Codex not at all,
// and the card still waits on one.
func TestAnAnswerTooLateIsNotSent(t *testing.T) {
	a, log := fake(t, "command")
	g := open(t, a, spec(t))
	do(t, g, provider.Start{Text: "go"})
	_, req := until[provider.RequestOpened](t, g)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := g.Do(ctx, provider.Respond{Request: req.ID, Answer: json.RawMessage(`{"allow":true}`)}); err == nil {
		t.Fatal("a cancelled answer was taken")
	}
	do(t, g, provider.Respond{Request: req.ID, Answer: json.RawMessage(`{"allow":false}`)})
	turn(t, g)
	closeRun(t, g)
	if got := answers(t, log); len(got) != 1 || compact(got[0]) != `{"decision":"decline"}` {
		t.Errorf("answered %s", got)
	}
}

// Nothing waits on app-server reading what it is sent: a reply to one
// that stopped reading returns at once.
func TestSendingNeverWaitsOnAppServer(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	c := newConn(w, func(message) {})
	defer c.end()
	big := strings.Repeat("x", 1<<20)
	done := make(chan struct{})
	go func() {
		for range 4 {
			c.reply(json.RawMessage("1"), big)
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a reply waited on a pipe nobody reads")
	}
}

// A thread whose engine restarted goes on with its Codex conversation: a
// new app-server resumes the thread its earlier runs had, and starts none.
func TestAThreadResumesItsConversation(t *testing.T) {
	a, log := fake(t, "memory")
	s := spec(t)
	g := open(t, a, s)
	do(t, g, provider.Start{Text: "Remember kumquat."})
	turn(t, g)
	session := g.(provider.Sessioned).Session()
	closeRun(t, g)
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	b := &Adapter{Path: a.Path}
	defer b.Close()
	s.Run, s.Session = api.NewID(), session
	g = open(t, b, s)
	do(t, g, provider.Start{Text: "Which word?"})
	events := turn(t, g)
	closeRun(t, g)
	if got := text(events); got != "earlier you said: Remember kumquat." {
		t.Errorf("after a restart Codex said %q", got)
	}
	if len(works(events)) != 0 {
		t.Errorf("steps %+v", works(events))
	}
	if got := g.(provider.Sessioned).Session(); got != session {
		t.Errorf("the session went from %q to %q", session, got)
	}
	resumed := sent(t, log, "thread/resume")
	if len(resumed) != 1 || len(sent(t, log, "thread/start")) != 1 {
		t.Fatalf("resumed %s, started %d", resumed, len(sent(t, log, "thread/start")))
	}
	var p struct {
		ThreadID       string
		ExcludeTurns   bool
		Cwd            string
		ApprovalPolicy string
		Config         struct {
			MCPServers map[string]any `json:"mcp_servers"`
		}
		DeveloperInstructions string
	}
	json.Unmarshal(resumed[0], &p)
	if p.ThreadID != session || !p.ExcludeTurns || p.Cwd != s.Workspace || p.ApprovalPolicy != askPolicy || len(p.Config.MCPServers) != 2 || p.DeveloperInstructions == "" {
		t.Errorf("thread/resume %s", resumed[0])
	}
}

// A conversation Codex no longer has starts a new one, and the turn says
// so.
func TestALostConversationStartsANewOne(t *testing.T) {
	a, log := fake(t, "memory")
	s := spec(t)
	s.Session = "019a0000-0000-7000-8000-gone"
	g := open(t, a, s)
	do(t, g, provider.Start{Text: "hi"})
	events := turn(t, g)
	closeRun(t, g)
	ws := works(events)
	if len(ws) != 1 || !strings.Contains(ws[0].Title, "no longer has this thread's earlier conversation") {
		t.Errorf("steps %+v", ws)
	}
	if got := g.(provider.Sessioned).Session(); got == s.Session || got == "" {
		t.Errorf("the session is %q", got)
	}
	if len(sent(t, log, "thread/start")) != 1 {
		t.Error("no new thread started")
	}
}

// A conversation open in another Codex client fails the run, rather than
// splitting it in two.
func TestAConversationOpenElsewhereFailsTheRun(t *testing.T) {
	a, log := fake(t, "writer")
	s := spec(t)
	s.Session = "019a0000-0000-7000-8000-elsewhere"
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	g, err := a.Open(ctx, s)
	if err == nil {
		g.Close(ctx)
		t.Fatal("the run opened")
	}
	if err.Error() != inUse {
		t.Errorf("Open: %v", err)
	}
	if len(sent(t, log, "thread/start")) != 0 {
		t.Error("a new thread started")
	}
}

// A parked app-server on another conversation than the run's is the
// thread's no longer: it is stopped, and a new one resumes the run's.
func TestAParkedAppServerOnAnotherConversationIsLeft(t *testing.T) {
	a, log := fake(t, "memory")
	s := spec(t)
	g := open(t, a, s)
	do(t, g, provider.Start{Text: "one"})
	turn(t, g)
	first := g.(provider.Sessioned).Session()
	closeRun(t, g)
	b := &Adapter{Path: a.Path}
	other := open(t, b, spec(t))
	do(t, other, provider.Start{Text: "two"})
	turn(t, other)
	second := other.(provider.Sessioned).Session()
	closeRun(t, other)
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	s.Run, s.Session = api.NewID(), second
	g = open(t, a, s)
	do(t, g, provider.Start{Text: "three"})
	got := text(turn(t, g))
	closeRun(t, g)
	if first == second || got != "earlier you said: two" {
		t.Errorf("sessions %q %q, said %q", first, second, got)
	}
	if n := len(sent(t, log, "thread/resume")); n != 1 {
		t.Errorf("%d resumes", n)
	}
}

// A conversation Codex refuses for now, or answers with another, fails
// the run: the thread keeps its conversation for the next one.
func TestAResumeCodexRefusesKeepsTheConversation(t *testing.T) {
	for _, scenario := range []string{"closing", "other-id"} {
		a, log := fake(t, scenario)
		s := spec(t)
		s.Session = "019a0000-0000-7000-8000-kept"
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		g, err := a.Open(ctx, s)
		if err == nil {
			g.Close(ctx)
			t.Errorf("%s: the run opened", scenario)
		}
		cancel()
		if len(sent(t, log, "thread/start")) != 0 {
			t.Errorf("%s: a new thread started", scenario)
		}
	}
}

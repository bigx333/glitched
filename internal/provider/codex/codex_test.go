package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
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
	if path := os.Getenv(envLaunch); path != "" {
		noteLaunch(path)
		if slices.Contains(os.Args[1:], "--version") {
			fmt.Println("codex-cli 0.200.0")
			os.Exit(0)
		}
	}
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
// its efforts and the one Codex runs it at where a thread names none, and
// none Codex hides, across every page. Codex's default is the Default.
func TestDescribeListsModelsAndEfforts(t *testing.T) {
	a, log := fake(t, "hi")
	info, err := a.Describe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.Name != "Codex" || info.Version != "0.171.0" || info.Path != a.Path {
		t.Errorf("info %+v", info)
	}
	var got []string
	for _, m := range info.Models {
		var efforts, speeds []string
		for _, e := range m.Efforts {
			efforts = append(efforts, e.ID)
		}
		for _, speed := range m.Speeds {
			speeds = append(speeds, string(speed))
		}
		got = append(got, fmt.Sprintf("%s=%s:%s@%s default=%t speeds=%s:%t", m.ID, m.Name, strings.Join(efforts, ","), m.DefaultEffort, m.Default, strings.Join(speeds, ","), m.SpeedsKnown))
	}
	if want := []string{"gpt-6.2-sol=GPT-6.2 Sol:low,medium,high,xhigh@medium default=true speeds=fast,ultrafast:true", "gpt-6-luna=GPT-6 Luna:low,medium@medium default=false speeds=fast:true"}; !slices.Equal(got, want) {
		t.Errorf("models %q, want %q", got, want)
	}
	if n := len(sent(t, log, "turn/start")); n != 0 {
		t.Errorf("Describe started %d turns", n)
	}
}

// A Codex whose launch setting names its CODEX_HOME runs the model that
// folder's config names, though the engine's own CODEX_HOME names
// another: Describe's Default is that one.
func TestDescribesDefaultIsTheLaunchFoldersConfig(t *testing.T) {
	a, _ := fake(t, "hi")
	engines, home := t.TempDir(), t.TempDir()
	t.Setenv("CODEX_HOME", engines)
	os.WriteFile(filepath.Join(engines, "config.toml"), []byte("model = \"gpt-6.2-sol\"\n"), 0o600)
	os.WriteFile(filepath.Join(home, "config.toml"), []byte("model = \"gpt-6-luna\"\n"), 0o600)
	a.Place(provider.Launch{Path: a.Path, Home: home})
	info, err := a.Describe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var defaults []string
	for _, m := range info.Models {
		if m.Default {
			defaults = append(defaults, m.ID)
		}
	}
	if !slices.Equal(defaults, []string{"gpt-6-luna"}) {
		t.Errorf("with the launch folder's config naming gpt-6-luna, the Default is %q", defaults)
	}
}

// The Default is the model a thread naming none runs: the one the user's
// config names, at its effort where it names one, though model/list marks
// another, and listed by its id where Codex doesn't list it; with no
// config/read, model/list's.
func TestDescribesDefaultIsTheConfigs(t *testing.T) {
	for scenario, want := range map[string]string{
		"configured":          "gpt-6-luna@low",
		"configured-unlisted": "gpt-7-preview@xhigh",
		"no-config-read":      "gpt-6.2-sol@medium",
		"hi":                  "gpt-6.2-sol@medium",
	} {
		a, _ := fake(t, scenario)
		info, err := a.Describe(context.Background())
		if err != nil {
			t.Fatalf("%s: %v", scenario, err)
		}
		var defaults []string
		for _, m := range info.Models {
			if m.Default {
				defaults = append(defaults, m.ID+"@"+m.DefaultEffort)
			}
		}
		if !slices.Equal(defaults, []string{want}) {
			t.Errorf("%s: the Default is %q, want %q", scenario, defaults, want)
		}
	}
}

// Signed out, Describe lists nothing and Open runs nothing: both say how
// to sign in, though Codex would list its own catalog.
func TestSignedOut(t *testing.T) {
	a, log := fake(t, "signed-out")
	info, err := a.Describe(context.Background())
	if !errors.Is(err, provider.ErrSignedOut) || err.Error() != signIn || len(info.Models) != 0 || info.Version != "0.171.0" {
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

// A turn a usage limit failed says when the spent window resets, from
// what Codex said of the account's windows before it, or when asked after;
// with neither, it says only that a limit failed it.
func TestALimitedTurnSaysWhenTheLimitResets(t *testing.T) {
	resets := time.Unix(1787199153, 0).UTC()
	for scenario, want := range map[string]api.UsageLimit{
		"limit-updated": {Resets: resets},
		"limit-read":    {Resets: resets},
		"turn-failed":   {},
	} {
		a, log := fake(t, scenario)
		g := open(t, a, spec(t))
		do(t, g, provider.Start{Text: "hi"})
		end := ended(turn(t, g))
		if end.Outcome != api.OutcomeFailed || end.Limit == nil || *end.Limit != want {
			t.Errorf("%s: the turn ended %+v with limit %v, want failed with %+v", scenario, end, end.Limit, want)
		}
		if asked := len(sent(t, log, "account/rateLimits/read")); asked != map[bool]int{true: 0, false: 1}[scenario == "limit-updated"] {
			t.Errorf("%s: asked Codex for the account's windows %d times", scenario, asked)
		}
		closeRun(t, g)
	}
}

// A limited turn whose windows Codex won't say still ends, once the read
// of them gives up, with a limit that doesn't say when it resets.
func TestALimitedTurnEndsWhenCodexWontSayWhenItResets(t *testing.T) {
	a, log := fake(t, "limit-silent")
	g := open(t, a, spec(t))
	do(t, g, provider.Start{Text: "hi"})
	end := ended(turn(t, g))
	if end.Outcome != api.OutcomeFailed || end.Limit == nil || !end.Limit.Resets.IsZero() {
		t.Errorf("the turn ended %+v with limit %v, want failed with a limit of no known reset", end, end.Limit)
	}
	if asked := len(sent(t, log, "account/rateLimits/read")); asked != 1 {
		t.Errorf("asked Codex for the account's windows %d times, want once", asked)
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

// Each update_plan is the run's plan, whole; the plan's item is no step.
func TestAPlanUpdateIsThePlan(t *testing.T) {
	a, _ := fake(t, "plan")
	g := open(t, a, spec(t))
	do(t, g, provider.Start{Text: "hi"})
	var plans []provider.Plan
	var work []provider.Work
	for _, e := range turn(t, g) {
		switch e := e.(type) {
		case provider.Plan:
			plans = append(plans, e)
		case provider.Work:
			work = append(work, e)
		}
	}
	closeRun(t, g)
	want := []provider.Plan{
		{Steps: []api.PlanStep{{Text: "Read the code", Status: api.PlanActive}, {Text: "Fix the bug", Status: api.PlanPending}}},
		{Steps: []api.PlanStep{{Text: "Read the code", Status: api.PlanDone}, {Text: "Fix the bug", Status: api.PlanActive}}},
	}
	if !reflect.DeepEqual(plans, want) || len(work) != 0 {
		t.Errorf("the turn reported plans %+v and steps %+v, want %+v and no step", plans, work, want)
	}
}

// A compaction leaves what the context holds unknown until Codex next
// says, whether it shows as an item or as the thread's news.
func TestACompactionReportsTheContextUnknown(t *testing.T) {
	a, _ := fake(t, "compacted")
	g := open(t, a, spec(t))
	do(t, g, provider.Start{Text: "hi"})
	var got []provider.Context
	for _, e := range turn(t, g) {
		if c, ok := e.(provider.Context); ok {
			got = append(got, c)
		}
	}
	closeRun(t, g)
	want := []provider.Context{{Compacted: true}, {Compacted: true}, {Used: 100, Window: 258400}}
	if !slices.Equal(got, want) {
		t.Errorf("the turn reported %+v, want %+v", got, want)
	}
}

// Codex resends the thread's last usage, as after a rate-limit update
// with no request since, or with a response cut short whose spending it
// adds but whose reading it keeps: neither is news of the turn's, whose
// model may not be the last one's.
func TestResentUsageIsNoReport(t *testing.T) {
	a, _ := fake(t, "resent")
	s := spec(t)
	var got [][]provider.Context
	for range 2 {
		g := open(t, a, s)
		do(t, g, provider.Start{Text: "hi"})
		var c []provider.Context
		for _, e := range turn(t, g) {
			if r, ok := e.(provider.Context); ok {
				c = append(c, r)
			}
		}
		got = append(got, c)
		closeRun(t, g)
		s.Run = api.NewID()
	}
	want := []provider.Context{{Used: 100, Window: 258400}}
	for i, c := range got {
		if !slices.Equal(c, want) {
			t.Errorf("turn %d reported %+v, want %+v", i+1, c, want)
		}
	}
}

// A request too big for the window has Codex fill it, with a usage of no
// input: the context is full, whatever the usage says it cost.
func TestAnOverflowShowsTheWindowFull(t *testing.T) {
	a, _ := fake(t, "overflow")
	g := open(t, a, spec(t))
	do(t, g, provider.Start{Text: "hi"})
	var got []provider.Context
	for _, e := range turn(t, g) {
		if c, ok := e.(provider.Context); ok {
			got = append(got, c)
		}
	}
	closeRun(t, g)
	if want := []provider.Context{{Used: 258400, Window: 258400}, {Used: 100, Window: 258400}}; !slices.Equal(got, want) {
		t.Errorf("the turn reported %+v, want %+v", got, want)
	}
}

// A run on a new app-server is a new Codex conversation, and says so: the
// last one's context is not this one's.
func TestANewAppServerIsANewSession(t *testing.T) {
	a, _ := fake(t, "whole")
	s := spec(t)
	var sessions []string
	for _, access := range []api.Access{api.AccessAsk, api.AccessAsk, api.AccessFull} {
		s.Run, s.Access = api.NewID(), access
		g := open(t, a, s)
		sessions = append(sessions, g.(provider.Sessioned).Session())
		do(t, g, provider.Start{Text: "hi"})
		turn(t, g)
		closeRun(t, g)
	}
	if sessions[0] == "" || sessions[1] != sessions[0] || sessions[2] == sessions[0] {
		t.Errorf("sessions %q: want one kept by the parked app-server, then a new one", sessions)
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
	body, events, answer := answerRun(t, scenario, `{"allow":`+strconv.FormatBool(allow)+"}")
	var b map[string]string
	json.Unmarshal(body, &b)
	return b, events, answer
}

// answerRun opens Codex's card in scenario, answers it with answer, and
// returns the card's body, the run's events and what Codex was answered.
func answerRun(t *testing.T, scenario, answer string) (json.RawMessage, []provider.Event, string) {
	t.Helper()
	a, log := fake(t, scenario)
	g := open(t, a, spec(t))
	do(t, g, provider.Start{Text: "go"})
	_, req := until[provider.RequestOpened](t, g)
	if req.Kind != api.KindApproval {
		t.Fatalf("a %s request", req.Kind)
	}
	do(t, g, provider.Respond{Request: req.ID, Answer: json.RawMessage(answer)})
	events := turn(t, g)
	closeRun(t, g)
	got := answers(t, log)
	if len(got) != 1 {
		t.Fatalf("answers %s", got)
	}
	return req.Body, events, compact(got[0])
}

// offersSession is whether a card's body offers Allow for this session.
func offersSession(body json.RawMessage) bool {
	var b struct{ Session bool }
	json.Unmarshal(body, &b)
	return b.Session
}

const sessionAnswer = `{"allow":true,"session":true}`

// Where Codex offers to allow such calls for its session, the card offers
// it too, and the answer goes back in Codex's own words: acceptForSession
// for a command or a file change, the session scope for more access, and
// Codex's own option or persist for an MCP tool.
func TestTheCardOffersCodexsSessionGrant(t *testing.T) {
	for scenario, want := range map[string]string{
		"network":                    `{"decision":"acceptForSession"}`,
		"network-escaped":            `{"decision":"acceptForSession"}`,
		"network-null":               `{"decision":"acceptForSession"}`,
		"network-absent":             `{"decision":"acceptForSession"}`,
		"file-no-reason":             `{"decision":"acceptForSession"}`,
		"permissions":                `{"permissions":{"network":{"enabled":true}},"scope":"session"}`,
		"mcp-approval":               `{"answers":{"mcp_tool_call_approval_call-1":{"answers":["Allow for this session"]}}}`,
		"elicitation-session":        `{"_meta":{"persist":"session"},"action":"accept","content":{}}`,
		"elicitation-session-always": `{"_meta":{"persist":"session"},"action":"accept","content":{}}`,
	} {
		body, _, answer := answerRun(t, scenario, sessionAnswer)
		if !offersSession(body) {
			t.Errorf("%s: card %s offers no session grant", scenario, body)
		}
		if answer != want {
			t.Errorf("%s: answered %s, want %s", scenario, answer, want)
		}
	}
}

// Where Codex offers no session grant the card offers none, an answer for
// the session fails, and Codex still gets the user's one answer: a
// command whose decisions leave it out, a file change Codex asks about
// with a reason, even an empty one, which it would ask about again, an MCP tool without the
// option or the persist, and an elicitation.
func TestNoSessionGrantWhereCodexOffersNone(t *testing.T) {
	for _, scenario := range []string{"command", "file", "file-empty-reason", "mcp-approval-once", "elicitation", "elicitation-always"} {
		a, log := fake(t, scenario)
		g := open(t, a, spec(t))
		do(t, g, provider.Start{Text: "go"})
		_, req := until[provider.RequestOpened](t, g)
		if offersSession(req.Body) {
			t.Errorf("%s: card %s offers a session grant", scenario, req.Body)
		}
		if err := g.Do(context.Background(), provider.Respond{Request: req.ID, Answer: json.RawMessage(sessionAnswer)}); err == nil {
			t.Errorf("%s: an answer for the session went through", scenario)
		}
		do(t, g, provider.Respond{Request: req.ID, Answer: json.RawMessage(`{"allow":true}`)})
		turn(t, g)
		closeRun(t, g)
		if got := answers(t, log); len(got) != 1 || strings.Contains(string(got[0]), "ession") {
			t.Errorf("%s: answered %s, want one answer for this call", scenario, got)
		}
	}
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

// Allow once grants more access for the turn only, and Deny none.
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

// An ordinary question whose id and options look like Codex's own MCP
// approval, but which takes the user's own answer, stays a question: it
// grants nothing, so the card offers no grant, and the pick goes back as
// the question's answer.
func TestAQuestionLikeAnApprovalStaysAQuestion(t *testing.T) {
	for _, scenario := range []string{"question-like-approval", "question-closed-like-approval"} {
		t.Run(scenario, func(t *testing.T) {
			a, log := fake(t, scenario)
			g := open(t, a, spec(t))
			do(t, g, provider.Start{Text: "go"})
			_, req := until[provider.RequestOpened](t, g)
			if req.Kind != api.KindQuestion || offersSession(req.Body) {
				t.Fatalf("a %s request %s, want a question", req.Kind, req.Body)
			}
			do(t, g, provider.Respond{Request: req.ID, Answer: json.RawMessage(`{"answers":[{"picked":["Allow for this session"]}]}`)})
			turn(t, g)
			closeRun(t, g)
			if got := answers(t, log); len(got) != 1 || compact(got[0]) != `{"answers":{"mcp_tool_call_approval_call-1":{"answers":["Allow for this session"]}}}` {
				t.Errorf("answered %s", got)
			}
		})
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
	if _, err := a.Open(context.Background(), spec(t)); !errors.Is(err, provider.ErrNotInstalled) || !strings.Contains(err.Error(), "npm install -g @openai/codex") {
		t.Errorf("open: %v", err)
	}
	if _, err := a.Describe(context.Background()); !errors.Is(err, provider.ErrNotInstalled) {
		t.Errorf("describe: %v", err)
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

// Fast is Codex's priority tier, ultrafast its ultrafast one, and off no
// tier, on every turn.
func TestSpeedIsCodexsServiceTier(t *testing.T) {
	a, log := fake(t, "hi")
	s := spec(t)
	for _, speed := range []api.Speed{api.SpeedFast, api.SpeedUltrafast, api.SpeedStandard} {
		s.Run = api.NewID()
		s.Model.SetSpeed(speed)
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
	if strings.Join(tiers, " ") != "priority ultrafast null" {
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
	if end := ended(events); end.Tokens != 100 {
		t.Errorf("the resumed turn used %d tokens, want its own 100", end.Tokens)
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
	if p.ThreadID != session || !p.ExcludeTurns || p.Cwd != s.Workspace || p.ApprovalPolicy != "on-request" || len(p.Config.MCPServers) != 2 || p.DeveloperInstructions == "" {
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

// Each access level reaches Codex as its approval policy and sandbox, at
// the thread's start and on each turn: Full access is Codex's own bypass.
func TestEachAccessLevelReachesCodex(t *testing.T) {
	for access, want := range map[api.Access][4]string{
		api.AccessAsk:       {"on-request", "read-only", "readOnly", "user"},
		api.AccessAutoEdits: {"on-request", "workspace-write", "workspaceWrite", "auto_review"},
		api.AccessFull:      {"never", "danger-full-access", "dangerFullAccess", "user"},
	} {
		a, log := fake(t, "hi")
		s := spec(t)
		s.Access = access
		g := open(t, a, s)
		do(t, g, provider.Start{Text: "hi"})
		turn(t, g)
		closeRun(t, g)
		var start struct{ ApprovalPolicy, Sandbox, ApprovalsReviewer string }
		json.Unmarshal(sent(t, log, "thread/start")[0], &start)
		var ts struct {
			ApprovalPolicy, ApprovalsReviewer string
			SandboxPolicy                     struct{ Type string }
		}
		json.Unmarshal(sent(t, log, "turn/start")[0], &ts)
		if got := [4]string{start.ApprovalPolicy, start.Sandbox, ts.SandboxPolicy.Type, start.ApprovalsReviewer}; got != want || ts.ApprovalPolicy != want[0] || ts.ApprovalsReviewer != want[3] {
			t.Errorf("access %v: %q and turn policy %q, reviewer %q, want %q", access, got, ts.ApprovalPolicy, ts.ApprovalsReviewer, want)
		}
	}
}

// A turn says what its model requests used: each of Codex's updates
// counts its own request.
func TestATurnSaysWhatItUsed(t *testing.T) {
	a, _ := fake(t, "hi")
	g := open(t, a, spec(t))
	do(t, g, provider.Start{Text: "hi"})
	end := ended(turn(t, g))
	closeRun(t, g)
	if end.Tokens != 100 {
		t.Errorf("the turn used %d tokens, want 100", end.Tokens)
	}
}

// Describe's answer stands until Forget, when Codex is asked again.
func TestForgetAsksCodexAgain(t *testing.T) {
	a, log := fake(t, "hi")
	for _, forget := range []bool{false, true} {
		if forget {
			a.Forget()
		}
		if _, err := a.Describe(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(sent(t, log, "account/read")); n != 2 {
		t.Errorf("%d probes, want 2: one before Forget and one after", n)
	}
}

// A turn used what each of its requests did: a reading Codex sends again
// unchanged adds nothing, and one after the turn ended charges no other
// turn and keeps the app-server.
func TestUsageCodexRepeatsCountsOnce(t *testing.T) {
	a, log := fake(t, "usage-again")
	s := spec(t)
	for range 2 {
		s.Run = api.NewID()
		g := open(t, a, s)
		do(t, g, provider.Start{Text: "hi"})
		end := ended(turn(t, g))
		time.Sleep(100 * time.Millisecond)
		closeRun(t, g)
		if end.Tokens != 140 {
			t.Errorf("the turn used %d tokens, want 140", end.Tokens)
		}
	}
	if n := len(sent(t, log, "initialize")); n != 1 {
		t.Errorf("%d app-servers started, want the one", n)
	}
}

// What Codex spends while the thread's app-server waits for its next run
// is no run's: the same reading sent again in that run adds nothing.
func TestUsageBetweenRunsChargesNoRun(t *testing.T) {
	a, log := fake(t, "parked-usage")
	s := spec(t)
	var used []int64
	for range 2 {
		s.Run = api.NewID()
		g := open(t, a, s)
		do(t, g, provider.Start{Text: "hi"})
		used = append(used, ended(turn(t, g)).Tokens)
		closeRun(t, g)
		time.Sleep(200 * time.Millisecond)
	}
	if used[0] != 100 || used[1] != 100 {
		t.Errorf("the turns used %v tokens, want 100 each", used)
	}
	if n := len(sent(t, log, "initialize")); n != 1 {
		t.Errorf("%d app-servers started, want the one", n)
	}
}

// A request cut short has Codex send its reading again with what the cut
// part spent: the turn spent the totals' growth, not the request twice.
func TestACutShortRequestCountsWhatItSpent(t *testing.T) {
	a, _ := fake(t, "cut-short")
	g := open(t, a, spec(t))
	do(t, g, provider.Start{Text: "hi"})
	end := ended(turn(t, g))
	closeRun(t, g)
	if end.Tokens != 190 {
		t.Errorf("the turn used %d tokens, want 190", end.Tokens)
	}
}

// A request too big for the window has Codex fill it with a made-up last
// reading: that is no model's spend, and the turn spent only its real
// request.
func TestAnOverflowSpendsOnlyWhatWasSent(t *testing.T) {
	a, _ := fake(t, "overflow")
	g := open(t, a, spec(t))
	do(t, g, provider.Start{Text: "hi"})
	end := ended(turn(t, g))
	closeRun(t, g)
	if end.Tokens != 100 {
		t.Errorf("the turn used %d tokens, want 100", end.Tokens)
	}
}

// A reading of a turn past that Codex sends again in the next turn says
// nothing new: the next turn counts from its own readings, even after a
// compaction set the thread's totals back.
func TestAnOldTurnsRepeatMovesNoCount(t *testing.T) {
	for scenario, want := range map[string]int64{"old-repeat": 170, "old-repeat-reset": 120} {
		a, _ := fake(t, scenario)
		s := spec(t)
		var used []int64
		for range 2 {
			s.Run = api.NewID()
			g := open(t, a, s)
			do(t, g, provider.Start{Text: "hi"})
			used = append(used, ended(turn(t, g)).Tokens)
			closeRun(t, g)
		}
		if used[0] != 100 || used[1] != want {
			t.Errorf("%s: the turns used %v tokens, want [100 %d]", scenario, used, want)
		}
	}
}

// A Codex that keeps Approve for me's approvals with the user, as an
// account without auto-review or a Codex too old to say does, runs its
// turns so too, which asks as before auto-review; Codex then says why as
// it describes itself, and once a thread takes auto-review, it says
// nothing of it.
func TestApproveForMeWithoutAutoReviewAsksTheUser(t *testing.T) {
	for _, tc := range []struct{ scenario, reviewer, noAuto string }{
		{"no-auto", "user", "Codex 0.171.0 kept the thread's approvals with you, not its auto-review"},
		{"old-codex", "user", "Codex 0.171.0 kept the thread's approvals with you, not its auto-review"},
		{"hi", "auto_review", ""},
	} {
		a, log := fake(t, tc.scenario)
		s := spec(t)
		s.Access = api.AccessAutoEdits
		g := open(t, a, s)
		do(t, g, provider.Start{Text: "hi"})
		turn(t, g)
		closeRun(t, g)
		var ts struct{ ApprovalsReviewer string }
		json.Unmarshal(sent(t, log, "turn/start")[0], &ts)
		if ts.ApprovalsReviewer != tc.reviewer {
			t.Errorf("%s: the turn's reviewer is %q, want %q", tc.scenario, ts.ApprovalsReviewer, tc.reviewer)
		}
		info, err := a.Describe(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if info.NoAuto != tc.noAuto {
			t.Errorf("%s: Codex describes its auto-review as %q, want %q", tc.scenario, info.NoAuto, tc.noAuto)
		}
	}
}

// Approve for me chosen mid-thread holds from the thread's next turn: a
// new app-server resumes the thread with auto-review.
func TestApproveForMeChosenMidThreadReviewsNextTurn(t *testing.T) {
	a, log := fake(t, "hi")
	s := spec(t)
	for _, access := range []api.Access{api.AccessAsk, api.AccessAutoEdits} {
		s.Run, s.Access = api.NewID(), access
		g := open(t, a, s)
		s.Session = g.(provider.Sessioned).Session()
		do(t, g, provider.Start{Text: "hi"})
		turn(t, g)
		closeRun(t, g)
	}
	var reviewers []string
	for _, m := range []string{"thread/start", "thread/resume", "turn/start"} {
		for _, p := range sent(t, log, m) {
			var v struct{ ApprovalsReviewer string }
			json.Unmarshal(p, &v)
			reviewers = append(reviewers, m+" "+v.ApprovalsReviewer)
		}
	}
	if want := []string{"thread/start user", "thread/resume auto_review", "turn/start user", "turn/start auto_review"}; !slices.Equal(reviewers, want) {
		t.Errorf("reviewers %q, want %q", reviewers, want)
	}
}

// A reviewer Codex reports other than the one asked for reaches no turn:
// Ask for approval and Full access keep theirs with the user, and Approve
// for me without auto-review does too.
func TestATurnSendsItsLevelsReviewer(t *testing.T) {
	for access, want := range map[api.Access]string{api.AccessAsk: "user", api.AccessAutoEdits: "user", api.AccessFull: "user"} {
		a, log := fake(t, "guardian")
		s := spec(t)
		s.Access = access
		g := open(t, a, s)
		do(t, g, provider.Start{Text: "hi"})
		turn(t, g)
		closeRun(t, g)
		var ts struct{ ApprovalsReviewer string }
		json.Unmarshal(sent(t, log, "turn/start")[0], &ts)
		if ts.ApprovalsReviewer != want {
			t.Errorf("access %v: the turn's reviewer is %q, want %q", access, ts.ApprovalsReviewer, want)
		}
	}
}

// What Settings says of auto-review is what the last Approve for me run
// had, on a parked app-server as on a new one.
func TestAParkedAppServerSaysWhatItsThreadHad(t *testing.T) {
	a, _ := fake(t, "no-auto")
	fellBack, took := spec(t), spec(t)
	fellBack.Access, took.Access = api.AccessAutoEdits, api.AccessAutoEdits
	var said []string
	for _, s := range []*provider.SessionSpec{&fellBack, &took, &fellBack} {
		if s == &took {
			t.Setenv(codextest.Scenario, "hi")
		}
		s.Run = api.NewID()
		g := open(t, a, *s)
		s.Session = g.(provider.Sessioned).Session()
		do(t, g, provider.Start{Text: "hi"})
		turn(t, g)
		closeRun(t, g)
		info, err := a.Describe(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		said = append(said, info.NoAuto)
	}
	if said[0] == "" || said[1] != "" || said[2] != said[0] {
		t.Errorf("Codex described its auto-review as %q, want the fallback, nothing, the fallback", said)
	}
}

// confirmed is the run's Granted and Ran reports, in order, and how many
// of its events came before the first of them.
func confirmed(events []provider.Event) (out []provider.Event, before int) {
	before = -1
	for i, e := range events {
		switch e.(type) {
		case provider.Granted, provider.Ran:
			if before < 0 {
				before = i
			}
			out = append(out, e)
		}
	}
	return out, before
}

// A run reports the access, model and effort Codex says the thread runs
// at, before any of the turn's work: what it asked, at each level.
func TestARunReportsWhatCodexRunsAt(t *testing.T) {
	for _, tc := range []struct {
		access api.Access
		mode   string
	}{
		{api.AccessAsk, "read-only, on-request, user"},
		{api.AccessAutoEdits, "workspace-write, on-request, auto_review"},
		{api.AccessFull, "danger-full-access, never, user"},
	} {
		a, _ := fake(t, "hi")
		s := spec(t)
		s.Access = tc.access
		g := open(t, a, s)
		do(t, g, provider.Start{Text: "hi"})
		events := turn(t, g)
		closeRun(t, g)
		got, before := confirmed(events)
		want := []provider.Event{provider.Granted{Access: tc.access, Mode: tc.mode}, provider.Ran{Model: "gpt-6-luna", Effort: "low"}}
		if !slices.Equal(got, want) || before != 0 {
			t.Errorf("access %d: reported %+v after %d events, want %+v first", tc.access, got, before, want)
		}
	}
}

// Approve for me on a Codex that keeps the approvals with the user
// reports the level with the user's approvals, and Codex's reason.
func TestApproveForMeFallingBackSaysSo(t *testing.T) {
	a, _ := fake(t, "no-auto")
	s := spec(t)
	s.Access = api.AccessAutoEdits
	g := open(t, a, s)
	do(t, g, provider.Start{Text: "hi"})
	got, _ := confirmed(turn(t, g))
	closeRun(t, g)
	want := provider.Granted{Access: api.AccessAutoEdits, Mode: "workspace-write, on-request, user", Why: "Codex 0.171.0 kept the thread's approvals with you, not its auto-review"}
	if len(got) == 0 || got[0] != want {
		t.Errorf("reported %+v, want %+v", got, want)
	}
}

// Each run reports again, on the thread's kept app-server too: a model
// changed for the next run is the one reported once Codex takes it, and
// an access changed is reported from the app-server it starts.
func TestEachRunReportsWhatItRunsAt(t *testing.T) {
	a, log := fake(t, "hi")
	s := spec(t)
	ask := provider.Granted{Access: api.AccessAsk, Mode: "read-only, on-request, user"}
	for i, step := range []struct {
		model  api.ModelRef
		access api.Access
		want   []provider.Event
	}{
		{s.Model, api.AccessAsk, []provider.Event{ask, provider.Ran{Model: "gpt-6-luna", Effort: "low"}}},
		{api.ModelRef{Provider: "codex", Model: "gpt-6.2-sol", Effort: "high"}, api.AccessAsk, []provider.Event{ask, provider.Ran{Model: "gpt-6.2-sol", Effort: "high"}}},
		{api.ModelRef{Provider: "codex", Model: "gpt-6.2-sol", Effort: "high"}, api.AccessAsk, []provider.Event{ask, provider.Ran{Model: "gpt-6.2-sol", Effort: "high"}}},
		{api.ModelRef{Provider: "codex", Model: "gpt-6.2-sol", Effort: "high"}, api.AccessFull, []provider.Event{provider.Granted{Access: api.AccessFull, Mode: "danger-full-access, never, user"}, provider.Ran{Model: "gpt-6.2-sol", Effort: "high"}}},
	} {
		s.Run, s.Model, s.Access = api.NewID(), step.model, step.access
		g := open(t, a, s)
		s.Session = g.(provider.Sessioned).Session()
		do(t, g, provider.Start{Text: "hi"})
		got, _ := confirmed(turn(t, g))
		closeRun(t, g)
		if !slices.Equal(got, step.want) {
			t.Errorf("run %d reported %+v, want %+v", i+1, got, step.want)
		}
	}
	if n, m := len(sent(t, log, "thread/start")), len(sent(t, log, "thread/resume")); n != 1 || m != 1 {
		t.Errorf("%d threads started and %d resumed, want the first three runs on one app-server", n, m)
	}
}

// A model Codex runs in place of the one asked is reported as Codex's,
// with why.
func TestAModelCodexSubstitutesIsReportedAsItsOwn(t *testing.T) {
	a, _ := fake(t, "substitute")
	g := open(t, a, spec(t))
	do(t, g, provider.Start{Text: "hi"})
	got, _ := confirmed(turn(t, g))
	closeRun(t, g)
	want := provider.Ran{Model: "gpt-6.2-sol", Effort: "low", Why: "Codex runs gpt-6.2-sol at low"}
	if len(got) != 2 || got[1] != want {
		t.Errorf("reported %+v, want %+v", got, want)
	}
}

// A model Codex changes mid-turn is reported again as it changes.
func TestAModelChangedMidTurnIsReported(t *testing.T) {
	a, _ := fake(t, "switch")
	g := open(t, a, spec(t))
	do(t, g, provider.Start{Text: "hi"})
	events := turn(t, g)
	closeRun(t, g)
	var ran []provider.Ran
	texted := false
	for _, e := range events {
		switch e := e.(type) {
		case provider.TextDelta:
			texted = true
		case provider.Ran:
			if len(ran) == 1 && !texted {
				t.Errorf("the change was reported before the turn's first text")
			}
			ran = append(ran, e)
		}
	}
	want := []provider.Ran{{Model: "gpt-6-luna", Effort: "low"}, {Model: "gpt-6.2-sol", Effort: "high", Why: "Codex runs gpt-6.2-sol at high"}}
	if !slices.Equal(ran, want) {
		t.Errorf("reported %+v, want %+v", ran, want)
	}
}

// What Codex doesn't say goes unreported: all of it from a Codex too old
// to say, and an effort the turn changed from one that says nothing of
// the change, or says it unset.
func TestWhatCodexDoesntSayIsNotReported(t *testing.T) {
	for _, tc := range []struct {
		scenario string
		want     []provider.Event
	}{
		{"old-codex", nil},
		{"silent", []provider.Event{provider.Granted{Access: api.AccessAsk, Mode: "read-only, on-request, user"}, provider.Ran{Model: "gpt-6-luna"}}},
		{"effort-null", []provider.Event{provider.Granted{Access: api.AccessAsk, Mode: "read-only, on-request, user"}, provider.Ran{Model: "gpt-6-luna"}}},
	} {
		a, _ := fake(t, tc.scenario)
		g := open(t, a, spec(t))
		do(t, g, provider.Start{Text: "hi"})
		got, _ := confirmed(turn(t, g))
		closeRun(t, g)
		if !slices.Equal(got, tc.want) {
			t.Errorf("%s: reported %+v, want %+v", tc.scenario, got, tc.want)
		}
	}
}

// A model Codex answers the run's thread/start with in place of the one
// asked is reported, though no turn update follows.
func TestAModelCodexStartsWithInPlaceOfTheAskedIsReported(t *testing.T) {
	a, _ := fake(t, "substitute-silent")
	g := open(t, a, spec(t))
	do(t, g, provider.Start{Text: "hi"})
	got, _ := confirmed(turn(t, g))
	closeRun(t, g)
	want := provider.Ran{Model: "gpt-6.2-sol", Why: "Codex runs gpt-6.2-sol"}
	if len(got) != 2 || got[1] != want {
		t.Errorf("reported %+v, want %+v", got, want)
	}
}

// An update of a turn that is over reaches no later run: the next run on
// the kept app-server, asking another model that Codex says nothing of,
// reports no model.
func TestAnOldTurnsSettingsReachNoLaterRun(t *testing.T) {
	a, _ := fake(t, "stale-settings")
	s := spec(t)
	var got [][]provider.Event
	for _, m := range []api.ModelRef{s.Model, {Provider: "codex", Model: "gpt-6.2-sol", Effort: "high"}} {
		s.Run, s.Model = api.NewID(), m
		g := open(t, a, s)
		do(t, g, provider.Start{Text: "hi"})
		reports, _ := confirmed(turn(t, g))
		closeRun(t, g)
		got = append(got, reports)
	}
	if want := []provider.Event{provider.Granted{Access: api.AccessAsk, Mode: "read-only, on-request, user"}}; !slices.Equal(got[1], want) {
		t.Errorf("the second run reported %+v, want %+v", got[1], want)
	}
}

// A trusted project's .codex/config.toml names the model a thread there
// runs, over the user's: DefaultModel asks Codex's config of the
// workspace.
func TestAProjectsConfigIsItsDefault(t *testing.T) {
	a, _ := fake(t, "hi")
	home, ws := t.TempDir(), t.TempDir()
	t.Setenv("CODEX_HOME", home)
	os.WriteFile(filepath.Join(home, "config.toml"), []byte("model = \"gpt-6-luna\"\n"), 0o600)
	os.MkdirAll(filepath.Join(ws, ".codex"), 0o755)
	os.WriteFile(filepath.Join(ws, ".codex", "config.toml"), []byte("model = \"gpt-6.2-sol\"\n"), 0o600)
	if got, err := a.DefaultModel(context.Background(), ws); err != nil || got != "gpt-6.2-sol" {
		t.Errorf("in the project, DefaultModel = %q, %v; want its config's gpt-6.2-sol", got, err)
	}
	if got, err := a.DefaultModel(context.Background(), t.TempDir()); err != nil || got != "gpt-6-luna" {
		t.Errorf("elsewhere, DefaultModel = %q, %v; want the user's gpt-6-luna", got, err)
	}
}

// The effort Codex's config names is one its default runs at, though
// Codex doesn't list that model, so the picker offers it.
func TestAConfiguredEffortIsOneTheDefaultRunsAt(t *testing.T) {
	a, _ := fake(t, "configured-unlisted")
	info, err := a.Describe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range info.Models {
		if m.Default && !slices.ContainsFunc(m.Efforts, func(e provider.Effort) bool { return e.ID == "xhigh" }) {
			t.Errorf("the default %s runs at %+v, want xhigh among them", m.ID, m.Efforts)
		}
	}
}

// A config/read that fails is an error DefaultModel says, so the app asks
// again; a Codex without config/read names no default there.
func TestAFailedConfigReadIsDefaultModelsError(t *testing.T) {
	a, _ := fake(t, "config-read-fails")
	if got, err := a.DefaultModel(context.Background(), t.TempDir()); err == nil {
		t.Errorf("DefaultModel = %q, nil; want config/read's error", got)
	}
	a, _ = fake(t, "no-config-read")
	if got, err := a.DefaultModel(context.Background(), t.TempDir()); err != nil || got != "" {
		t.Errorf("without config/read, DefaultModel = %q, %v; want none and no error", got, err)
	}
}

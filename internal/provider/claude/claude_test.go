package claude_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/mcp"
	"glitched/internal/provider"
	"glitched/internal/provider/claude"
	"glitched/internal/provider/claude/transcript"
	"glitched/internal/runtime"
	"glitched/internal/store"
)

// The test binary plays claude: with these set, it replays a transcript
// that record wrote, one stdin line for each line the adapter sent.
const (
	envFake   = "GLITCHED_FAKE_CLAUDE"        // the transcript a --session-id run replays
	envResume = "GLITCHED_FAKE_CLAUDE_RESUME" // the transcript a --resume run replays
	envArgv   = "GLITCHED_FAKE_CLAUDE_ARGV"   // a file each run appends its argv to
	envEntry  = "GLITCHED_FAKE_CLAUDE_ENTRY"  // a file each run appends its entrypoint entries to
	envPids   = "GLITCHED_FAKE_CLAUDE_PIDS"   // a file each run appends its processes to, by role
	envEngine = "GLITCHED_FAKE_ENGINE"        // the store a test binary serves, as an engine a test can kill
	envHide   = "GLITCHED_FAKE_HIDE"          // a file a test binary writes its PID to, as a tool that hides its environment
	envSleep  = "GLITCHED_FAKE_SLEEP"         // how long a test binary sleeps, as sleep where there is none
)

func TestMain(m *testing.M) {
	// A project's own MCP server, which claude starts with the tee's
	// environment.
	if len(os.Args) > 1 && os.Args[1] == "mcp-toolless" {
		os.Exit(toollessMCP())
	}
	if code, teeing := transcript.Tee(); teeing {
		os.Exit(code)
	}
	// The test binary plays glitched mcp too, for the runs of a host
	// WithMCP gave it.
	if len(os.Args) > 1 && os.Args[1] == "mcp" {
		if err := mcp.Main(context.Background(), os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "glitched mcp:", err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	if d := os.Getenv(envSleep); d != "" {
		time.Sleep(must(time.ParseDuration(d)))
		os.Exit(0)
	}
	if path := os.Getenv(envHide); path != "" {
		os.Exit(hidden(path))
	}
	if path := os.Getenv(envEngine); path != "" {
		os.Exit(engine(path))
	}
	if path := os.Getenv(envFake); path != "" {
		os.Exit(fakeClaude(path))
	}
	os.Exit(m.Run())
}

func fakeClaude(path string) int {
	if slices.Contains(os.Args, "--resume") {
		path = os.Getenv(envResume)
	}
	if f := os.Getenv(envArgv); f != "" {
		b, _ := json.Marshal(os.Args[1:])
		out, _ := os.OpenFile(f, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		out.Write(append(b, '\n'))
		out.Close()
	}
	if f := os.Getenv(envEntry); f != "" {
		var entries []string
		for _, kv := range os.Environ() {
			if k, _, _ := strings.Cut(kv, "="); strings.EqualFold(k, "CLAUDE_CODE_ENTRYPOINT") || mcp.Routing(k) {
				entries = append(entries, kv)
			}
		}
		b, _ := json.Marshal(entries)
		os.WriteFile(f, b, 0o644)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fake claude:", err)
		return 2
	}
	note("claude", os.Getpid())
	wd, _ := os.Getwd()
	// The uuids the transcript's messages had stand for those the adapter
	// sent in their place.
	// A path goes into JSON escaped, as Windows' backslashes must be.
	quoted, _ := json.Marshal(wd)
	uuids := []string{"$WORKSPACE", string(quoted[1 : len(quoted)-1])}
	// An entry with "if" plays only when a request so far set that field,
	// and one with "unless" only when none did.
	set := map[string]bool{}
	in := bufio.NewReader(os.Stdin)
	var server *mcpServer
	defer func() {
		if server != nil {
			server.close()
		}
	}()
	for _, l := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var e map[string]json.RawMessage
		json.Unmarshal([]byte(strings.NewReplacer(uuids...).Replace(l)), &e)
		var when struct{ If, Unless string }
		json.Unmarshal([]byte(l), &when)
		if when.If != "" && !set[when.If] || when.Unless != "" && set[when.Unless] {
			continue
		}
		switch {
		case e["sent"] != nil:
			got, err := in.ReadBytes('\n')
			if err != nil {
				return 0
			}
			transcript.Got(got)
			if want := kind(e["sent"]); kind(got) != want {
				fmt.Fprintf(os.Stderr, "fake claude: got %s on stdin, want %s\n", kind(got), want)
				return 3
			}
			if was, is := uuidIn(e["sent"]), uuidIn(got); was != "" && is != "" {
				uuids = append(uuids, was, is)
			}
			var req struct{ Request map[string]any }
			json.Unmarshal(got, &req)
			for k, v := range req.Request {
				if v == true {
					set[k] = true
				}
			}
		case e["take"] != nil:
			// Claude Code takes a queued message as a turn of its own, and
			// writes it to the session: here, a file of that name.
			var name string
			json.Unmarshal(e["take"], &name)
			os.WriteFile(name, nil, 0o644)
		case e["graceful"] != nil:
			// Claude Code takes a while to exit on SIGTERM, and goes on
			// with its work meanwhile.
			var d string
			json.Unmarshal(e["graceful"], &d)
			term := make(chan os.Signal, 1)
			signal.Notify(term, syscall.SIGTERM)
			go func() {
				<-term
				time.Sleep(must(time.ParseDuration(d)))
				os.Exit(143)
			}()
		case e["mcp-sent"] != nil:
			// What Claude Code sent the run's MCP server: the server the
			// run's --mcp-config names gets it too.
			if server == nil {
				if server, err = startMCP(); err != nil {
					fmt.Fprintln(os.Stderr, "fake claude:", err)
					return 2
				}
			}
			server.in.Write(append(e["mcp-sent"], '\n'))
		case e["mcp-recv"] != nil:
			if _, err := server.out.ReadBytes('\n'); err != nil {
				fmt.Fprintln(os.Stderr, "fake claude: the MCP server said nothing more:", err)
				return 3
			}
		case e["recv"] != nil:
			os.Stdout.Write(append(e["recv"], '\n'))
		case e["stderr"] != nil:
			var s string
			json.Unmarshal(e["stderr"], &s)
			fmt.Fprintln(os.Stderr, s)
		case e["eof"] != nil:
			for {
				got, err := in.ReadBytes('\n')
				if err != nil {
					break
				}
				transcript.Got(got)
				fmt.Fprintf(os.Stderr, "fake claude: got %s on stdin, want its end\n", kind(got))
			}
		case e["tool"] != nil:
			// Claude Code runs a tool call in a shell of its own session,
			// and stops it on SIGTERM.
			var argv []string
			json.Unmarshal(e["tool"], &argv)
			sh := command(argv)
			if err := sh.Start(); err != nil {
				fmt.Fprintln(os.Stderr, "fake claude:", err)
				return 2
			}
			note("tool", sh.Process.Pid)
			term := make(chan os.Signal, 1)
			signal.Notify(term, syscall.SIGTERM)
			go func() {
				<-term
				sh.Process.Kill()
				sh.Wait()
				os.Exit(143)
			}()
		case e["orphan"] != nil:
			// A tool shell this claude leaves running when it dies.
			var argv []string
			json.Unmarshal(e["orphan"], &argv)
			sh := command(argv)
			if err := sh.Start(); err != nil {
				fmt.Fprintln(os.Stderr, "fake claude:", err)
				return 2
			}
			note("tool", sh.Process.Pid)
		case e["holder"] != nil:
			// A process claude leaves running, holding its output open.
			sh := command([]string{"sleep", "60"})
			sh.Stdout, sh.Stderr = os.Stdout, os.Stderr
			if err := sh.Start(); err != nil {
				fmt.Fprintln(os.Stderr, "fake claude:", err)
				return 2
			}
			note("holder", sh.Process.Pid)
		case e["sleep"] != nil:
			// A model call, which takes Claude Code a while.
			var d string
			json.Unmarshal(e["sleep"], &d)
			time.Sleep(must(time.ParseDuration(d)))
		case e["hang"] != nil:
			// Busy, and reading nothing more.
			time.Sleep(time.Hour)
		case e["exit"] != nil:
			var code int
			json.Unmarshal(e["exit"], &code)
			return code
		}
	}
	return 0
}

// mcpServer is the run's MCP server as the fake claude started it.
type mcpServer struct {
	cmd *exec.Cmd
	in  io.WriteCloser
	out *bufio.Reader
}

// startMCP starts the server of the --mcp-config the fake claude was
// given, as Claude Code does: its command and args, with its env added to
// claude's own.
func startMCP() (*mcpServer, error) {
	i := slices.Index(os.Args, "--mcp-config")
	if i < 0 || i+1 == len(os.Args) {
		return nil, errors.New("the transcript has MCP traffic, but the run has no --mcp-config")
	}
	var cfg struct {
		MCPServers map[string]struct {
			Command string
			Args    []string
			Env     map[string]string
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(os.Args[i+1]), &cfg); err != nil {
		return nil, err
	}
	s, ok := cfg.MCPServers["glitched"]
	if !ok {
		return nil, fmt.Errorf("--mcp-config %s has no glitched server", os.Args[i+1])
	}
	cmd := exec.Command(s.Command, s.Args...)
	cmd.Env = os.Environ()
	for k, v := range s.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.Stderr = os.Stderr
	in, err1 := cmd.StdinPipe()
	out, err2 := cmd.StdoutPipe()
	if err := errors.Join(err1, err2, cmd.Start()); err != nil {
		return nil, err
	}
	return &mcpServer{cmd: cmd, in: in, out: bufio.NewReader(out)}, nil
}

func (s *mcpServer) close() {
	s.in.Close()
	s.cmd.Wait()
}

// engine serves the store at path until it is killed, with the fake
// claude when a transcript is set, else the claude on PATH.
func engine(path string) int {
	os.Unsetenv(envEngine)
	st, err := store.Open(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "engine:", err)
		return 2
	}
	ad := &claude.Adapter{}
	if os.Getenv(envFake) != "" {
		ad.Path = must(os.Executable())
	}
	if _, err := runtime.New(ctx, st, provider.Mux{"claude": ad}); err != nil {
		fmt.Fprintln(os.Stderr, "engine:", err)
		return 2
	}
	time.Sleep(time.Hour)
	return 0
}

// note appends a process of the fake's to the file envPids names.
func note(role string, pid int) {
	if f := os.Getenv(envPids); f != "" {
		out, _ := os.OpenFile(f, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		fmt.Fprintln(out, role, pid)
		out.Close()
	}
}

func uuidIn(line []byte) string {
	var m struct{ UUID string }
	json.Unmarshal(line, &m)
	return m.UUID
}

func kind(line []byte) string {
	var m struct {
		Type    string `json:"type"`
		Request struct {
			Subtype string `json:"subtype"`
		} `json:"request"`
	}
	json.Unmarshal(line, &m)
	return strings.TrimSuffix(m.Type+" "+m.Request.Subtype, " ")
}

// replay has runs started from now on replay the named transcripts, and
// returns the file their argv go to.
func replay(t *testing.T, sessionID, resume string) string {
	t.Helper()
	t.Setenv(envFake, filepath.Join(must(os.Getwd()), "testdata", sessionID+".jsonl"))
	if resume != "" {
		t.Setenv(envResume, filepath.Join(must(os.Getwd()), "testdata", resume+".jsonl"))
	}
	argv := filepath.Join(t.TempDir(), "argv")
	t.Setenv(envArgv, argv)
	return argv
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

var ctx = context.Background()

// turnWait is how long a turn may take: a replay is instant, a live one
// is not.
var turnWait = 10 * time.Second

// host is a real engine on ad, the fake claude when nil.
func host(t *testing.T, ad *claude.Adapter) *runtime.Host {
	t.Helper()
	return hostAt(t, filepath.Join(t.TempDir(), "host.db"), ad)
}

// hostAt is host on the store at path.
func hostAt(t *testing.T, path string, ad *claude.Adapter) *runtime.Host {
	t.Helper()
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if ad == nil {
		ad = &claude.Adapter{Path: must(os.Executable())}
	}
	h, err := runtime.New(ctx, st, provider.Mux{"claude": ad})
	if err != nil {
		st.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		h.Close()
		st.Close()
	})
	return h
}

func newThread(t *testing.T, h *runtime.Host, access api.Access) api.ID {
	t.Helper()
	thread := api.NewID()
	submit(t, h, thread, api.ThreadCreate{Workspace: t.TempDir(), Title: "hi", Access: access,
		Model: api.ModelRef{Provider: "claude", Model: "haiku"}})
	return thread
}

func submit(t *testing.T, h *runtime.Host, thread api.ID, body api.Intent) {
	t.Helper()
	if _, err := h.Submit(ctx, api.Command{ID: api.NewID(), Target: thread, Body: body}); err != nil {
		t.Fatal(err)
	}
}

// turn sends text and returns what the thread's log holds of the run
// that took it, once the turn ended. Nobody answers its requests.
func turn(t *testing.T, h *runtime.Host, thread api.ID, text string) promptLog {
	t.Helper()
	return playOn(t, h, thread, text, func(requestLog) bool { return false })
}

// streamed is the text a transcript's one text block streamed.
func streamed(t *testing.T, name string) string {
	t.Helper()
	var text strings.Builder
	for _, l := range strings.Split(string(must(os.ReadFile(filepath.Join("testdata", name+".jsonl")))), "\n") {
		var e struct {
			Recv struct {
				Event struct {
					Delta struct{ Type, Text string }
				}
			}
		}
		json.Unmarshal([]byte(l), &e)
		if e.Recv.Event.Delta.Type == "text_delta" {
			text.WriteString(e.Recv.Event.Delta.Text)
		}
	}
	if text.Len() == 0 {
		t.Fatalf("%s streamed no text", name)
	}
	return text.String()
}

func TestATurnStreamsIntoTheThread(t *testing.T) {
	replay(t, "tools", "")
	h := host(t, nil)
	thread := newThread(t, h, api.AccessAsk)

	got := playOn(t, h, thread, "Read hello.txt. Then use the Write tool to create out.txt containing ok. Then reply done.", func(r requestLog) bool {
		race(t, h, thread, r.opened, func(int) json.RawMessage { return allowed(false) })
		return false
	})

	if got.ended.Outcome != api.OutcomeCompleted || got.ended.Tokens == 0 {
		t.Errorf("turn ended %+v, want completed with the tokens it used", got.ended)
	}
	if want := streamed(t, "tools"); got.text != want {
		t.Errorf("assistant text %q, want what Claude streamed: %q", got.text, want)
	}
	want := []api.WorkItem{
		{Item: "read", Kind: "read", Title: "Read hello.txt", Status: api.WorkStarted, Path: "hello.txt"},
		{Item: "read", Kind: "read", Title: "Read hello.txt", Status: api.WorkCompleted, Path: "hello.txt"},
		{Item: "write", Kind: "edit", Title: "Write out.txt", Status: api.WorkStarted, Path: "out.txt"},
		{Item: "write", Kind: "edit", Title: "Write out.txt", Status: api.WorkFailed, Path: "out.txt"},
	}
	if len(got.work) != len(want) {
		t.Fatalf("work items %+v, want %d: the read, and the write the user denied", got.work, len(want))
	}
	for i, w := range got.work {
		if w.Kind != want[i].Kind || w.Title != want[i].Title || w.Status != want[i].Status || w.Path != want[i].Path {
			t.Errorf("work item %d is %+v, want %+v", i, w, want[i])
		}
	}
	if got.work[0].Item != got.work[1].Item || got.work[2].Item != got.work[3].Item || got.work[0].Item == got.work[2].Item {
		t.Errorf("work items %+v, want each tool use's start and end on one item", got.work)
	}
	if !strings.Contains(got.work[3].Output, "denied this tool call") {
		t.Errorf("the denied write shows %q, want the reason glitched gave", got.work[3].Output)
	}
}

// A turn that ends without a step's result, as when claude exits during a
// tool call, ends the step too.
func TestATurnEndsTheStepsItLeftOpen(t *testing.T) {
	for _, name := range []string{"exit-mid-tool", "result-mid-tool"} {
		t.Run(name, func(t *testing.T) {
			replay(t, name, "")
			h := host(t, nil)
			got := turn(t, h, newThread(t, h, api.AccessFull), "Run sleep 60.")
			if got.ended.Outcome != api.OutcomeFailed {
				t.Errorf("turn ended %+v, want failed", got.ended)
			}
			if len(got.work) != 2 || got.work[0].Status != api.WorkStarted || got.work[1].Status != api.WorkFailed || got.work[1].Item != got.work[0].Item {
				t.Errorf("work items %+v, want the Bash step started, then failed", got.work)
			}
		})
	}
}

func TestALaterTurnResumesTheThreadsSession(t *testing.T) {
	argv := replay(t, "hi", "")
	h := host(t, nil)
	thread := newThread(t, h, api.AccessAsk)
	if first := turn(t, h, thread, "Say hi in three words."); first.ended.Outcome != api.OutcomeCompleted || first.text == "" {
		t.Fatalf("first turn: %+v", first)
	}

	replay(t, "again", "again-resume")
	t.Setenv(envArgv, argv)
	got := turn(t, h, thread, "What did I ask you to do last time? Answer in under ten words.")
	if got.ended.Outcome != api.OutcomeCompleted || !strings.Contains(got.text, "say hi") {
		t.Errorf("second turn %+v, want Claude to answer from the first turn's session", got)
	}

	var sessions [][2]string
	for _, l := range strings.Split(strings.TrimSpace(string(must(os.ReadFile(argv)))), "\n") {
		var args []string
		json.Unmarshal([]byte(l), &args)
		sessions = append(sessions, [2]string{args[len(args)-2], args[len(args)-1]})
	}
	if len(sessions) != 3 || sessions[0][0] != "--session-id" || sessions[1][0] != "--session-id" || sessions[2][0] != "--resume" ||
		sessions[1][1] != sessions[0][1] || sessions[2][1] != sessions[0][1] {
		t.Errorf("claude ran with sessions %v, want the thread's session created, found in use, then resumed", sessions)
	}
}

func TestEachThreadHasItsOwnSession(t *testing.T) {
	argv := replay(t, "hi", "")
	h := host(t, nil)
	for range 2 {
		turn(t, h, newThread(t, h, api.AccessAsk), "Say hi in three words.")
	}
	lines := strings.Split(strings.TrimSpace(string(must(os.ReadFile(argv)))), "\n")
	if len(lines) != 2 || lines[0] == lines[1] {
		t.Errorf("two threads ran claude with %q, want a session each", lines)
	}
}

func TestASignedOutTurnSaysToLogIn(t *testing.T) {
	replay(t, "logged-out", "")
	h := host(t, nil)
	got := turn(t, h, newThread(t, h, api.AccessAsk), "say hi")
	want := "Claude Code is not signed in.\nRun claude in a terminal and use /login.\n(claude said: Not logged in · Please run /login)"
	if got.ended.Outcome != api.OutcomeFailed || got.ended.Error != want {
		t.Errorf("turn ended %+v, want it failed with\n%s", got.ended, want)
	}
	if got.text != "" {
		t.Errorf("assistant text %q; the sign-in message belongs to the error, not the reply", got.text)
	}
}

func TestAMissingClaudeSaysToInstallIt(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir()) // the home Windows reads
	want := "Claude Code is not installed.\nInstall it, or set claude_path in engine.json."
	for _, ad := range []*claude.Adapter{{}, {Path: filepath.Join(t.TempDir(), "claude")}} {
		h := host(t, ad)
		got := turn(t, h, newThread(t, h, api.AccessAsk), "say hi")
		if got.ended.Outcome != api.OutcomeFailed || got.ended.Error != want {
			t.Errorf("with Path %q the turn ended %+v, want it failed with\n%s", ad.Path, got.ended, want)
		}
	}
}

func TestAccessPicksThePermissionMode(t *testing.T) {
	argv := replay(t, "hi", "")
	h := host(t, nil)
	for _, access := range []api.Access{api.AccessAsk, api.AccessAutoEdits, api.AccessFull} {
		turn(t, h, newThread(t, h, access), "Say hi in three words.")
	}
	var modes []string
	for _, l := range strings.Split(strings.TrimSpace(string(must(os.ReadFile(argv)))), "\n") {
		var args []string
		json.Unmarshal([]byte(l), &args)
		if i := slices.Index(args, "--permission-mode"); i >= 0 {
			modes = append(modes, args[i+1])
		}
		// Whatever the mode leaves undecided comes to glitched to ask.
		if i := slices.Index(args, "--permission-prompt-tool"); i < 0 || args[i+1] != "stdio" {
			t.Errorf("claude ran with %q, want its prompts on stdio", args)
		}
		// AskUserQuestion's questions come as a prompt too.
		if slices.Contains(args, "--disallowedTools") {
			t.Errorf("claude ran with %q, want every tool allowed to ask", args)
		}
	}
	if want := []string{"default", "acceptEdits", "bypassPermissions"}; !slices.Equal(modes, want) {
		t.Errorf("permission modes %v, want %v", modes, want)
	}
}

// Anthropic meters SDK entrypoints harder, so claude must hear glitched,
// once, whatever the engine inherited.
func TestClaudeRunsAsGlitched(t *testing.T) {
	replay(t, "hi", "")
	t.Setenv("CLAUDE_CODE_ENTRYPOINT", "sdk-cli")
	entry := filepath.Join(t.TempDir(), "entry")
	t.Setenv(envEntry, entry)
	h := host(t, nil)
	turn(t, h, newThread(t, h, api.AccessAsk), "Say hi in three words.")
	var got []string
	json.Unmarshal(must(os.ReadFile(entry)), &got)
	want := []string{"CLAUDE_CODE_ENTRYPOINT=glitched"}
	if !slices.Equal(got, want) {
		t.Errorf("claude ran with %q, want only %q", got, want)
	}
	// os/exec keeps the last of duplicate keys, but which one a platform's
	// exec keeps is not ours to rely on: the command holds one.
	var built []string
	for _, kv := range claude.Command(ctx, must(os.Executable())).Env {
		if strings.HasPrefix(strings.ToUpper(kv), "CLAUDE_CODE_ENTRYPOINT=") {
			built = append(built, kv)
		}
	}
	if !slices.Equal(built, want) {
		t.Errorf("the claude command's env has %q, want only %q", built, want)
	}
}

// A run gets the MCP servers of the engine's config, the app's browser,
// beside the user's own, when the engine has one; none otherwise.
func TestARunGetsTheEnginesMCPServers(t *testing.T) {
	argv := replay(t, "hi", "")
	config := filepath.Join(t.TempDir(), "browser-mcp.json")
	for _, ad := range []*claude.Adapter{{Path: must(os.Executable()), MCPConfig: config}, nil} {
		h := host(t, ad)
		turn(t, h, newThread(t, h, api.AccessAsk), "Say hi in three words.")
	}
	lines := strings.Split(strings.TrimSpace(string(must(os.ReadFile(argv)))), "\n")
	if len(lines) != 2 {
		t.Fatalf("claude ran %d times, want 2", len(lines))
	}
	var with, without []string
	json.Unmarshal([]byte(lines[0]), &with)
	json.Unmarshal([]byte(lines[1]), &without)
	if slices.Contains(without, "--mcp-config") {
		t.Errorf("claude ran with %q, want no MCP config without one", without)
	}
	i := slices.Index(with, "--mcp-config")
	if i < 0 || i+2 >= len(with) || with[i+1] != config {
		t.Fatalf("claude ran with %q, want --mcp-config %s", with, config)
	}
	// --mcp-config takes every value up to the next flag.
	if !strings.HasPrefix(with[i+2], "--") {
		t.Errorf("--mcp-config is followed by %q, not a flag", with[i+2])
	}
}

// A run with glitched mcp and the engine's config gets both in one
// --mcp-config, the run's inline first, then the file, and the next flag
// after them. Only glitched mcp's tools run without asking.
func TestARunGetsGlitchedMCPAndTheEnginesMCPServers(t *testing.T) {
	argv := replay(t, "hi", "")
	config := filepath.Join(t.TempDir(), "browser-mcp.json")
	st, err := store.Open(filepath.Join(t.TempDir(), "host.db"))
	if err != nil {
		t.Fatal(err)
	}
	ad := &claude.Adapter{Path: must(os.Executable()), MCPConfig: config}
	h, err := runtime.New(ctx, st, provider.Mux{"claude": ad},
		runtime.WithMCP(mcp.Launcher{Command: must(os.Executable()), Engine: filepath.Join(t.TempDir(), "engine.sock")}))
	if err != nil {
		st.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		h.Close()
		st.Close()
	})
	turn(t, h, newThread(t, h, api.AccessAsk), "Say hi in three words.")

	var args []string
	json.Unmarshal([]byte(strings.TrimSpace(string(must(os.ReadFile(argv))))), &args)
	i := slices.Index(args, "--mcp-config")
	if i < 0 || i+3 >= len(args) {
		t.Fatalf("claude ran with %q, want an --mcp-config", args)
	}
	if n := strings.Count(strings.Join(args, "\x00"), "--mcp-config"); n != 1 {
		t.Errorf("claude ran with %d --mcp-config, want 1: %q", n, args)
	}
	var cfg struct {
		MCPServers map[string]any `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(args[i+1]), &cfg); err != nil || cfg.MCPServers["glitched"] == nil {
		t.Errorf("--mcp-config's first value %q, want glitched mcp inline", args[i+1])
	}
	if args[i+2] != config {
		t.Errorf("--mcp-config's second value %q, want %s", args[i+2], config)
	}
	if args[i+3] != "--allowedTools" || args[i+4] != "mcp__glitched__*" {
		t.Errorf("--mcp-config is followed by %q, want --allowedTools mcp__glitched__*", args[i+3:])
	}
}

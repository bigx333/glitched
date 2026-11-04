package claude_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/provider"
	"glitched/internal/provider/claude"
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
)

func TestMain(m *testing.M) {
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
			if k, _, _ := strings.Cut(kv, "="); strings.EqualFold(k, "CLAUDE_CODE_ENTRYPOINT") {
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
	wd, _ := os.Getwd()
	in := bufio.NewReader(os.Stdin)
	for _, l := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var e map[string]json.RawMessage
		json.Unmarshal([]byte(strings.ReplaceAll(l, "$WORKSPACE", wd)), &e)
		switch {
		case e["sent"] != nil:
			got, err := in.ReadBytes('\n')
			if err != nil {
				return 0
			}
			if want := kind(e["sent"]); kind(got) != want {
				fmt.Fprintf(os.Stderr, "fake claude: got %s on stdin, want %s\n", kind(got), want)
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
				if _, err := in.ReadBytes('\n'); err != nil {
					break
				}
			}
		case e["exit"] != nil:
			var code int
			json.Unmarshal(e["exit"], &code)
			return code
		}
	}
	return 0
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
	st, err := store.Open(filepath.Join(t.TempDir(), "host.db"))
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

// turn sends text and returns what the thread's log holds of the run that
// took it, once the turn ended.
type turnLog struct {
	text  string
	work  []api.WorkItem
	ended api.TurnEnded
}

func turn(t *testing.T, h *runtime.Host, thread api.ID, text string) turnLog {
	t.Helper()
	msg := api.NewID()
	if _, err := h.Submit(ctx, api.Command{ID: msg, Target: thread, Body: api.MessageDispatch{Text: text, Mode: api.StartNow}}); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(turnWait); ; time.Sleep(5 * time.Millisecond) {
		snap, err := h.Read(ctx, api.Query{Thread: thread})
		if err != nil {
			t.Fatal(err)
		}
		var l turnLog
		for _, e := range snap.Events {
			switch b := must(e.Decode()).(type) {
			case api.AssistantDelta:
				if b.Run == msg {
					l.text += b.Text
				}
			case api.WorkItem:
				if b.Run == msg {
					l.work = append(l.work, b)
				}
			case api.TurnEnded:
				if b.Run == msg {
					l.ended = b
					return l
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the turn did not end: %+v", l)
		}
	}
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

	got := turn(t, h, thread, "Read hello.txt. Then use the Write tool to create out.txt containing ok. Then reply done.")

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
		t.Fatalf("work items %+v, want %d: the read, and the write Ask mode denied", got.work, len(want))
	}
	for i, w := range got.work {
		if w.Kind != want[i].Kind || w.Title != want[i].Title || w.Status != want[i].Status || w.Path != want[i].Path {
			t.Errorf("work item %d is %+v, want %+v", i, w, want[i])
		}
	}
	if got.work[0].Item != got.work[1].Item || got.work[2].Item != got.work[3].Item || got.work[0].Item == got.work[2].Item {
		t.Errorf("work items %+v, want each tool use's start and end on one item", got.work)
	}
	if !strings.Contains(got.work[3].Output, "haven't granted it yet") {
		t.Errorf("the denied write shows %q, want Claude Code's reason", got.work[3].Output)
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
	}
	if want := []string{"default", "acceptEdits", "bypassPermissions"}; !slices.Equal(modes, want) {
		t.Errorf("permission modes %v, want %v", modes, want)
	}
}

func TestATestCannotRunARealClaude(t *testing.T) {
	bin := t.TempDir()
	ran := filepath.Join(t.TempDir(), "ran")
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\ntouch "+ran+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	defer func() {
		if recover() == nil {
			t.Error("Open ran the claude on PATH in a test without the claudelive tag, want a panic")
		}
		if _, err := os.Stat(ran); err == nil {
			t.Error("the claude on PATH ran")
		}
	}()
	(&claude.Adapter{}).Open(ctx, provider.SessionSpec{Thread: api.NewID(), Run: api.NewID(), Workspace: t.TempDir()})
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
	for _, kv := range claude.Command(ctx, "claude").Env {
		if strings.HasPrefix(strings.ToUpper(kv), "CLAUDE_CODE_ENTRYPOINT=") {
			built = append(built, kv)
		}
	}
	if !slices.Equal(built, want) {
		t.Errorf("the claude command's env has %q, want only %q", built, want)
	}
}

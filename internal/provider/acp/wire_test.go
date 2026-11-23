package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"

	"glitched/internal/api"
	"glitched/internal/provider"
)

// runWireFake is an agent written straight on the wire, for what acptest's
// SDK-built fake can't send: another session's updates, a line past the
// SDK's limit, a request after a cancel.
func runWireFake(mode string) {
	w := json.NewEncoder(os.Stdout)
	turns := 0
	re := func(id json.RawMessage, result any) {
		w.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	}
	note := func(sid, text string) {
		w.Encode(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{
			"sessionId": sid, "update": map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": text}},
		}})
	}
	permission := func(sid, kind string) {
		w.Encode(map[string]any{"jsonrpc": "2.0", "id": 100, "method": "session/request_permission", "params": map[string]any{
			"sessionId": sid, "toolCall": map[string]any{"toolCallId": "demo", "kind": kind, "title": "Demo work",
				"rawInput": map[string]any{"ServerName": "glitched"}},
			"options": []any{map[string]any{"optionId": "allow", "kind": "allow_once", "name": "Allow"}},
		}})
	}
	options := func(model string) []any {
		return []any{map[string]any{"id": "model", "name": "Model", "category": "model", "type": "select", "currentValue": model,
			"options": []any{map[string]any{"value": "a", "name": "A"}, map[string]any{"value": "b", "name": "B"}}}}
	}
	var promptID json.RawMessage
	r := bufio.NewScanner(os.Stdin)
	for r.Scan() {
		var req struct {
			ID     json.RawMessage
			Method string
			Result json.RawMessage
		}
		if err := json.Unmarshal(r.Bytes(), &req); err != nil {
			panic(err)
		}
		switch req.Method {
		case "initialize":
			if mode == "init-error" {
				w.Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "error": map[string]any{"code": -32603, "message": "Could not initialize local profile"}})
				continue
			}
			if mode == "version" {
				re(req.ID, map[string]any{"protocolVersion": 999, "agentCapabilities": map[string]any{}})
				continue
			}
			re(req.ID, map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]any{}})
		case "session/new":
			if mode == "late-auth" {
				os.Stdout.Close()
				time.Sleep(400 * time.Millisecond)
				fmt.Fprintln(os.Stderr, "Please sign in")
				time.Sleep(150 * time.Millisecond)
				return
			}
			re(req.ID, map[string]any{"sessionId": "ours", "configOptions": options("a")})
		case "session/set_config_option":
			if mode == "handoff" && turns > 0 {
				note("ours", "I kept going on my own.")
			}
			re(req.ID, map[string]any{"configOptions": options("b")})
		case "session/prompt":
			turns++
			if mode == "deny-fallback" || mode == "foreign-permission" {
				sid := "ours"
				if mode == "foreign-permission" {
					sid = "foreign"
				}
				promptID = req.ID
				w.Encode(map[string]any{"jsonrpc": "2.0", "id": 100, "method": "session/request_permission", "params": map[string]any{
					"sessionId": sid, "toolCall": map[string]any{"toolCallId": "check", "kind": "read", "title": "Read notes"},
					"options": []any{map[string]any{"optionId": "aa", "kind": "allow_always", "name": "Always allow"}, map[string]any{"optionId": "dd", "kind": "reject_always", "name": "Always reject"}},
				}})
				continue
			}
			if mode == "unknown-end" {
				re(req.ID, map[string]any{})
				continue
			}
			if mode == "crash-child" {
				c := exec.Command("/bin/sleep", "15")
				if err := c.Start(); err != nil {
					panic(err)
				}
				os.WriteFile(os.Getenv("ACP_WIRE_FAKE_OUT"), []byte(strconv.Itoa(c.Process.Pid)), 0600)
				return
			}
			if mode == "unowned" && turns == 1 {
				re(req.ID, map[string]any{"stopReason": "end_turn"})
				dir := os.Getenv("ACP_WIRE_FAKE_OUT")
				go func() {
					for _, phase := range []string{"between", "before"} {
						for {
							if _, err := os.Stat(filepath.Join(dir, phase)); err == nil {
								break
							}
							time.Sleep(time.Millisecond)
						}
						note("ours", "unsolicited-"+phase)
					}
				}()
				continue
			}
			switch mode {
			case "system-child":
				child := exec.Command("/bin/sleep", "60")
				if err := child.Start(); err != nil {
					panic(err)
				}
				os.WriteFile(os.Getenv("ACP_WIRE_FAKE_OUT"), []byte(strconv.Itoa(child.Process.Pid)), 0o600)
			case "foreign-session":
				note("other", "text from another session")
			case "late-permission":
				promptID = req.ID
				note("ours", "Ready to be stopped.")
				continue
			case "oversize":
				note("ours", strings.Repeat("x", 12<<20))
				note("ours", "a trailing update")
				return
			}
			re(req.ID, map[string]any{"stopReason": "end_turn"})
		case "session/cancel":
			permission("ours", "other")
		case "":
			if (mode == "deny-fallback" || mode == "foreign-permission") && string(req.ID) == "100" {
				note("ours", string(req.Result))
				re(promptID, map[string]any{"stopReason": "end_turn"})
			}
			if mode == "late-permission" && string(req.ID) == "100" {
				os.WriteFile(os.Getenv("ACP_WIRE_FAKE_OUT"), req.Result, 0o600)
				re(promptID, map[string]any{"stopReason": "cancelled"})
			}
		}
	}
}

func wireFake(t *testing.T, mode string) *Adapter {
	t.Helper()
	a, _ := fake(t, "")
	a.Profile.Args = []string{"wire-fake", mode}
	return a
}

func TestCloseStopsAToolItsAgentLeftWithNoMark(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS platform executables")
	}
	out := filepath.Join(t.TempDir(), "pid")
	t.Setenv("ACP_WIRE_FAKE_OUT", out)
	a := wireFake(t, "system-child")
	g := open(t, a, spec(t))
	defer closeRun(t, g)
	do(t, g, provider.Start{Text: "Run fake child."})
	turn(t, g)
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	child, err := strconv.Atoi(string(b))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { syscall.Kill(child, syscall.SIGKILL) })
	p := g.(*Agent).p
	t.Logf("before close: parent %d, live child %d, visible ACP mark %v", p.cmd.Process.Pid, child, carries(child, p.mark))
	closeRun(t, g)
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if alive(child) {
		ps, _ := exec.Command("/bin/ps", "-o", "pid=,ppid=,pgid=,stat=,comm=", "-p", strconv.Itoa(child)).CombinedOutput()
		t.Fatalf("adapter Close returned nil; child is still live: %s", ps)
	}
}

func TestASignInLineAfterStdoutClosesIsSignedOut(t *testing.T) {
	a := wireFake(t, "late-auth")
	g, err := a.Open(context.Background(), spec(t))
	if g != nil {
		closeRun(t, g)
	}
	if !errors.Is(err, provider.ErrSignedOut) {
		t.Fatalf("fake closed stdout, printed 'Please sign in', and exited; Open returned %v (signed out = %v)", err, errors.Is(err, provider.ErrSignedOut))
	}
}

func TestAnotherSessionsTextStaysOutOfTheRun(t *testing.T) {
	a := wireFake(t, "foreign-session")
	g := open(t, a, spec(t))
	defer closeRun(t, g)
	do(t, g, provider.Start{Text: "Do not read another session."})
	if got := text(turn(t, g)); got != "" {
		t.Fatalf("active session %q received %q from the fake's other session", g.(*Agent).p.session, got)
	}
}

func TestATrustedToolAskedAfterAStopIsCancelled(t *testing.T) {
	out := filepath.Join(t.TempDir(), "permission-result")
	t.Setenv("ACP_WIRE_FAKE_OUT", out)
	a := wireFake(t, "late-permission")
	g := open(t, a, spec(t))
	defer closeRun(t, g)
	do(t, g, provider.Start{Text: "Request a harmless demo."})
	next(t, g)
	do(t, g, provider.Interrupt{})
	if end := ended(t, turn(t, g)); end.Outcome != api.OutcomeInterrupted {
		t.Fatalf("expected interrupted, got %+v", end)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"outcome":"cancelled"`) {
		t.Fatalf("fake asked for permission only after receiving session/cancel, got %s; want cancelled", b)
	}
}

func TestAnAgentThatSpeaksAsItWakesIsReplaced(t *testing.T) {
	a := wireFake(t, "handoff")
	s := spec(t)
	g := open(t, a, s)
	do(t, g, provider.Start{Text: "First."})
	turn(t, g)
	first := g.(*Agent).p.cmd.Process.Pid
	closeRun(t, g)
	s.Model.Model = "b"
	g = open(t, a, s)
	defer closeRun(t, g)
	second := g.(*Agent).p.cmd.Process.Pid
	if first == second {
		t.Fatalf("Open reused process %d after it sent unsolicited text before the set_config_option response and before Start", second)
	}
}

func TestALineTooLongForTheSDKLetsTheBridgeGo(t *testing.T) {
	a := wireFake(t, "oversize")
	g := open(t, a, spec(t))
	do(t, g, provider.Start{Text: "Return a large tool output."})
	end := ended(t, turn(t, g))
	closeRun(t, g)
	t.Logf("turn result: %s", end.Error)
	buf := make([]byte, 1<<20)
	stacks := string(buf[:runtime.Stack(buf, true)])
	for block := range strings.SplitSeq(stacks, "\n\n") {
		if strings.Contains(block, "(*proc).readStdout.func2") && strings.Contains(block, "io.(*pipe).write") {
			t.Fatalf("close returned nil but stdout goroutine is stuck with no reader:\n%s", block)
		}
	}
}

func TestACommandsWordsStayApartOnItsCard(t *testing.T) {
	one := step{kind: acpsdk.ToolKindExecute, input: json.RawMessage(`{"command":["demo","a b"]}`)}
	two := step{kind: acpsdk.ToolKindExecute, input: json.RawMessage(`{"command":["demo","a","b"]}`)}
	a, b := one.approval("/demo-workspace", "Antigravity"), two.approval("/demo-workspace", "Antigravity")
	if string(a) == string(b) {
		t.Fatalf("different argv %s and %s produce indistinguishable cards %s", one.input, two.input, a)
	}
}

func TestARelativeAgentPathRunsFromTheWorkspace(t *testing.T) {
	engineWD := t.TempDir()
	t.Chdir(engineWD)
	a := wireFake(t, "plain")
	if err := os.Symlink(a.Path, "fake-agent"); err != nil {
		t.Fatal(err)
	}
	a.Path = "./fake-agent"
	bin, err := a.Binary()
	if err != nil {
		t.Fatal(err)
	}
	g, err := a.Open(context.Background(), spec(t))
	if g != nil {
		closeRun(t, g)
	}
	if err != nil {
		t.Fatalf("Binary found %q from the engine directory, but Open could not run it from the workspace: %v", bin, err)
	}
}

func TestAPathThroughASymlinkIsShownAsGiven(t *testing.T) {
	ws, outside := t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(outside, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "sub"), filepath.Join(ws, "via")); err != nil {
		t.Fatal(err)
	}
	for dir, contents := range map[string]string{ws: "workspace", outside: "outside"} {
		if err := os.WriteFile(filepath.Join(dir, "note"), []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	target := ws + "/via/../note"
	f, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if shown := rel(ws, target); shown != target {
		t.Fatalf("the OS opens %q for %q, but rel shows it as %q", f, target, shown)
	}
}

func TestARunStartedOtherwiseGetsItsOwnAgent(t *testing.T) {
	for _, change := range []string{"MCP", "access", "workspace"} {
		t.Run(change, func(t *testing.T) {
			a := wireFake(t, "plain")
			s := spec(t)
			g := open(t, a, s)
			do(t, g, provider.Start{Text: "First."})
			turn(t, g)
			first := g.(*Agent).p.cmd.Process.Pid
			closeRun(t, g)
			switch change {
			case "MCP":
				s.MCP[0].Env["GLITCHED_THREAD"] = "new"
			case "access":
				s.Access = api.AccessFull
			case "workspace":
				s.Workspace = t.TempDir()
			}
			g = open(t, a, s)
			defer closeRun(t, g)
			if second := g.(*Agent).p.cmd.Process.Pid; first == second {
				t.Fatalf("reused PID %d after changing %s", second, change)
			}
		})
	}
}

// Close has the bridge from stdout let go of a line the SDK stopped
// reading partway.
func TestCloseReleasesAStalledStdoutReader(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	p := &proc{mark: "test-" + api.NewID().String(), stdin: w, reaped: make(chan struct{}), read: make(chan struct{})}
	sdk := p.readStdout(r, io.Discard)
	defer sdk.Close()
	reap := sync.OnceFunc(func() { close(p.reaped) })
	defer reap()
	if _, err := io.WriteString(w, "{}\n"); err != nil {
		t.Fatal(err)
	}
	var first [1]byte
	if _, err := io.ReadFull(sdk, first[:]); err != nil {
		t.Fatal(err)
	}
	reap()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if err := p.Close(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.read:
	default:
		t.Fatal("Close returned with the bridge still holding a line")
	}
}

package browsermcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// session runs Serve with call, and returns a func that sends a message
// and, for one with an id, the answer to it.
func session(t *testing.T, call Call) func(msg string) map[string]any {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- Serve(context.Background(), inR, outW, call); outW.Close() }()
	t.Cleanup(func() {
		inW.Close()
		if err := <-done; err != nil {
			t.Errorf("Serve: %v", err)
		}
	})
	lines := bufio.NewScanner(outR)
	return func(msg string) map[string]any {
		t.Helper()
		if _, err := io.WriteString(inW, msg+"\n"); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(msg, `"id"`) {
			return nil
		}
		if !lines.Scan() {
			t.Fatalf("no answer to %s", msg)
		}
		var r map[string]any
		if err := json.Unmarshal(lines.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		return r
	}
}

// socketDir is a directory for a socket: t.TempDir is too long a path
// for one on macOS, whose socket paths end at 104 bytes.
func socketDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "gl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func TestServeSpeaksMCP(t *testing.T) {
	var got []string
	send := session(t, func(_ context.Context, tool string, args json.RawMessage) (string, error) {
		got = append(got, tool+" "+string(args))
		if tool == "browser_click" {
			return "", errors.New("s1e1 is disabled")
		}
		return "snapshot", nil
	})
	init := send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"claude-code","version":"2"}}}`)
	res := init["result"].(map[string]any)
	if res["protocolVersion"] != "2025-03-26" || res["capabilities"].(map[string]any)["tools"] == nil {
		t.Errorf("initialize answered %v", init)
	}
	// A version it doesn't speak gets its latest, for the client to judge.
	if v := send(`{"jsonrpc":"2.0","id":2,"method":"initialize","params":{"protocolVersion":"1999-01-01"}}`)["result"].(map[string]any)["protocolVersion"]; v != Versions[0] {
		t.Errorf("an unknown version got %v", v)
	}
	send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	list := send(`{"jsonrpc":"2.0","id":"l","method":"tools/list"}`)
	var names []string
	for _, tool := range list["result"].(map[string]any)["tools"].([]any) {
		m := tool.(map[string]any)
		names = append(names, m["name"].(string))
		if m["inputSchema"].(map[string]any)["type"] != "object" {
			t.Errorf("%s's schema is %v", m["name"], m["inputSchema"])
		}
		// Every tool changes something: a snapshot loads a tab's page
		// and replaces its refs, so none may run unasked or at once.
		if m["annotations"].(map[string]any)["readOnlyHint"] != false {
			t.Errorf("%s is said to be read-only", m["name"])
		}
	}
	if want := "browser_tabs browser_navigate browser_snapshot browser_click browser_type"; strings.Join(names, " ") != want {
		t.Errorf("tools %v, want %s", names, want)
	}
	if list["id"] != "l" {
		t.Errorf("answered id %v, want l", list["id"])
	}
	ok := send(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"browser_snapshot","arguments":{"tab":2}}}`)
	if r := ok["result"].(map[string]any); r["isError"] != nil || r["content"].([]any)[0].(map[string]any)["text"] != "snapshot" {
		t.Errorf("a call answered %v", ok)
	}
	failed := send(`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"browser_click","arguments":{"ref":"s1e1"}}}`)
	if r := failed["result"].(map[string]any); r["isError"] != true || r["content"].([]any)[0].(map[string]any)["text"] != "s1e1 is disabled" {
		t.Errorf("a failed call answered %v", failed)
	}
	if e := send(`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"rm_rf"}}`)["error"]; e == nil {
		t.Error("an unknown tool was called")
	}
	if e := send(`{"jsonrpc":"2.0","id":6,"method":"resources/list"}`)["error"].(map[string]any); e["code"] != float64(-32601) {
		t.Errorf("an unknown method: %v", e)
	}
	if want := []string{`browser_snapshot {"tab":2}`, `browser_click {"ref":"s1e1"}`}; strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("calls %q, want %q", got, want)
	}
}

// A call that takes its time doesn't hold up the next.
func TestServeAnswersCallsAsTheyEnd(t *testing.T) {
	slow := make(chan struct{})
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	go Serve(context.Background(), inR, outW, func(_ context.Context, tool string, _ json.RawMessage) (string, error) {
		if tool == "browser_navigate" {
			<-slow
		}
		return tool, nil
	})
	defer inW.Close()
	go io.WriteString(inW, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"browser_navigate","arguments":{"url":"go.dev"}}}`+"\n"+
		`{"jsonrpc":"2.0","id":2,"method":"ping"}`+"\n")
	lines := bufio.NewScanner(outR)
	var ids []string
	for range 2 {
		if !lines.Scan() {
			t.Fatal("no answer")
		}
		var r struct{ ID json.RawMessage }
		json.Unmarshal(lines.Bytes(), &r)
		ids = append(ids, string(r.ID))
		if len(ids) == 1 {
			close(slow)
		}
	}
	if strings.Join(ids, " ") != "2 1" {
		t.Errorf("answered %v, want the ping before the call", ids)
	}
}

func TestTheBridgeReachesTheApp(t *testing.T) {
	dir := socketDir(t)
	call := Dial(dir)
	if _, err := call(context.Background(), "browser_tabs", json.RawMessage(`{}`)); !errors.Is(err, ErrNoApp) {
		t.Fatalf("with no app: %v", err)
	}
	// A socket an app left as it died is no app either, and the next
	// app's listen takes its place.
	dead, err := net.Listen("unix", Socket(dir))
	if err != nil {
		t.Fatal(err)
	}
	dead.(*net.UnixListener).SetUnlinkOnClose(false)
	dead.Close()
	if _, err := os.Stat(Socket(dir)); err != nil {
		t.Fatal(err)
	}
	ln, err := Listen(dir)
	if err != nil {
		t.Fatal(err)
	}
	hungUp := make(chan struct{})
	go Accept(ln, func(ctx context.Context, tool string, args json.RawMessage) (string, error) {
		switch tool {
		case "browser_click":
			return "", errors.New("s1e1 is gone")
		case "browser_navigate":
			<-ctx.Done()
			close(hungUp)
			return "", ctx.Err()
		}
		return tool + " " + string(args), nil
	})
	defer ln.Close()
	if _, err := Listen(dir); err == nil || !strings.Contains(err.Error(), "another Glitched app") {
		t.Errorf("a second app listening: %v", err)
	}
	if got, err := call(context.Background(), "browser_tabs", json.RawMessage(`{"action":"list"}`)); err != nil || got != `browser_tabs {"action":"list"}` {
		t.Errorf("a call answered %q, %v", got, err)
	}
	if _, err := call(context.Background(), "browser_click", json.RawMessage(`{}`)); err == nil || err.Error() != "s1e1 is gone" {
		t.Errorf("a failed call: %v", err)
	}
	// A call the agent gives up on ends in the app too.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := call(ctx, "browser_navigate", json.RawMessage(`{}`)); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("a call given up on: %v", err)
	}
	select {
	case <-hungUp:
	case <-time.After(5 * time.Second):
		t.Error("the app's call went on after the agent gave up")
	}
	if filepath.Base(Socket(dir)) != "browser.sock" {
		t.Error(Socket(dir))
	}
}

// A call the client cancels, as the user stops the turn, ends in the app,
// and so do the calls still running as the client goes.
func TestServeEndsCallsTheClientLeaves(t *testing.T) {
	started := make(chan string, 2)
	ended := make(chan string, 2)
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	answers := make(chan string, 4)
	go func() {
		lines := bufio.NewScanner(outR)
		for lines.Scan() {
			answers <- lines.Text()
		}
	}()
	done := make(chan error, 1)
	go func() {
		defer outW.Close()
		done <- Serve(context.Background(), inR, outW, func(ctx context.Context, tool string, args json.RawMessage) (string, error) {
			started <- string(args)
			<-ctx.Done()
			ended <- string(args)
			return "", ctx.Err()
		})
	}()
	io.WriteString(inW, `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"browser_navigate","arguments":{"url":"a"}}}`+"\n"+
		`{"jsonrpc":"2.0","id":"8","method":"tools/call","params":{"name":"browser_navigate","arguments":{"url":"b"}}}`+"\n")
	<-started
	<-started
	io.WriteString(inW, `{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":7,"reason":"the user stopped"}}`+"\n")
	select {
	case got := <-ended:
		if got != `{"url":"a"}` {
			t.Errorf("cancelling call 7 ended %s", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a cancelled call went on")
	}
	// A cancelled call gets no answer.
	select {
	case a := <-answers:
		t.Errorf("a cancelled call was answered: %s", a)
	case <-time.After(100 * time.Millisecond):
	}
	inW.Close()
	select {
	case got := <-ended:
		if got != `{"url":"b"}` {
			t.Errorf("the client going ended %s", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a call went on after the client went")
	}
	if err := <-done; err != nil {
		t.Error(err)
	}
}

// A caller that sends no request is hung up on.
func TestTheBridgeHangsUpOnASilentCaller(t *testing.T) {
	old := requestTime.Swap(int64(50 * time.Millisecond))
	t.Cleanup(func() { requestTime.Store(old) })
	ln, err := Listen(socketDir(t))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go Accept(ln, func(context.Context, string, json.RawMessage) (string, error) { return "", nil })
	conn, err := net.Dial("unix", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Errorf("a silent caller's read: %v, want the app to hang up", err)
	}
}

// A call the agent gave up on says so, and a socket the server may not
// use says why, rather than that the app isn't open.
func TestTheBridgeSaysWhyItCantReachTheApp(t *testing.T) {
	dir := socketDir(t)
	ln, err := Listen(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go Accept(ln, func(context.Context, string, json.RawMessage) (string, error) { return "ok", nil })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Dial(dir)(ctx, "browser_tabs", json.RawMessage(`{}`)); !errors.Is(err, context.Canceled) {
		t.Errorf("a call given up on: %v", err)
	}
	if os.Geteuid() == 0 {
		t.Skip("root reaches a socket whatever its mode")
	}
	os.Chmod(Socket(dir), 0)
	if _, err := Dial(dir)(context.Background(), "browser_tabs", json.RawMessage(`{}`)); err == nil || errors.Is(err, ErrNoApp) || !strings.Contains(err.Error(), "can't be reached") {
		t.Errorf("a socket the server may not use: %v", err)
	}
}

// The socket is the user's alone.
func TestTheBridgeSocketIsTheUsers(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no modes")
	}
	ln, err := Listen(socketDir(t))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if fi, err := os.Stat(ln.Addr().String()); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("the socket: %v %v, want 0600", fi.Mode().Perm(), err)
	}
}

// flaky is a listener whose first accepts fail as too many files open
// would.
type flaky struct {
	net.Listener
	fails int
}

type tempErr struct{}

func (tempErr) Error() string   { return "too many open files" }
func (tempErr) Temporary() bool { return true }
func (tempErr) Timeout() bool   { return false }

func (l *flaky) Accept() (net.Conn, error) {
	if l.fails > 0 {
		l.fails--
		return nil, tempErr{}
	}
	return l.Listener.Accept()
}

// An accept failing for a while doesn't stop the socket answering.
func TestTheBridgeOutlastsAFailingAccept(t *testing.T) {
	dir := socketDir(t)
	ln, err := Listen(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go Accept(&flaky{Listener: ln, fails: 3}, func(context.Context, string, json.RawMessage) (string, error) { return "ok", nil })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if got, err := Dial(dir)(ctx, "browser_tabs", json.RawMessage(`{}`)); err != nil || got != "ok" {
		t.Errorf("after accepts failed: %q, %v", got, err)
	}
}

// A caller sending more after its request is still there: only its
// connection ending is it hanging up.
func TestTheBridgeHearsOnlyAHangUp(t *testing.T) {
	dir := socketDir(t)
	ln, err := Listen(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go Accept(ln, func(ctx context.Context, _ string, _ json.RawMessage) (string, error) {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(200 * time.Millisecond):
			return "ok", nil
		}
	})
	conn, err := net.Dial("unix", Socket(dir))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	io.WriteString(conn, `{"tool":"browser_tabs","args":{}}`+"\n\n")
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil || !strings.Contains(line, `"text":"ok"`) {
		t.Errorf("a caller that sent a blank line after: %q, %v", line, err)
	}
}

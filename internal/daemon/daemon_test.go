package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/coder/websocket"

	"glitched/internal/api"
	"glitched/internal/browsermcp"
	"glitched/internal/engine"
	"glitched/internal/provider"
	"glitched/internal/provider/claude"
	"glitched/internal/provider/fake"
	"glitched/internal/transport"
)

// stateDir is short: unix socket paths are limited to about 100 bytes.
func stateDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "gl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func start(t *testing.T, cfg Config) *Daemon {
	t.Helper()
	cfg.Hub = true
	cfg.Providers = provider.Mux{"fake": fake.New(fake.Turn(true))}
	d, err := Start(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	return d
}

func remote(t *testing.T, d *Daemon, token string) *transport.EndpointClient {
	t.Helper()
	c := transport.NewEndpointClient(transport.Tailnet{Address: d.Addrs()[0].String(), Token: token}, api.ID{})
	t.Cleanup(func() { c.Close() })
	return c
}

func TestStartTwiceOnOneDirIsLocked(t *testing.T) {
	dir := stateDir(t)
	d, err := Start(context.Background(), Config{Dir: dir, Hub: true, Providers: provider.Mux{"fake": fake.New(nil)}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Start(context.Background(), Config{Dir: dir, Hub: true, Providers: provider.Mux{"fake": fake.New(nil)}}); !errors.Is(err, ErrLocked) {
		t.Fatalf("a second engine on the dir: %v, want ErrLocked", err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	start(t, Config{Dir: dir})
}

func TestStartRefusesAListenAddressOffTheTailnet(t *testing.T) {
	dir := stateDir(t)
	for _, addr := range []string{"0.0.0.0:7465", "8.8.8.8:7465"} {
		_, err := Start(context.Background(), Config{Dir: dir, Hub: true, Listen: []string{addr}, Providers: provider.Mux{"fake": fake.New(nil)}})
		if err == nil || !strings.Contains(err.Error(), "Tailscale") {
			t.Fatalf("listen on %s: %v, want the tailnet rule", addr, err)
		}
	}
	// The failed starts released the lock and the store.
	start(t, Config{Dir: dir})
}

func TestTCPAdmitsOnlyAPairedToken(t *testing.T) {
	dir := stateDir(t)
	token, err := Pair(dir, "laptop")
	if err != nil {
		t.Fatal(err)
	}
	d := start(t, Config{Dir: dir, Listen: []string{"127.0.0.1:0"}})

	ws, _, err := websocket.Dial(context.Background(), "ws://"+d.Addrs()[0].String()+"/ws", &websocket.DialOptions{
		HTTPHeader: http.Header{"Glitched-Protocol": {fmt.Sprint(api.Protocol)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	if _, _, err := ws.Read(context.Background()); err != nil {
		t.Fatalf("the engine's challenge: %v", err)
	}
	ws.Write(context.Background(), websocket.MessageText, []byte(`{"id":1,"method":"info","params":{}}`))
	if _, b, err := ws.Read(context.Background()); websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
		t.Fatalf("a call in place of a proof got %s %v, want the socket closed", b, err)
	}
	for name, tok := range map[string]string{"no token": "", "a wrong token": token + "x"} {
		if _, err := remote(t, d, tok).Info(context.Background()); !errors.Is(err, transport.ErrRefused) {
			t.Errorf("%s: %v, want ErrRefused", name, err)
		}
	}
	env, err := remote(t, d, token).Info(context.Background())
	if err != nil {
		t.Fatalf("a paired token: %v", err)
	}
	if local, _ := d.Endpoint().Info(context.Background()); env.ID != local.ID {
		t.Fatalf("reached host %s, want %s", env.ID, local.ID)
	}

	// The engine is not restarted between pairing, use and removing.
	phone, err := Pair(dir, "phone")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := remote(t, d, phone).Info(context.Background()); err != nil {
		t.Fatalf("a token paired while serving: %v", err)
	}
	if err := RemoveToken(dir, "phone"); err != nil {
		t.Fatal(err)
	}
	if _, err := remote(t, d, phone).Info(context.Background()); !errors.Is(err, transport.ErrRefused) {
		t.Fatalf("a new socket with a removed token: %v, want ErrRefused", err)
	}
}

func TestRemovingATokenClosesItsLiveSockets(t *testing.T) {
	dir := stateDir(t)
	token, err := Pair(dir, "laptop")
	if err != nil {
		t.Fatal(err)
	}
	d := start(t, Config{Dir: dir, Listen: []string{"127.0.0.1:0"}})
	live := remote(t, d, token)
	if _, err := live.Info(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := RemoveToken(dir, "laptop"); err != nil {
		t.Fatal(err)
	}
	// Nothing dials meanwhile, so only the engine's own reread can close the
	// live socket; the client then redials and is refused.
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, err := live.Info(context.Background())
		if errors.Is(err, transport.ErrRefused) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("a socket admitted before the removal still answers: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestUnixSocketNeedsNoToken(t *testing.T) {
	dir := stateDir(t)
	d := start(t, Config{Dir: dir, Listen: []string{"127.0.0.1:0"}})
	local := transport.NewEndpointClient(Socket(dir), api.ID{})
	defer local.Close()
	if _, err := local.Info(context.Background()); err != nil {
		t.Fatalf("the unix socket: %v", err)
	}
	if _, err := remote(t, d, "").Info(context.Background()); !errors.Is(err, transport.ErrRefused) {
		t.Fatalf("TCP on the same engine without a token: %v, want ErrRefused", err)
	}
}

func TestPairKeepsOnlyTheTokensHash(t *testing.T) {
	dir := stateDir(t)
	token, err := Pair(dir, "laptop")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Pair(dir, "laptop"); err == nil {
		t.Fatal("a second token with one label")
	}
	path := filepath.Join(dir, "tokens.json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), token) {
		t.Fatalf("tokens.json holds the token itself:\n%s", b)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != fs.FileMode(0o600) {
		t.Fatalf("tokens.json has mode %o, want 600", fi.Mode().Perm())
	}
	ts, err := Tokens(dir)
	if err != nil || len(ts) != 1 || ts[0].Label != "laptop" || ts[0].Created.IsZero() {
		t.Fatalf("tokens: %+v %v", ts, err)
	}
	if err := RemoveToken(dir, "phone"); err == nil {
		t.Fatal("removed a label that has no token")
	}
}

// Pairs run at once each wait for the others, so every token lands.
func TestPairsAtOnceAllLand(t *testing.T) {
	dir := stateDir(t)
	var wg sync.WaitGroup
	for i := range 16 {
		wg.Go(func() {
			if _, err := Pair(dir, fmt.Sprint("device", i)); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if ts, err := Tokens(dir); err != nil || len(ts) != 16 {
		t.Fatalf("tokens.json holds %d tokens (%v), want the 16 paired", len(ts), err)
	}
}

// unassigned is a Tailscale address this machine doesn't hold.
const unassigned = "100.64.0.77:7465"

func TestStartServesWhileATailnetAddressIsUnassigned(t *testing.T) {
	dir := stateDir(t)
	token, err := Pair(dir, "laptop")
	if err != nil {
		t.Fatal(err)
	}
	d, err := Start(context.Background(), Config{Dir: dir, Hub: true, Listen: []string{"127.0.0.1:0", unassigned}, Providers: provider.Mux{"fake": fake.New(nil)}})
	if err != nil {
		t.Fatalf("start with a tailnet address not assigned yet: %v", err)
	}
	local := transport.NewEndpointClient(Socket(dir), api.ID{})
	defer local.Close()
	if _, err := local.Info(context.Background()); err != nil {
		t.Fatalf("the unix socket: %v", err)
	}
	if _, err := remote(t, d, token).Info(context.Background()); err != nil {
		t.Fatalf("the loopback listener: %v", err)
	}
	closed := make(chan error, 1)
	go func() { closed <- d.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("close waited on the address")
	}
}

func TestStartListensOnAnAddressOnceItIsAssigned(t *testing.T) {
	count := logged(t)
	dir := stateDir(t)
	token, err := Pair(dir, "laptop")
	if err != nil {
		t.Fatal(err)
	}
	real := listenOn
	t.Cleanup(func() { listenOn = real })
	var tries atomic.Int32
	assigned := make(chan net.Addr, 1)
	listenOn = func(tn transport.Tailnet, ctx context.Context) (net.Listener, error) {
		if tries.Add(1) <= 2 {
			return real(tn, ctx)
		}
		ln, err := real(transport.Tailnet{Address: "127.0.0.1:0"}, ctx)
		if err == nil {
			assigned <- ln.Addr()
		}
		return ln, err
	}
	d := start(t, Config{Dir: dir, Listen: []string{unassigned}})
	var addr net.Addr
	select {
	case addr = <-assigned:
	case <-time.After(10 * time.Second):
		t.Fatal("never listened once the address was assigned")
	}
	c := transport.NewEndpointClient(transport.Tailnet{Address: addr.String(), Token: token}, api.ID{})
	defer c.Close()
	if _, err := c.Info(context.Background()); err != nil {
		t.Fatalf("the listener bound late: %v", err)
	}
	if got := d.Addrs(); len(got) != 1 || got[0].String() != addr.String() {
		t.Fatalf("addrs %v, want %v", got, addr)
	}
	if n := count("waiting to listen"); n != 1 {
		t.Fatalf("2 failed binds with one error logged %d times, want once", n)
	}
}

// An engine whose hub speaks another protocol says so in its log, not that
// the hub is unreachable.
func TestAHubOfAnotherProtocolIsNamedInTheLog(t *testing.T) {
	hubDir := stateDir(t)
	ln, err := Socket(hubDir).Listen(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// A protocol 1 engine, which announces no protocol.
	older := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ws, err := websocket.Accept(w, r, nil); err == nil {
			ws.Read(context.Background())
			ws.CloseNow()
		}
	})}
	go older.Serve(ln)
	t.Cleanup(func() { older.Close() })
	var logged strings.Builder
	was := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&syncWriter{w: &logged}, nil)))
	t.Cleanup(func() { slog.SetDefault(was) })
	d, err := Start(context.Background(), Config{Dir: stateDir(t), HubDir: hubDir, Providers: provider.Mux{"fake": fake.New(fake.Turn(false))}})
	if err != nil {
		t.Fatal(err)
	}
	d.Close()
	if s := logged.String(); !strings.Contains(s, "the hub speaks another protocol") || strings.Contains(s, "hub unreachable") {
		t.Fatalf("the log: %s", s)
	}
}

type syncWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

// The child limits engine.json keeps are those the engine starts with, and
// SetLimits changes them at once, as the GUI's own engine does.
func TestTheChildLimitsRoundTripThroughEngineJSON(t *testing.T) {
	dir := stateDir(t)
	want := engine.Limits{Working: 2, All: 3, Depth: 1}
	if err := (Config{Dir: dir, Hub: true, Epoch: 1, Children: want}).Save(); err != nil {
		t.Fatal(err)
	}
	cfg, err := Defaults(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Children != want {
		t.Fatalf("engine.json's limits: %+v, want %+v", cfg.Children, want)
	}
	ad := fake.New(fake.Turn(false))
	cfg.Providers = provider.Mux{"fake": ad}
	d, err := Start(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	ep := d.Endpoint()
	ctx := context.Background()
	submit := func(target api.ID, body api.Intent) api.ID {
		t.Helper()
		c := api.Command{ID: api.NewID(), Target: target, Body: body}
		if _, err := ep.Submit(ctx, c); err != nil {
			t.Fatal(err)
		}
		return c.ID
	}
	spawned := func(run api.ID) error {
		t.Helper()
		for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(2 * time.Millisecond) {
			if answered, err := ad.Spawned(fake.ChildOf(run, 0)); answered {
				return err
			}
			if time.Now().After(deadline) {
				t.Fatal("the spawn was never answered")
			}
		}
	}
	ad.Spawns(ep, "split", fake.Child{Title: "kid"})
	ad.Spawns(ep, "nest", fake.Child{Title: "grandkid"})
	parent := api.NewID()
	submit(parent, api.ThreadCreate{Workspace: "/src", Title: "top", Model: api.ModelRef{Provider: "fake", Model: "fake"}})
	split := submit(parent, api.MessageDispatch{Text: "split", Mode: api.StartNow})
	if err := spawned(split); err != nil {
		t.Fatal(err)
	}
	kid := fake.ChildOf(split, 0)
	var rej *api.RejectedError
	if err := spawned(submit(kid, api.MessageDispatch{Text: "nest", Mode: api.AfterActive})); !errors.As(err, &rej) {
		t.Fatalf("a grandchild at depth 2 under engine.json's depth 1: %v, want refused", err)
	}
	d.SetLimits(engine.Limits{Depth: 2})
	if err := spawned(submit(kid, api.MessageDispatch{Text: "nest", Mode: api.AfterActive})); err != nil {
		t.Fatalf("a grandchild after SetLimits raised the depth to 2: %v", err)
	}
}

// specs records the session each run opened with, on top of the fake.
type specs struct {
	*fake.Adapter
	mu   sync.Mutex
	seen []provider.SessionSpec
}

func (s *specs) Open(ctx context.Context, spec provider.SessionSpec) (provider.Agent, error) {
	s.mu.Lock()
	s.seen = append(s.seen, spec)
	s.mu.Unlock()
	return s.Adapter.Open(ctx, spec)
}

// Each run gets glitched mcp from the binary the engine runs in, the CLI's
// or the GUI's, for its thread, dialing the engine's own socket. It names
// no run: an agent kept between runs serves the thread's next one too.
func TestEachRunGetsGlitchedMCPOnTheEnginesSocket(t *testing.T) {
	dir := stateDir(t)
	ad := &specs{Adapter: fake.New(fake.Turn(false))}
	d, err := Start(context.Background(), Config{Dir: dir, Hub: true, Providers: provider.Mux{"fake": ad}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	ep := transport.NewEndpointClient(Socket(dir), api.ID{})
	defer ep.Close()
	ctx := context.Background()
	thread := api.NewID()
	if _, err := ep.Submit(ctx, api.Command{ID: api.NewID(), Target: thread, Body: api.ThreadCreate{Workspace: "/src/app", Title: "t", Model: api.ModelRef{Provider: "fake", Model: "fake"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := ep.Submit(ctx, api.Command{ID: api.NewID(), Target: thread, Body: api.MessageDispatch{Text: "hi", Mode: api.StartNow}}); err != nil {
		t.Fatal(err)
	}
	var seen []provider.SessionSpec
	for deadline := time.Now().Add(5 * time.Second); len(seen) == 0; time.Sleep(5 * time.Millisecond) {
		ad.mu.Lock()
		seen = append([]provider.SessionSpec(nil), ad.seen...)
		ad.mu.Unlock()
		if time.Now().After(deadline) {
			t.Fatal("the run never opened")
		}
	}
	exe, _ := os.Executable()
	want := map[string]string{"GLITCHED_THREAD": thread.String(), "GLITCHED_ACCESS": "0", "GLITCHED_ENGINE": Socket(dir).Address}
	if len(seen[0].MCP) != 1 {
		t.Fatalf("the run's MCP servers %+v, want glitched mcp alone", seen[0].MCP)
	}
	if s := seen[0].MCP[0]; s.Name != "glitched" || s.Command != exe || len(s.Args) != 1 || s.Args[0] != "mcp" || fmt.Sprint(s.Env) != fmt.Sprint(want) || s.Instructions == "" || !s.Trusted {
		t.Errorf("the run's MCP server %+v, want trusted glitched mcp from %s with %v", s, exe, want)
	}
}

// An engine started on a relative Dir hands the agent's glitched mcp the
// socket's absolute path: that server runs in the agent's workspace.
func TestARelativeDirGivesGlitchedMCPAnAbsoluteSocket(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	ad := &specs{Adapter: fake.New(fake.Turn(false))}
	d, err := Start(context.Background(), Config{Dir: "state", Hub: true, Providers: provider.Mux{"fake": ad}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	ep := transport.NewEndpointClient(Socket("state"), api.ID{})
	defer ep.Close()
	ctx := context.Background()
	thread := api.NewID()
	if _, err := ep.Submit(ctx, api.Command{ID: api.NewID(), Target: thread, Body: api.ThreadCreate{Workspace: "/src/app", Title: "t", Model: api.ModelRef{Provider: "fake", Model: "fake"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := ep.Submit(ctx, api.Command{ID: api.NewID(), Target: thread, Body: api.MessageDispatch{Text: "hi", Mode: api.StartNow}}); err != nil {
		t.Fatal(err)
	}
	var seen []provider.SessionSpec
	for deadline := time.Now().Add(5 * time.Second); len(seen) == 0; time.Sleep(5 * time.Millisecond) {
		ad.mu.Lock()
		seen = append([]provider.SessionSpec(nil), ad.seen...)
		ad.mu.Unlock()
		if time.Now().After(deadline) {
			t.Fatal("the run never opened")
		}
	}
	want := filepath.Join(root, "state", "engine.sock")
	if s := seen[0].MCP; len(s) != 1 || s[0].Env["GLITCHED_ENGINE"] != want {
		t.Errorf("the run's MCP servers %+v, want glitched mcp with GLITCHED_ENGINE %s", s, want)
	}
}

// An engine running Claude Code gives each run the browser of the app on
// its state directory, after glitched mcp, at an absolute path: the agent
// runs in the thread's workspace. The browser's tools ask.
func TestWithClaudeGivesRunsTheBrowser(t *testing.T) {
	base := t.TempDir()
	t.Chdir(base)
	cfg, err := Defaults("state")
	if err != nil {
		t.Fatal(err)
	}
	cfg.WithClaude()
	if _, ok := cfg.Providers["claude"].(*claude.Adapter); !ok {
		t.Fatalf("claude's adapter is %T", cfg.Providers["claude"])
	}
	ad := &specs{Adapter: fake.New(fake.Turn(false))}
	cfg.Providers["fake"] = ad
	d, err := Start(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	ep := transport.NewEndpointClient(Socket("state"), api.ID{})
	defer ep.Close()
	ctx := context.Background()
	thread := api.NewID()
	if _, err := ep.Submit(ctx, api.Command{ID: api.NewID(), Target: thread, Body: api.ThreadCreate{Workspace: "/src/app", Title: "t", Model: api.ModelRef{Provider: "fake", Model: "fake"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := ep.Submit(ctx, api.Command{ID: api.NewID(), Target: thread, Body: api.MessageDispatch{Text: "hi", Mode: api.StartNow}}); err != nil {
		t.Fatal(err)
	}
	var seen []provider.SessionSpec
	for deadline := time.Now().Add(5 * time.Second); len(seen) == 0; time.Sleep(5 * time.Millisecond) {
		ad.mu.Lock()
		seen = append([]provider.SessionSpec(nil), ad.seen...)
		ad.mu.Unlock()
		if time.Now().After(deadline) {
			t.Fatal("the run never opened")
		}
	}
	servers := seen[0].MCP
	if len(servers) != 2 || servers[0].Name != "glitched" {
		t.Fatalf("the run's MCP servers %+v, want glitched mcp, then the browser", servers)
	}
	s := servers[1]
	got := append([]string{s.Command}, s.Args...)
	if want := browsermcp.Command(filepath.Join(base, "state")); s.Name != browsermcp.ServerName || s.Trusted || s.Env != nil || !slices.Equal(got, want) {
		t.Errorf("the run's browser %+v, want untrusted %s running %q", s, browsermcp.ServerName, want)
	}
}

// mutating opens each run's agent after changing what the run's spec
// says of its MCP servers, as an adapter that edits them in place would.
type mutating struct {
	*fake.Adapter
	got chan provider.MCPServer // each run's last server, as it came
}

func (m *mutating) Open(ctx context.Context, spec provider.SessionSpec) (provider.Agent, error) {
	last := spec.MCP[len(spec.MCP)-1]
	m.got <- provider.MCPServer{Name: last.Name, Args: slices.Clone(last.Args), Env: maps.Clone(last.Env)}
	for _, s := range spec.MCP {
		s.Args[0] = "mutated"
		if s.Env != nil {
			s.Env["MARK"] = "mutated"
		}
	}
	return m.Adapter.Open(ctx, spec)
}

// What one run's adapter does to its MCP servers never reaches the
// engine's own servers in a later run.
func TestARunsMCPServersAreItsOwn(t *testing.T) {
	dir := stateDir(t)
	ad := &mutating{Adapter: fake.New(fake.Turn(false)), got: make(chan provider.MCPServer, 2)}
	engineServer := provider.MCPServer{Name: "engine", Command: "/engine", Args: []string{"serve"}, Env: map[string]string{"MARK": "original"}}
	d, err := Start(context.Background(), Config{Dir: dir, Hub: true, Providers: provider.Mux{"fake": ad}, MCP: []provider.MCPServer{engineServer}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	ep := transport.NewEndpointClient(Socket(dir), api.ID{})
	defer ep.Close()
	ctx := context.Background()
	for i := range 2 {
		thread := api.NewID()
		for _, body := range []api.Intent{
			api.ThreadCreate{Workspace: "/src/app", Title: "t", Model: api.ModelRef{Provider: "fake", Model: "fake"}},
			api.MessageDispatch{Text: "hi", Mode: api.StartNow},
		} {
			if _, err := ep.Submit(ctx, api.Command{ID: api.NewID(), Target: thread, Body: body}); err != nil {
				t.Fatal(err)
			}
		}
		select {
		case s := <-ad.got:
			if s.Name != "engine" || s.Args[0] != "serve" || s.Env["MARK"] != "original" {
				t.Fatalf("run %d's engine server %+v, want it as the engine has it", i, s)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("run %d never opened", i)
		}
	}
}

// An engine serving the tailnet on an installed Tailscale that has its
// address binds it before anything else, and refuses to start, its store
// never opened, when another process holds the port there.
func TestStartRefusesAnInstalledTailscalesPortInUse(t *testing.T) {
	dir := stateDir(t)
	ip := netip.MustParseAddr("100.64.0.9")
	addr := net.JoinHostPort(ip.String(), Port)
	real := listenOn
	t.Cleanup(func() { listenOn = real })
	listenOn = func(tn transport.Tailnet, ctx context.Context) (net.Listener, error) {
		if tn.Address == addr {
			return nil, &net.OpError{Op: "listen", Net: "tcp", Err: os.NewSyscallError("bind", syscall.EADDRINUSE)}
		}
		return real(tn, ctx)
	}
	running := func(context.Context) (api.Tailnet, error) { return api.Tailnet{State: "Running", IP: ip}, nil }
	_, err := Start(context.Background(), Config{Dir: dir, Hub: true, Tailnet: true, Detect: running, Providers: provider.Mux{"fake": fake.New(nil)}})
	if want := "can't listen on " + addr + ": address already in use"; err == nil || err.Error() != want || !errors.Is(err, syscall.EADDRINUSE) {
		t.Fatalf("start with the tailnet's port in use: %v, want %q", err, want)
	}
	if _, err := os.Stat(filepath.Join(dir, "engine.db")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the refused start opened its store: %v", err)
	}
	if _, err := os.Stat(Socket(dir).Address); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the refused start left its socket: %v", err)
	}
}

// A loopback address the engine can't bind refuses the start, even when the
// system says it isn't assigned: only a Tailscale address is waited for.
func TestStartRefusesALoopbackAddressItCantBind(t *testing.T) {
	dir := stateDir(t)
	real := listenOn
	t.Cleanup(func() { listenOn = real })
	listenOn = func(tn transport.Tailnet, ctx context.Context) (net.Listener, error) {
		return nil, &net.OpError{Op: "listen", Net: "tcp", Err: os.NewSyscallError("bind", syscall.EADDRNOTAVAIL)}
	}
	for _, addr := range []string{"127.0.0.42:7465", "[::ffff:127.0.0.42%lo0]:7465"} {
		_, err := Start(context.Background(), Config{Dir: dir, Hub: true, Listen: []string{addr}, Providers: provider.Mux{"fake": fake.New(nil)}})
		if err == nil || !strings.HasPrefix(err.Error(), "can't listen on "+addr+": ") {
			t.Fatalf("start on a loopback address it can't bind: %v, want a refusal naming %s", err, addr)
		}
		if _, err := os.Stat(filepath.Join(dir, "engine.db")); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("the refused start on %s opened its store: %v", addr, err)
		}
	}
}

// A Tailscale address assigned after the engine started whose port another
// process holds stops the engine with where and why, rather than waiting
// for the port.
func TestALateAddressWhosePortIsTakenStopsTheEngine(t *testing.T) {
	quiet(t)
	real := listenOn
	t.Cleanup(func() { listenOn = real })
	var tries atomic.Int32
	listenOn = func(tn transport.Tailnet, ctx context.Context) (net.Listener, error) {
		if tn.Address != unassigned {
			return real(tn, ctx)
		}
		errno := syscall.EADDRINUSE
		if tries.Add(1) == 1 {
			errno = syscall.EADDRNOTAVAIL
		}
		return nil, &net.OpError{Op: "listen", Net: "tcp", Err: os.NewSyscallError("bind", errno)}
	}
	d, err := Start(context.Background(), Config{Dir: stateDir(t), Hub: true, Listen: []string{unassigned}, Providers: provider.Mux{"fake": fake.New(nil)}})
	if err != nil {
		t.Fatalf("start while the address is unassigned: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	stopped := make(chan error, 1)
	go func() { stopped <- d.Wait() }()
	select {
	case err := <-stopped:
		if want := "can't listen on " + unassigned + ": address already in use"; err == nil || err.Error() != want {
			t.Fatalf("the engine stopped with %v, want %q", err, want)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("the engine kept waiting for a port in use after %d binds", tries.Load())
	}
}

// An installed Tailscale's address that -listen names in another spelling
// is bound once, by -listen.
func TestAnInstalledAddressListenSpellsOtherwiseBindsOnce(t *testing.T) {
	quiet(t)
	ip := netip.MustParseAddr("100.64.0.9")
	mapped := net.JoinHostPort(netip.AddrFrom16(ip.As16()).String(), Port)
	real := listenOn
	t.Cleanup(func() { listenOn = real })
	var binds atomic.Int32
	listenOn = func(tn transport.Tailnet, ctx context.Context) (net.Listener, error) {
		if binds.Add(1) > 1 {
			return nil, &net.OpError{Op: "listen", Net: "tcp", Err: os.NewSyscallError("bind", syscall.EADDRINUSE)}
		}
		return real(transport.Tailnet{Address: "127.0.0.1:0"}, ctx)
	}
	running := func(context.Context) (api.Tailnet, error) { return api.Tailnet{State: "Running", IP: ip}, nil }
	d, err := Start(context.Background(), Config{Dir: stateDir(t), Hub: true, Tailnet: true, Listen: []string{mapped}, Detect: running, Providers: provider.Mux{"fake": fake.New(nil)}})
	if err != nil {
		t.Fatalf("start with %s listed and Tailscale at %s: %v", mapped, ip, err)
	}
	t.Cleanup(func() { d.Close() })
	if n := binds.Load(); n != 1 {
		t.Fatalf("%d binds, want 1", n)
	}
}

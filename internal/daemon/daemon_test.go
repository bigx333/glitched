package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"glitched/internal/api"
	"glitched/internal/provider"
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

	// The engine is not restarted between pairing, use and revoking.
	phone, err := Pair(dir, "phone")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := remote(t, d, phone).Info(context.Background()); err != nil {
		t.Fatalf("a token paired while serving: %v", err)
	}
	if err := Revoke(dir, "phone"); err != nil {
		t.Fatal(err)
	}
	if _, err := remote(t, d, phone).Info(context.Background()); !errors.Is(err, transport.ErrRefused) {
		t.Fatalf("a new socket with a revoked token: %v, want ErrRefused", err)
	}
}

func TestRevokingATokenClosesItsLiveSockets(t *testing.T) {
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
	if err := Revoke(dir, "laptop"); err != nil {
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
			t.Fatalf("a socket admitted before the revoke still answers: %v", err)
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
	if err := Revoke(dir, "phone"); err == nil {
		t.Fatal("revoked a label that has no token")
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

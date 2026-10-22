package daemon

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"glitched/internal/api"
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
	cfg.Adapter = fake.New(fake.Turn(true))
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
	d, err := Start(context.Background(), Config{Dir: dir, Hub: true, Adapter: fake.New(nil)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Start(context.Background(), Config{Dir: dir, Hub: true, Adapter: fake.New(nil)}); !errors.Is(err, ErrLocked) {
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
		_, err := Start(context.Background(), Config{Dir: dir, Hub: true, Listen: []string{addr}, Adapter: fake.New(nil)})
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

	resp, err := http.Get("http://" + d.Addrs()[0].String() + "/ws")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("an upgrade without a token got %d, want 401", resp.StatusCode)
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

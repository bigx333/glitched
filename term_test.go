package main

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"

	"glitched/internal/client"
	"glitched/internal/daemon"
	"glitched/internal/provider/fake"
)

// The drawer's terminal runs a real shell through libghostty-vt: typed
// commands run, and what they print shows. Skipped where the library
// can't load (offline, before its first download), or where the project
// it opens isn't checked out.
func TestRealTerminal(t *testing.T) {
	if fi, err := os.Stat(expandHome("~/Projects/t3-mygo")); err != nil || !fi.IsDir() {
		t.Skip("~/Projects/t3-mygo not checked out")
	}
	if err := terminal.Load(); err != nil {
		t.Skip("libghostty-vt unavailable:", err)
	}
	if _, err := exec.LookPath("/bin/sh"); err != nil {
		t.Skip("no /bin/sh, as on Windows:", err)
	}
	term, err := terminal.New(terminal.Options{Command: []string{"/bin/sh"}, Dir: projectDir("~/Projects/t3-mygo"), Theme: darkTheme.terminal()})
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	tt := ui.NewTester(func(c *ui.Context) { terminal.View(c, term).Fill().AutoFocus() }, 640, 300)
	tt.Type("echo hello from $(basename $PWD)")
	tt.Key(0, ui.KeyEnter)
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(term.Text(), "hello from t3-mygo") {
		if time.Now().After(deadline) {
			t.Fatalf("no output; terminal shows %q", term.Text())
		}
		time.Sleep(20 * time.Millisecond)
		tt.Frame()
	}
}

func TestProjectDir(t *testing.T) {
	if d := projectDir("~/definitely/not/here"); d == "" || strings.Contains(d, "definitely") {
		t.Errorf("missing project dir %q, want the home directory", d)
	}
}

func TestRemoteThreadShowsTerminalDrawer(t *testing.T) {
	rdir := tempDir(t)
	token, err := daemon.Pair(rdir, "gui")
	if err != nil {
		t.Fatal(err)
	}
	d := engine(t, daemon.Config{Dir: rdir, Listen: []string{"127.0.0.1:0"}}, fake.New(fake.Turn(false)))
	seed(t, d, "/srv/api", "Remote terminal thread", "Hello remote")
	addr := d.Addrs()[0].String()

	h := start(t, tempDir(t))
	var addErr error
	added := false
	h.a.backend.AddServer(client.Server{Label: "remotebox", Addr: addr, Token: token}, func(err error) {
		addErr, added = err, true
	})
	h.until("remote server added", func() bool { return added })
	if addErr != nil {
		t.Fatalf("AddServer: %v", addErr)
	}
	h.until("remotebox online", func() bool {
		return len(h.a.backend.Envs()) > 1 && h.a.backend.Envs()[1].Status == client.Status(client.Online{})
	})

	th := h.thread("Remote terminal thread")
	h.tt.Advance(time.Second)
	h.click(th.View.Title)
	h.tt.Advance(time.Second)
	h.a.showTerminal = true
	h.tt.Frame()
	if !h.tt.HasText("remotebox") || !h.tt.HasText("zsh") {
		t.Fatalf("expected remote terminal drawer showing remotebox and zsh header, got %q", h.tt.Texts())
	}
	_ = context.Background()
}

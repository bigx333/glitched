package codex

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"glitched/internal/api"
	"glitched/internal/provider"
)

// envLaunch is a file each start of the fake codex appends how it was
// started to; with it set, the fake answers --version.
const envLaunch = "GLITCHED_FAKE_CODEX_LAUNCH"

type start struct {
	Exe  string   `json:"exe"`
	Argv []string `json:"argv"`
	Home string   `json:"home"`
}

func noteLaunch(path string) {
	b, _ := json.Marshal(start{Exe: os.Args[0], Argv: os.Args[1:], Home: os.Getenv("CODEX_HOME")})
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	f.Write(append(b, '\n'))
	f.Close()
}

func startsIn(t *testing.T, path string) []start {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []start
	for sc := bufio.NewScanner(f); sc.Scan(); {
		var s start
		json.Unmarshal(sc.Bytes(), &s)
		out = append(out, s)
	}
	return out
}

// placedFake is fake's adapter with its codex at a path with a space, a
// CODEX_HOME with a space and arguments of the user's.
func placedFake(t *testing.T, scenario string) (*Adapter, provider.Launch, string) {
	t.Helper()
	a, _ := fake(t, scenario)
	dir := filepath.Join(t.TempDir(), "my tools")
	home := filepath.Join(t.TempDir(), "codex home")
	for _, d := range []string{dir, home} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	bin := filepath.Join(dir, "my codex")
	if err := os.Symlink(a.Path, bin); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(t.TempDir(), "starts")
	t.Setenv(envLaunch, log)
	l := provider.Launch{Path: bin, Home: home, Args: `-c 'features.image_generation=true' --enable "x y"`}
	a.Place(l)
	return a, l, log
}

// Every app-server the adapter starts, for a turn or a probe, runs the
// program Place names, in its CODEX_HOME, with the user's arguments.
func TestEveryCodexStartsAsItsLaunchSays(t *testing.T) {
	a, l, log := placedFake(t, "hi")
	if _, err := a.Describe(t.Context()); err != nil {
		t.Fatal(err)
	}
	g := open(t, a, spec(t))
	do(t, g, provider.Start{Text: "Hi."})
	turn(t, g)
	closeRun(t, g)
	got := startsIn(t, log)
	if len(got) != 2 {
		t.Fatalf("%d app-servers, want a probe's and a turn's: %+v", len(got), got)
	}
	want := []string{"app-server", "-c", "features.image_generation=true", "--enable", "x y"}
	for _, s := range got {
		if s.Exe != l.Path || s.Home != l.Home || !slices.Equal(s.Argv, want) {
			t.Errorf("codex started as %+v, want %s %q in %s", s, l.Path, want, l.Home)
		}
	}
	prog, err := a.Placement().Program(l)
	if err != nil {
		t.Fatal(err)
	}
	if v, err := a.Version(t.Context(), prog); err != nil || v != "codex-cli 0.200.0" {
		t.Errorf("Version = %q, %v", v, err)
	}
	if got := startsIn(t, log); got[len(got)-1].Home != l.Home || !slices.Equal(got[len(got)-1].Argv, []string{"--version"}) {
		t.Errorf("--version ran as %+v", got[len(got)-1])
	}
	if cmd := a.Command(prog); !slices.Equal(cmd, append([]string{l.Path}, want...)) {
		t.Errorf("Command = %q", cmd)
	}
}

// A thread's parked app-server started before a change of launch isn't
// the one its next run takes: that one starts as the launch says now.
func TestARunAfterPlaceGetsANewAppServer(t *testing.T) {
	a, l, log := placedFake(t, "hi")
	s := spec(t)
	g := open(t, a, s)
	do(t, g, provider.Start{Text: "Hi."})
	turn(t, g)
	closeRun(t, g)
	l.Args = "--enable other"
	a.Place(l)
	s.Run, s.Generation = api.NewID(), 2
	g = open(t, a, s)
	closeRun(t, g)
	got := startsIn(t, log)
	if len(got) != 2 || !slices.Contains(got[1].Argv, "other") {
		t.Errorf("starts %+v, want a second app-server with the new arguments", got)
	}
}

func TestTheUsersArgumentsCantSetCodexsAccess(t *testing.T) {
	a := &Adapter{}
	self, _ := os.Executable()
	for _, args := range []string{"-c sandbox_mode=danger-full-access", "--config approval_policy=never", "-c=sandbox_workspace_write.network_access=true", "-c mcp_servers.glitched.command=x", "--print-startup-config"} {
		_, err := a.Placement().Program(provider.Launch{Path: self, Args: args})
		var bad *provider.LaunchError
		if !errors.As(err, &bad) || !strings.Contains(bad.Args, "set by Glitched itself") {
			t.Errorf("args %q: %v, want them refused", args, err)
		}
	}
}

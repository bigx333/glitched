package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"glitched/internal/provider"
)

// envLaunch is a file each start of the fake agent appends how it was
// started to; with it set, the fake answers --version.
const envLaunch = "GLITCHED_FAKE_AGENT_LAUNCH"

type start struct {
	Exe  string   `json:"exe"`
	Argv []string `json:"argv"`
}

func noteLaunch(path string) {
	b, _ := json.Marshal(start{Exe: os.Args[0], Argv: os.Args[1:]})
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

// Every agent the adapter starts, for a turn or a probe, runs the program
// Place names, with the user's arguments after the profile's and before
// the access level's.
func TestEveryAgentStartsAsItsLaunchSays(t *testing.T) {
	a, _ := fake(t, "hi")
	dir := filepath.Join(t.TempDir(), "my tools")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "my agy")
	if err := os.Symlink(a.Path, bin); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(t.TempDir(), "starts")
	t.Setenv(envLaunch, log)
	l := provider.Launch{Path: bin, Args: `--add-dir "/tmp/a b"`}
	a.Place(l)
	if _, err := a.Describe(context.Background()); err != nil {
		t.Fatal(err)
	}
	s := spec(t)
	g := open(t, a, s)
	do(t, g, provider.Start{Text: "Hi."})
	turn(t, g)
	closeRun(t, g)
	got := startsIn(t, log)
	if len(got) != 2 {
		t.Fatalf("%d agents, want a probe's and a turn's: %+v", len(got), got)
	}
	want := slices.Concat(Antigravity.Args, []string{"--add-dir", "/tmp/a b"}, Antigravity.Access[s.Access].Args)
	for _, st := range got {
		if st.Exe != bin || !slices.Equal(st.Argv, want) {
			t.Errorf("agent started as %+v, want %s %q", st, bin, want)
		}
	}
	prog, err := a.Placement().Program(l)
	if err != nil {
		t.Fatal(err)
	}
	if v, err := a.Version(context.Background(), prog); err != nil || v != "1.5.14" {
		t.Errorf("Version = %q, %v", v, err)
	}
}

// agy names no variable for a folder of its own, so a Home stops it
// starting rather than being dropped, and its access flags aren't the
// user's to pass.
func TestAnAgentTakesNoHomeAndNoAccessFlags(t *testing.T) {
	a, _ := fake(t, "hi")
	a.Place(provider.Launch{Path: a.Path, Home: t.TempDir()})
	if _, err := a.Open(context.Background(), spec(t)); !errors.Is(err, provider.ErrNotInstalled) || !strings.Contains(err.Error(), "no folder of its own") {
		t.Errorf("open with a Home: %v", err)
	}
	for _, args := range []string{"--dangerously-skip-permissions", "--mode=auto", "--login"} {
		_, err := a.Placement().Program(provider.Launch{Path: a.Path, Args: args})
		var bad *provider.LaunchError
		if !errors.As(err, &bad) || !strings.Contains(bad.Args, "set by Glitched itself") {
			t.Errorf("args %q: %v, want them refused", args, err)
		}
	}
}

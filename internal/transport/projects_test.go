package transport

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/projects"
)

// machine is Projects as an engine serves them, on folders in a test's
// own, with a git that runs script.
type machine struct {
	hide  []string
	bin   string
	calls atomic.Int32
}

func (m *machine) Folders(ctx context.Context, q api.FolderQuery) (api.FolderList, error) {
	m.calls.Add(1)
	return projects.List(ctx, q.Path, m.hide)
}

func (m *machine) Clone(ctx context.Context, req api.CloneRequest, progress func(string)) (api.Cloned, error) {
	m.calls.Add(1)
	return projects.CloneInto(ctx, req, m.hide, m.bin, progress)
}

func timeout(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// gitRunning has the machine's git run script, as sh, in place of git.
func gitRunning(t *testing.T, script string) *machine {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake git is a shell script")
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return &machine{bin: bin}
}

func TestProjectsOverASocket(t *testing.T) {
	m := gitRunning(t, "echo \"Cloning into '$5'...\" >&2\necho 'Receiving objects: 100% (3/3)' >&2")
	c := NewEndpointClient(start(t, "unix", &Server{Projects: m}), api.ID{})
	defer c.Close()
	ctx := timeout(t)
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "glitched", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	list, err := c.Folders(ctx, api.FolderQuery{Path: dir})
	if err != nil || list.Path != dir || !slices.Equal(list.Folders, []api.Folder{{Name: "glitched", Repo: true}}) {
		t.Fatalf("listed %+v, %v; want glitched, a repository, in %s", list, err, dir)
	}
	dest := filepath.Join(dir, "lighthouse")
	var lines []string
	cloned, err := c.Clone(ctx, api.CloneRequest{Source: "https://example.com/lighthouse.git", Dest: dest}, func(l string) { lines = append(lines, l) })
	if err != nil || cloned.Path != dest {
		t.Fatalf("cloned into %q, %v; want %q", cloned.Path, err, dest)
	}
	if !slices.Equal(lines, []string{"Cloning into '" + dest + "'...", "Receiving objects: 100% (3/3)"}) {
		t.Fatalf("the clone said %q", lines)
	}
}

// A client that gives up on a clone stops it on the engine, which takes
// away the folder it made.
func TestCancellingACloneStopsItAndTakesItsFolder(t *testing.T) {
	m := gitRunning(t, "echo 'Receiving objects:  10% (1/10)' >&2\nexec /bin/sleep 60")
	c := NewEndpointClient(start(t, "unix", &Server{Projects: m}), api.ID{})
	defer c.Close()
	dest := filepath.Join(t.TempDir(), "lighthouse")
	ctx, cancel := context.WithCancel(timeout(t))
	progressed := make(chan struct{}, 1)
	ended := make(chan error, 1)
	go func() {
		_, err := c.Clone(ctx, api.CloneRequest{Source: "https://example.com/lighthouse.git", Dest: dest}, func(string) {
			select {
			case progressed <- struct{}{}:
			default:
			}
		})
		ended <- err
	}()
	select {
	case <-progressed:
	case err := <-ended:
		t.Fatalf("the clone ended before it said anything: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("the clone said nothing")
	}
	if _, err := os.Stat(dest); err != nil {
		t.Fatalf("the clone made no folder: %v", err)
	}
	cancel()
	if err := <-ended; !errors.Is(err, context.Canceled) {
		t.Fatalf("the cancelled clone ended with %v, want context.Canceled", err)
	}
	for began := time.Now(); ; time.Sleep(10 * time.Millisecond) {
		if _, err := os.Stat(dest); errors.Is(err, os.ErrNotExist) {
			break
		}
		if time.Since(began) > 10*time.Second {
			t.Fatal("the cancelled clone's folder stays")
		}
	}
}

// Over the tailnet, a client with a wrong token is refused before it can
// list or clone anything.
func TestProjectsRefuseAWrongToken(t *testing.T) {
	m := gitRunning(t, "exit 0")
	ctx := timeout(t)
	addr := serveTailnet(t, &Server{Transport: Tailnet{}, Projects: m, Authorize: admit(map[string]context.Context{"good": ctx})})
	bad := NewEndpointClient(Tailnet{Address: addr, Token: "bad"}, api.ID{})
	defer bad.Close()
	if list, err := bad.Folders(ctx, api.FolderQuery{Path: t.TempDir()}); !errors.Is(err, ErrRefused) {
		t.Fatalf("a wrong token listed %+v, %v; want ErrRefused", list, err)
	}
	dest := filepath.Join(t.TempDir(), "lighthouse")
	if _, err := bad.Clone(ctx, api.CloneRequest{Source: "o/lighthouse", Dest: dest}, func(string) {}); !errors.Is(err, ErrRefused) {
		t.Fatalf("a wrong token's clone: %v, want ErrRefused", err)
	}
	if n := m.calls.Load(); n != 0 {
		t.Fatalf("a wrong token reached Projects %d times", n)
	}
	good := NewEndpointClient(Tailnet{Address: addr, Token: "good"}, api.ID{})
	defer good.Close()
	if _, err := good.Folders(ctx, api.FolderQuery{Path: t.TempDir()}); err != nil {
		t.Fatalf("the right token's listing: %v", err)
	}
}

// An engine that serves no Projects, as one older than them, answers
// that it doesn't serve the call.
func TestProjectsOfAnEngineWithoutThem(t *testing.T) {
	c := NewEndpointClient(start(t, "unix", &Server{Endpoint: &fakeEndpoint{}}), api.ID{})
	defer c.Close()
	ctx := timeout(t)
	if _, err := c.Folders(ctx, api.FolderQuery{}); !errors.Is(err, ErrUnknownMethod) {
		t.Fatalf("Folders: %v, want ErrUnknownMethod", err)
	}
	if _, err := c.Clone(ctx, api.CloneRequest{Source: "o/n", Dest: "~/n"}, func(string) {}); !errors.Is(err, ErrUnknownMethod) {
		t.Fatalf("Clone: %v, want ErrUnknownMethod", err)
	}
}

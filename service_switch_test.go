package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"glitched/internal/api"
	"glitched/internal/daemon"
	"glitched/internal/provider/fake"
	"glitched/internal/service"
	"glitched/internal/transport"
)

// serviceStub puts a fake launchctl and systemctl first on PATH, under a
// home of the test's, so the Background service switch never reaches the
// machine's own service manager. It returns what they were called with.
// While the file $SERVICE_STUB_HOLD names exists, they hold their answer
// to whether the service runs.
func serviceStub(t *testing.T) (home string, calls func() string) {
	t.Helper()
	bin := t.TempDir()
	home = t.TempDir()
	script := `#!/bin/sh
echo "${0##*/} $*" >> "` + bin + `/log"
case "$*" in
*show*|print*) while [ -n "$SERVICE_STUB_HOLD" ] && [ -e "$SERVICE_STUB_HOLD" ]; do sleep 0.05; done ;;
esac
case "$*" in
*show*) echo ActiveState=active; echo MainPID=42 ;;
print*) echo "	state = running"; echo "	pid = 42" ;;
esac
`
	for _, name := range []string{"launchctl", "systemctl"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return home, func() string {
		data, _ := os.ReadFile(filepath.Join(bin, "log"))
		os.Remove(filepath.Join(bin, "log"))
		return string(data)
	}
}

// installService installs the service for dir, as another app or the CLI
// would.
func installService(t *testing.T, dir string) string {
	t.Helper()
	st, err := service.Install(context.Background(), service.Options{Dir: dir, Binary: "/bin/glitched", PATH: "/usr/bin:/bin"})
	if err != nil {
		t.Fatal(err)
	}
	return st.UnitPath
}

// hold has the stub hold its answer until the returned func lets it go.
func hold(t *testing.T) (release func()) {
	t.Helper()
	gate := filepath.Join(t.TempDir(), "hold")
	if err := os.WriteFile(gate, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SERVICE_STUB_HOLD", gate)
	release = func() { os.Remove(gate) }
	t.Cleanup(release)
	return release
}

// The service installed for another state directory, such as the main
// app's seen from the dev GUI, is that directory's: the switch reads off,
// says whose it is, and neither stops nor takes it over.
func TestTheBackgroundServiceSwitchLeavesAnotherDirectorysServiceBe(t *testing.T) {
	home, calls := serviceStub(t)
	prod := filepath.Join(home, ".glitched")
	unit := installService(t, prod)
	before, _ := os.ReadFile(unit)
	calls()

	h := start(t, tempDir(t))
	ticking(t, h)
	h.click("Settings")
	h.click("This machine")
	h.reveal("Enable background service")
	h.until("the service read as another directory's", func() bool {
		return h.tt.HasText("Installed for another state directory, " + prod + ". Manage it from there.")
	})
	h.click("Enable background service")
	h.tt.Frame()
	if h.a.svcDoing != svcIdle {
		t.Errorf("the switch acts on another directory's service: %v", h.a.svcDoing)
	}
	if after, _ := os.ReadFile(unit); string(after) != string(before) {
		t.Errorf("the switch rewrote %s to\n%s", unit, after)
	}
	if log := calls(); strings.Contains(log, "disable") || strings.Contains(log, "bootout") || strings.Contains(log, "enable") {
		t.Errorf("the switch called %q on another directory's service", log)
	}
}

// Till the first read says whether the service is installed, the switch
// says it checks and does nothing, rather than reading off.
func TestTheBackgroundServiceSwitchWaitsForTheFirstRead(t *testing.T) {
	_, calls := serviceStub(t)
	dir := tempDir(t)
	installService(t, dir)
	release := hold(t)
	calls()

	h := start(t, dir)
	ticking(t, h)
	h.click("Settings")
	h.click("This machine")
	h.reveal("Enable background service")
	if !h.tt.HasText("Checking the background service…") {
		t.Errorf("before the first read the row shows %q, want it checking", h.tt.Texts())
	}
	h.click("Enable background service")
	h.tt.Frame()
	if h.a.svcDoing != svcIdle {
		t.Errorf("the switch acted before the first read: %v", h.a.svcDoing)
	}
	release()
	h.until("the first read", func() bool { return h.tt.HasText("Installed and running (PID 42).") })
	if log := calls(); strings.Contains(log, "disable") || strings.Contains(log, "bootout") {
		t.Errorf("the switch called %q before the first read", log)
	}
}

// While the switch works, the row says what it does, whatever a read
// that lands meanwhile says.
func TestTheBackgroundServiceSwitchSaysWhatItDoes(t *testing.T) {
	_, _ = serviceStub(t)
	h := start(t, tempDir(t))
	ticking(t, h)
	h.click("Settings")
	h.click("This machine")
	h.reveal("Enable background service")
	h.until("the first read", func() bool { return !h.a.svcRead.IsZero() && !h.a.svcLoading })
	release := hold(t)
	h.click("Enable background service")
	h.until("the switch at work", func() bool { return h.tt.HasText("Installing background service…") })
	h.a.svcStatus.Installed = true // as a read landing mid-install would have it
	h.tt.Frame()
	if !h.tt.HasText("Installing background service…") {
		t.Errorf("mid-install the row shows %q, want it installing", h.tt.Texts())
	}
	release()
	h.until("the install", func() bool { return h.a.svcDoing == svcIdle && h.tt.HasText("Installed and running (PID 42).") })
}

// Turning the background service on while the engine runs inside the app
// hands the state directory over: the app lets its engine go, the service's
// engine, which waits for the lock, takes it, and the app attaches to it,
// so quitting the app leaves the engine running.
func TestTheBackgroundServiceTakesOverTheAppsEngine(t *testing.T) {
	_, calls := serviceStub(t)
	dir := tempDir(t)
	h := start(t, dir)
	ticking(t, h)
	h.click("Settings")
	h.click("This machine")
	h.until("the engine embedded here", func() bool { return h.a.backend.Machine().Embedded && h.tt.HasText("Online") })

	// What the service manager starts as the switch turns on: the service's
	// glitched serve -wait-lock.
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan *daemon.Daemon, 1)
	go func() {
		d, err := daemon.StartWhenFree(ctx, func() (daemon.Config, error) {
			return daemon.Config{Dir: dir, Hub: true, Providers: daemon.FakeProviders(fake.New(nil))}, nil
		})
		if err != nil {
			served <- nil
			return
		}
		served <- d
	}()
	var svc *daemon.Daemon
	t.Cleanup(func() {
		cancel()
		if svc == nil {
			svc = <-served
		}
		if svc != nil {
			svc.Close()
		}
	})

	h.reveal("Enable background service")
	h.click("Enable background service")
	h.until("the app attached to the service's engine", func() bool {
		return !h.a.backend.Machine().Embedded && h.tt.HasText("Online") && h.tt.HasText(h.local().Name+" runs in another process, such as glitched serve, and this app attached to it.")
	})
	if log := calls(); !strings.Contains(log, "enable glitched.service") && !strings.Contains(log, "bootstrap") {
		t.Errorf("the switch called %q, want the service enabled", log)
	}
	select {
	case svc = <-served:
	default:
		t.Fatal("the app attached to an engine other than the service's")
	}
	if svc == nil {
		t.Fatal("the service's engine failed to start")
	}

	h.a.backend.Close()
	ep := transport.NewEndpointClient(daemon.Socket(dir), api.ID{})
	defer ep.Close()
	if _, err := ep.Info(context.Background()); err != nil {
		t.Errorf("with the app closed, no engine answers on %s: %v", dir, err)
	}
}

// The switch stops or swaps the engine, so it waits while an agent runs,
// as Restart does.
func TestTheBackgroundServiceSwitchWaitsForRunningAgents(t *testing.T) {
	_, calls := serviceStub(t)
	dir := tempDir(t)
	unit := installService(t, dir)
	calls()

	d := engine(t, daemon.Config{Dir: dir}, fake.New(fake.Turn(true)))
	h := start(t, dir)
	ticking(t, h)
	h.click("Settings")
	h.click("This machine")
	h.until("the service read as on", func() bool { return h.a.svcStatus.Installed && h.tt.HasText("runs in another process") })

	th := api.NewID()
	for _, c := range []api.Command{
		{ID: api.NewID(), Target: th, Body: api.ThreadCreate{Workspace: "/work/t3code", Title: "Busy", Model: fakeModel}},
		{ID: api.NewID(), Target: th, Body: api.MessageDispatch{Text: "go", Mode: api.StartNow}},
	} {
		if _, err := d.Endpoint().Submit(context.Background(), c); err != nil {
			t.Fatal(err)
		}
	}
	h.until("the agent at work", func() bool { return h.a.localRuns() > 0 })
	h.tt.Frame()
	h.reveal("Enable background service")
	h.click("Enable background service")
	h.until("the switch to settle", func() bool { return h.a.svcDoing == svcIdle })
	if log := calls(); strings.Contains(log, "disable") || strings.Contains(log, "bootout") {
		t.Errorf("the switch stopped the service under a running agent: %q", log)
	}
	if _, err := os.Stat(unit); err != nil {
		t.Errorf("the switch removed the service under a running agent: %v", err)
	}
}

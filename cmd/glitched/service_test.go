package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"glitched/internal/service"
)

func TestServiceCLIStatusAndUsage(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	dir := filepath.Join(home, "state")

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldOut := os.Stdout
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = oldOut })

	if err := serviceCmd(context.Background(), []string{"status", "-json", "-dir", dir}); err != nil {
		w.Close()
		t.Fatal(err)
	}
	w.Close()
	os.Stdout = oldOut

	var st service.Status
	if err := json.NewDecoder(r).Decode(&st); err != nil {
		t.Fatal(err)
	}
	if st.Installed || st.Running {
		t.Errorf("unexpected status for fresh home: %+v", st)
	}

	if err := serviceCmd(context.Background(), nil); !errors.As(err, new(usageError)) {
		t.Errorf("no args error = %v, want usageError", err)
	}
	if err := serviceCmd(context.Background(), []string{"bogus"}); !errors.As(err, new(usageError)) {
		t.Errorf("bad action error = %v, want usageError", err)
	} else if !strings.Contains(err.Error(), "bogus") {
		t.Errorf("error = %v, want action name", err)
	}
}

// cliStub puts a fake launchctl and systemctl first on PATH under a home of
// the test's. report is what systemctl show and launchctl print answer.
func cliStub(t *testing.T, report string) (home string, calls func() string) {
	t.Helper()
	bin := t.TempDir()
	home = t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "report"), []byte(report), 0o644); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\necho \"${0##*/} $*\" >> \"" + bin + "/log\"\ncase \"$*\" in\n*show*|print*) cat \"" + bin + "/report\" ;;\nesac\n"
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

// svc runs the service command and returns what it printed.
func svc(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var err error
	out := stdout(t, func() { err = serviceCmd(context.Background(), args) })
	return out, err
}

// glitched service, run for one state directory, reports a service
// installed for another as that one's and refuses to stop or replace it.
func TestServiceCLILeavesAnotherDirectorysServiceBe(t *testing.T) {
	if !service.Inspect(context.Background(), service.Options{}).Supported {
		t.Skip("no background service on this OS")
	}
	home, calls := cliStub(t, "ActiveState=active\nMainPID=42\n\tstate = running\n\tpid = 42\n")
	prod, dev := filepath.Join(home, ".glitched"), filepath.Join(home, ".glitched-dev")
	if _, err := svc(t, "install", "-dir", prod, "-binary", "/bin/true"); err != nil {
		t.Fatal(err)
	}
	unit := service.Inspect(context.Background(), service.Options{Dir: prod}).UnitPath
	before, _ := os.ReadFile(unit)
	calls()

	out, err := svc(t, "status", "-dir", dev)
	if err != nil || !strings.Contains(out, "for another directory, "+prod) {
		t.Errorf("status from %s printed %q (%v), want it installed for %s", dev, out, err, prod)
	}
	for _, action := range []string{"uninstall", "install"} {
		if _, err := svc(t, action, "-dir", dev, "-binary", "/bin/true"); err == nil || !strings.Contains(err.Error(), prod) {
			t.Errorf("%s from %s = %v, want a refusal naming %s", action, dev, err, prod)
		}
	}
	if after, _ := os.ReadFile(unit); string(after) != string(before) {
		t.Errorf("the definition changed to\n%s", after)
	}
	if log := calls(); strings.Contains(log, "disable") || strings.Contains(log, "bootout") || strings.Contains(log, "restart") {
		t.Errorf("the other directory called %q", log)
	}
}

// A service its manager keeps restarting reads as restarting, with why it
// last stopped, not as running.
func TestServiceCLIStatusShowsACrashLoop(t *testing.T) {
	if !service.Inspect(context.Background(), service.Options{}).Supported {
		t.Skip("no background service on this OS")
	}
	home, _ := cliStub(t, "ActiveState=activating\nSubState=auto-restart\nResult=exit-code\nExecMainStatus=1\nMainPID=0\n\tstate = spawn scheduled\n\tlast exit code = 1\n")
	dir := filepath.Join(home, ".glitched")
	if _, err := svc(t, "install", "-dir", dir, "-binary", "/bin/true"); err != nil {
		t.Fatal(err)
	}
	out, err := svc(t, "status", "-dir", dir)
	if err != nil || !strings.Contains(out, "restarting") || strings.Contains(out, "running") || !strings.Contains(out, "1") {
		t.Errorf("status printed %q (%v), want it restarting after exiting 1", out, err)
	}
}

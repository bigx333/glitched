package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stub is a fake launchctl and systemctl on PATH. Each logs its arguments,
// keeps whether the manager loaded the job and whether it runs, and refuses
// the calls that match the shell pattern fail.
type stub struct {
	t    *testing.T
	dir  string
	home string
	m    manager
}

const stubScript = `#!/bin/sh
me=${0##*/}
echo "$me $*" >> "$STUB_DIR/log"
case "$me $*" in
$(cat "$STUB_DIR/fail" 2>/dev/null)) echo "refused by the stub" >&2; exit 1 ;;
esac
case "$me $*" in
"launchctl print"*) [ -e "$STUB_DIR/loaded" ] || exit 113; echo "	state = running"; echo "	pid = 42" ;;
"launchctl bootstrap"*) : > "$STUB_DIR/loaded" ;;
"launchctl bootout"*) [ -e "$STUB_DIR/loaded" ] || exit 3; rm -f "$STUB_DIR/loaded" ;;
"systemctl --user start"*|"systemctl --user restart"*) : > "$STUB_DIR/active" ;;
"systemctl --user disable --now"*) rm -f "$STUB_DIR/active" ;;
"systemctl --user show"*) if [ -e "$STUB_DIR/active" ]; then echo ActiveState=active; echo MainPID=42; else echo ActiveState=inactive; echo MainPID=0; fi ;;
esac
`

func newStub(t *testing.T, m manager) *stub {
	t.Helper()
	s := &stub{t: t, dir: t.TempDir(), home: t.TempDir(), m: m}
	for _, name := range []string{"launchctl", "systemctl"} {
		if err := os.WriteFile(filepath.Join(s.dir, name), []byte(stubScript), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("STUB_DIR", s.dir)
	t.Setenv("PATH", s.dir+":/usr/bin:/bin")
	t.Setenv("XDG_CONFIG_HOME", "")
	was := osManager
	osManager = m
	t.Cleanup(func() { osManager = was })
	return s
}

// fail has the managers refuse the calls the shell pattern matches.
func (s *stub) fail(pattern string) {
	s.t.Helper()
	if err := os.WriteFile(filepath.Join(s.dir, "fail"), []byte(pattern), 0o644); err != nil {
		s.t.Fatal(err)
	}
}

// calls returns the calls since the last, and forgets them.
func (s *stub) calls() []string {
	s.t.Helper()
	path := filepath.Join(s.dir, "log")
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		s.t.Fatal(err)
	}
	os.Remove(path)
	return strings.Fields(strings.ReplaceAll(strings.TrimSpace(string(data)), " ", "_"))
}

func (s *stub) opts(dir string) Options {
	return Options{Binary: "/opt/glitched/bin/glitched", Dir: filepath.Join(s.home, dir), PATH: "/usr/bin:/bin", Home: s.home}
}

func (s *stub) definition() (string, bool) {
	data, err := os.ReadFile(s.m.path(s.home))
	return string(data), err == nil
}

var managers = []struct {
	name    string
	m       manager
	start   string // the call that starts the job once its definition is new
	restart string // the call that stops a running job to apply a changed definition
	apply   string // the call that runs a changed definition
	stop    string // the call that stops the job and drops it
}{
	{"launchd", launchd{}, "launchctl bootstrap*", "launchctl bootout*", "launchctl bootstrap*", "launchctl bootout*"},
	{"systemd", systemd{}, "systemctl --user start*", "systemctl --user restart*", "systemctl --user restart*", "systemctl --user disable*"},
}

func hasCall(calls []string, pattern string) bool {
	prefix := strings.ReplaceAll(strings.TrimSuffix(pattern, "*"), " ", "_")
	for _, c := range calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

// A manager that refuses to start the service leaves nothing installed, so
// Settings' switch reads off as it was.
func TestInstallRefusedLeavesNothingInstalled(t *testing.T) {
	for _, tc := range managers {
		t.Run(tc.name, func(t *testing.T) {
			s := newStub(t, tc.m)
			s.fail(tc.start)
			st, err := Install(context.Background(), s.opts("state"))
			if err == nil || !strings.Contains(err.Error(), "refused by the stub") {
				t.Fatalf("Install = %v, want the manager's refusal", err)
			}
			if def, ok := s.definition(); ok {
				t.Errorf("the refused install left its definition on disk:\n%s", def)
			}
			if st.Installed || Inspect(context.Background(), Options{Home: s.home}).Installed {
				t.Errorf("the refused install reads as installed: %+v", st)
			}
		})
	}
}

// A changed definition the manager refuses to run gives way to the one
// installed before, which runs again.
func TestReinstallRefusedPutsBackTheOldDefinition(t *testing.T) {
	for _, tc := range managers {
		t.Run(tc.name, func(t *testing.T) {
			s := newStub(t, tc.m)
			if _, err := Install(context.Background(), s.opts("one")); err != nil {
				t.Fatal(err)
			}
			old, _ := s.definition()
			s.calls()
			s.fail(tc.apply)
			if _, err := Install(context.Background(), s.opts("two")); err == nil {
				t.Fatal("Install succeeded, want the manager's refusal")
			}
			if def, _ := s.definition(); def != old {
				t.Errorf("after the refused reinstall the definition is\n%s\nwant the old one\n%s", def, old)
			}
		})
	}
}

// A manager that refuses to stop the service keeps its definition, and
// Uninstall says so.
func TestUninstallRefusedKeepsTheDefinition(t *testing.T) {
	for _, tc := range managers {
		t.Run(tc.name, func(t *testing.T) {
			s := newStub(t, tc.m)
			if _, err := Install(context.Background(), s.opts("state")); err != nil {
				t.Fatal(err)
			}
			s.fail(tc.stop)
			st, err := Uninstall(context.Background(), Options{Home: s.home})
			if err == nil || !strings.Contains(err.Error(), "refused by the stub") {
				t.Fatalf("Uninstall = %v, want the manager's refusal", err)
			}
			if _, ok := s.definition(); !ok || !st.Installed {
				t.Errorf("the refused uninstall removed the definition: %+v", st)
			}
		})
	}
}

// Uninstall stops the service and removes its definition.
func TestUninstallRemovesTheDefinition(t *testing.T) {
	for _, tc := range managers {
		t.Run(tc.name, func(t *testing.T) {
			s := newStub(t, tc.m)
			if _, err := Install(context.Background(), s.opts("state")); err != nil {
				t.Fatal(err)
			}
			s.calls()
			st, err := Uninstall(context.Background(), Options{Home: s.home})
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := s.definition(); ok || st.Installed || st.Running {
				t.Errorf("after Uninstall: %+v", st)
			}
			if calls := s.calls(); !hasCall(calls, tc.stop) {
				t.Errorf("Uninstall called %q, want %s", calls, tc.stop)
			}
		})
	}
}

// Installing what is installed already leaves its engine running, and a
// changed definition restarts it to apply the change.
func TestReinstallRestartsOnlyAChangedService(t *testing.T) {
	for _, tc := range managers {
		t.Run(tc.name, func(t *testing.T) {
			s := newStub(t, tc.m)
			if _, err := Install(context.Background(), s.opts("one")); err != nil {
				t.Fatal(err)
			}
			s.calls()
			st, err := Install(context.Background(), s.opts("one"))
			if err != nil {
				t.Fatal(err)
			}
			if calls := s.calls(); hasCall(calls, tc.restart) {
				t.Errorf("an unchanged reinstall called %q, which stops the running engine", calls)
			}
			if !st.Running {
				t.Errorf("after an unchanged reinstall: %+v, want running", st)
			}
			st, err = Install(context.Background(), s.opts("two"))
			if err != nil {
				t.Fatal(err)
			}
			if calls := s.calls(); !hasCall(calls, tc.restart) || !hasCall(calls, tc.apply) {
				t.Errorf("a reinstall with another -dir called %q, want the engine restarted", calls)
			}
			if want := filepath.Join(s.home, "two"); st.Dir != want || !st.Running {
				t.Errorf("after the changed reinstall: %+v, want running on %s", st, want)
			}
		})
	}
}

// Status reports what the installed definition runs, not what its caller
// would install.
func TestStatusReadsTheInstalledDefinition(t *testing.T) {
	for _, tc := range managers {
		t.Run(tc.name, func(t *testing.T) {
			s := newStub(t, tc.m)
			o := s.opts("100% state")
			if _, err := Install(context.Background(), o); err != nil {
				t.Fatal(err)
			}
			st := Inspect(context.Background(), Options{Home: s.home, Binary: "/elsewhere/glitched", Dir: "/elsewhere"})
			if st.Binary != o.Binary || st.Dir != o.Dir || !st.Installed || st.PID != 42 {
				t.Errorf("status %+v, want the installed %s on %s", st, o.Binary, o.Dir)
			}
		})
	}
}

// The systemd unit goes where systemd looks for it, which XDG_CONFIG_HOME
// moves.
func TestUnitFollowsXDGConfigHome(t *testing.T) {
	s := newStub(t, systemd{})
	config := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", config)
	st, err := Install(context.Background(), s.opts("state"))
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(config, "systemd", "user", unitName); st.UnitPath != want {
		t.Errorf("unit at %s, want %s", st.UnitPath, want)
	}
	if _, err := os.Stat(st.UnitPath); err != nil {
		t.Error(err)
	}
}

// What the login shell's profile prints stays out of the captured PATH.
func TestCapturePATHIgnoresTheProfilesOutput(t *testing.T) {
	shell := filepath.Join(t.TempDir(), "sh")
	script := "#!/bin/sh\necho 'Welcome back!'\nPATH=/from/profile:/usr/bin\nexport PATH\nexec /bin/sh -c \"$2\"\n"
	if err := os.WriteFile(shell, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SHELL", shell)
	if got := CapturePATH(); got != "/from/profile:/usr/bin" {
		t.Errorf("CapturePATH = %q, want the profile's PATH alone", got)
	}
}

// scripts/glitched installs the same launchd job Settings does, so either
// sees and replaces the other's instead of running a second engine.
func TestScriptUsesTheServiceLabel(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "scripts", "glitched"))
	if err != nil {
		t.Fatal(err)
	}
	if want := `LAUNCHD_LABEL="` + Label + `"`; !strings.Contains(string(data), want) {
		t.Errorf("scripts/glitched lacks %s", want)
	}
}

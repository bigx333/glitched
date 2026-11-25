package service

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
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
$(cat "$STUB_DIR/hang" 2>/dev/null)) exec sleep 10 ;;
esac
case "$me $*" in
"launchctl print"*) [ -e "$STUB_DIR/loaded" ] || { echo "Could not find service \"$2\" in domain"; exit 113; }; if [ -e "$STUB_DIR/print" ]; then cat "$STUB_DIR/print"; else echo "	state = running"; echo "	pid = 42"; fi ;;
"launchctl bootstrap"*) : > "$STUB_DIR/loaded" ;;
"launchctl bootout"*) [ -e "$STUB_DIR/loaded" ] || exit 3; rm -f "$STUB_DIR/loaded" ;;
"systemctl --user start"*|"systemctl --user restart"*) : > "$STUB_DIR/active" ;;
"systemctl --user enable"*) : > "$STUB_DIR/enabled" ;;
"systemctl --user disable --now"*) rm -f "$STUB_DIR/active" "$STUB_DIR/enabled" ;;
"systemctl --user show"*) if [ -e "$STUB_DIR/show" ]; then cat "$STUB_DIR/show"; elif [ -e "$STUB_DIR/active" ]; then echo ActiveState=active; echo MainPID=42; else echo ActiveState=inactive; echo MainPID=0; fi ;;
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
func (s *stub) fail(pattern string) { s.set("fail", pattern) }

// hang has the managers never answer the calls the shell pattern matches.
func (s *stub) hang(pattern string) { s.set("hang", pattern) }

func (s *stub) set(name, content string) {
	s.t.Helper()
	if err := os.WriteFile(filepath.Join(s.dir, name), []byte(content), 0o644); err != nil {
		s.t.Fatal(err)
	}
}

func (s *stub) has(name string) bool {
	_, err := os.Stat(filepath.Join(s.dir, name))
	return err == nil
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

// report has the manager describe the job with out, as launchctl print or
// systemctl show would.
func (s *stub) report(out string) {
	s.t.Helper()
	name := "print"
	if _, ok := s.m.(systemd); ok {
		name = "show"
	}
	s.set(name, out)
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
			two := s.opts("one")
			two.Binary = "/opt/glitched/next/glitched"
			if _, err := Install(context.Background(), two); err == nil {
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
			st, err := Uninstall(context.Background(), s.opts("state"))
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
			st, err := Uninstall(context.Background(), s.opts("state"))
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
			next := s.opts("one")
			next.Binary = "/opt/glitched/next/glitched"
			st, err = Install(context.Background(), next)
			if err != nil {
				t.Fatal(err)
			}
			if calls := s.calls(); !hasCall(calls, tc.restart) || !hasCall(calls, tc.apply) {
				t.Errorf("a reinstall with another binary called %q, want the engine restarted", calls)
			}
			if st.Binary != next.Binary || !st.Running {
				t.Errorf("after the changed reinstall: %+v, want %s running", st, next.Binary)
			}
		})
	}
}

// One service serves one state directory. Another directory's calls,
// such as the dev GUI's, see it as that directory's and leave it be:
// they neither stop it nor point it at themselves.
func TestAServiceForAnotherDirectoryIsLeftBe(t *testing.T) {
	for _, tc := range managers {
		t.Run(tc.name, func(t *testing.T) {
			s := newStub(t, tc.m)
			prod := s.opts("prod")
			if _, err := Install(context.Background(), prod); err != nil {
				t.Fatal(err)
			}
			def, _ := s.definition()
			s.calls()
			dev := s.opts("dev")

			st := Inspect(context.Background(), dev)
			if !st.Installed || !st.Foreign || st.Dir != prod.Dir {
				t.Errorf("status from the other directory %+v, want installed for %s and foreign", st, prod.Dir)
			}
			if ours := Inspect(context.Background(), prod); ours.Foreign {
				t.Errorf("status from its own directory reads foreign: %+v", ours)
			}

			if _, err := Install(context.Background(), dev); err == nil || !strings.Contains(err.Error(), prod.Dir) {
				t.Errorf("Install from the other directory = %v, want a refusal naming %s", err, prod.Dir)
			}
			if _, err := Uninstall(context.Background(), dev); err == nil || !strings.Contains(err.Error(), prod.Dir) {
				t.Errorf("Uninstall from the other directory = %v, want a refusal naming %s", err, prod.Dir)
			}
			if now, _ := s.definition(); now != def {
				t.Errorf("the other directory rewrote the definition to\n%s", now)
			}
			if calls := s.calls(); hasCall(calls, tc.stop) || hasCall(calls, tc.restart) || hasCall(calls, tc.start) {
				t.Errorf("the other directory called %q on the service", calls)
			}
		})
	}
}

// A job the manager keeps restarting, as after a crash, is not running.
// Status says it restarts, and why it last stopped.
func TestStatusReportsACrashLoopAsRestarting(t *testing.T) {
	for _, tc := range []struct {
		name, out string
		m         manager
	}{
		{"systemd", "ActiveState=activating\nSubState=auto-restart\nResult=exit-code\nExecMainStatus=1\nMainPID=0\n", systemd{}},
		{"launchd", "\tstate = spawn scheduled\n\tlast exit code = 1\n", launchd{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newStub(t, tc.m)
			if _, err := Install(context.Background(), s.opts("state")); err != nil {
				t.Fatal(err)
			}
			s.report(tc.out)
			st := Inspect(context.Background(), s.opts("state"))
			if st.Running || !st.Starting || !strings.Contains(st.LastExit, "1") {
				t.Errorf("status %+v, want restarting, not running, with its last exit", st)
			}
		})
	}
}

// An install cut short between enabling the unit and starting it, as by
// closing Settings, still undoes the enabling: the rollback has time of
// its own.
func TestInstallCancelledMidwayRollsBack(t *testing.T) {
	s := newStub(t, systemd{})
	s.hang("systemctl --user start*")
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if _, err := Install(ctx, s.opts("state")); err == nil {
		t.Fatal("Install succeeded, want it cut short")
	}
	if s.has("enabled") {
		t.Errorf("the cut-short install left the unit enabled; calls %q", s.calls())
	}
	if def, ok := s.definition(); ok {
		t.Errorf("the cut-short install left its definition:\n%s", def)
	}
}

// A definition that names its directory as -dir=DIR, as one written by
// hand can, is that directory's as much as one with -dir DIR.
func TestADirFlagWithAnEqualsSignNamesTheDirectory(t *testing.T) {
	for _, tc := range managers {
		t.Run(tc.name, func(t *testing.T) {
			s := newStub(t, tc.m)
			prod := s.opts("prod")
			if _, err := Install(context.Background(), prod); err != nil {
				t.Fatal(err)
			}
			def, _ := s.definition()
			def = strings.Replace(def, map[string]string{
				"launchd": "<string>-dir</string>\n\t\t<string>",
				"systemd": `"-dir" "`,
			}[tc.name], map[string]string{
				"launchd": "<string>-dir=",
				"systemd": `"-dir=`,
			}[tc.name], 1)
			if err := os.WriteFile(s.m.path(s.home), []byte(def), 0o644); err != nil {
				t.Fatal(err)
			}
			if st := Inspect(context.Background(), s.opts("dev")); !st.Foreign || st.Dir != prod.Dir {
				t.Errorf("status from another directory %+v, want installed for %s and foreign; definition\n%s", st, prod.Dir, def)
			}
			if _, err := Uninstall(context.Background(), s.opts("dev")); err == nil {
				t.Error("Uninstall from another directory succeeded")
			}
		})
	}
}

// A state directory reached through a symlink is the same directory.
func TestASymlinkedStateDirectoryIsTheSame(t *testing.T) {
	for _, tc := range managers {
		t.Run(tc.name, func(t *testing.T) {
			s := newStub(t, tc.m)
			real := s.opts("real")
			if err := os.MkdirAll(real.Dir, 0o700); err != nil {
				t.Fatal(err)
			}
			alias := s.opts("alias")
			if err := os.Symlink(real.Dir, alias.Dir); err != nil {
				t.Fatal(err)
			}
			if _, err := Install(context.Background(), alias); err != nil {
				t.Fatal(err)
			}
			if st := Inspect(context.Background(), real); st.Foreign {
				t.Errorf("status from %s reads the service for its alias %s as foreign: %+v", real.Dir, alias.Dir, st)
			}
			if _, err := Uninstall(context.Background(), real); err != nil {
				t.Errorf("Uninstall from %s: %v", real.Dir, err)
			}
		})
	}
}

// When launchd can't say whether it runs the job, Uninstall still stops
// it, and says so when that fails too, rather than leave it running.
func TestUninstallStopsAJobLaunchdCantDescribe(t *testing.T) {
	for _, tc := range []struct {
		name, fail string
		ok         bool
	}{
		{"bootout works", "launchctl print*", true},
		{"bootout refused", "launchctl [pb][ro]*", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newStub(t, launchd{})
			if _, err := Install(context.Background(), s.opts("state")); err != nil {
				t.Fatal(err)
			}
			s.calls()
			s.fail(tc.fail)
			_, err := Uninstall(context.Background(), s.opts("state"))
			calls := s.calls()
			if !hasCall(calls, "launchctl bootout*") {
				t.Errorf("Uninstall called %q, want a bootout", calls)
			}
			_, kept := s.definition()
			if tc.ok && (err != nil || kept) {
				t.Errorf("Uninstall = %v, definition kept %v; want it gone", err, kept)
			}
			if !tc.ok && (err == nil || !kept) {
				t.Errorf("Uninstall = %v, definition kept %v; want the refusal and the definition", err, kept)
			}
		})
	}
	t.Run("not loaded", func(t *testing.T) {
		s := newStub(t, launchd{})
		if _, err := Install(context.Background(), s.opts("state")); err != nil {
			t.Fatal(err)
		}
		os.Remove(filepath.Join(s.dir, "loaded"))
		if _, err := Uninstall(context.Background(), s.opts("state")); err != nil {
			t.Errorf("Uninstall of a job launchd does not run: %v", err)
		}
		if _, ok := s.definition(); ok {
			t.Error("the definition stays")
		}
	})
}

// The service runs the glitched that installs it, which knows serve's
// -wait-lock, not an older one first on PATH.
func TestInstallRunsTheRunningBinary(t *testing.T) {
	s := newStub(t, osManagerOr(t))
	if err := os.WriteFile(filepath.Join(s.dir, "glitched"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	o := s.opts("state")
	o.Binary = ""
	st, err := Install(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	exe, _ := os.Executable()
	if st.Binary != exe {
		t.Errorf("the service runs %s, want this binary %s", st.Binary, exe)
	}
}

func osManagerOr(t *testing.T) manager {
	if osManager == nil {
		t.Skip("no background service on this OS")
	}
	return osManager
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

// scripts/glitched run for one state directory, as GLITCHED_DIR names it,
// leaves a service installed for another be, and still manages its own.
func TestScriptLeavesAnotherDirectorysServiceBe(t *testing.T) {
	if osManager == nil {
		t.Skip("no background service on this OS")
	}
	s := newStub(t, osManager)
	script, err := filepath.Abs(filepath.Join("..", "..", "scripts", "glitched"))
	if err != nil {
		t.Fatal(err)
	}
	glitched := func(dir string, args ...string) (string, error) {
		cmd := exec.Command("bash", append([]string{script}, args...)...)
		cmd.Env = append(os.Environ(), "HOME="+s.home, "GLITCHED_DIR="+dir, "GLITCHED_BIN_DIR="+t.TempDir())
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	for _, tc := range []struct{ name, def string }{
		{"Settings' definition", ""},
		{"the script's definition", map[string]string{
			"darwin": "<plist>\n<dict>\n  <key>ProgramArguments</key>\n  <array>\n    <string>/bin/glitched</string>\n    <string>serve</string>\n    <string>-dir</string>\n    <string>" + filepath.Join(s.home, "prod") + "</string>\n    <string>-wait-lock</string>\n  </array>\n</dict>\n</plist>\n",
			"linux":  "[Service]\nExecStart=\"/bin/glitched\" serve -dir \"" + filepath.Join(s.home, "prod") + "\" -wait-lock\n",
		}[runtime.GOOS]},
		{"a definition with -dir=DIR", map[string]string{
			"darwin": "<plist>\n<dict>\n  <key>ProgramArguments</key>\n  <array>\n    <string>/bin/glitched</string>\n    <string>serve</string>\n    <string>-dir=" + filepath.Join(s.home, "prod") + "</string>\n    <string>-wait-lock</string>\n  </array>\n</dict>\n</plist>\n",
			"linux":  "[Service]\nExecStart=\"/bin/glitched\" \"serve\" \"-dir=" + filepath.Join(s.home, "prod") + "\" \"-wait-lock\"\n",
		}[runtime.GOOS]},
		{"a definition with an unquoted --dir=DIR", map[string]string{
			"darwin": "<plist>\n<dict>\n  <key>ProgramArguments</key>\n  <array>\n    <string>/bin/glitched</string>\n    <string>serve</string>\n    <string>--dir=" + filepath.Join(s.home, "prod") + "</string>\n  </array>\n</dict>\n</plist>\n",
			"linux":  "[Service]\nExecStart=/bin/glitched serve --dir=" + filepath.Join(s.home, "prod") + " -wait-lock\n",
		}[runtime.GOOS]},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Install(context.Background(), s.opts("prod")); err != nil {
				t.Fatal(err)
			}
			if tc.def != "" {
				if err := os.WriteFile(s.m.path(s.home), []byte(tc.def), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			def, _ := s.definition()
			s.calls()
			for _, action := range []string{"stop", "uninstall", "restart", "install"} {
				out, err := glitched(filepath.Join(s.home, "dev"), "daemon", action)
				if err == nil || !strings.Contains(out, filepath.Join(s.home, "prod")) {
					t.Errorf("daemon %s from the other directory = %v:\n%s\nwant a refusal naming the service's directory", action, err, out)
				}
			}
			if now, _ := s.definition(); now != def {
				t.Errorf("the other directory rewrote the definition to\n%s", now)
			}
			if calls := s.calls(); len(calls) > 0 && (hasCall(calls, "launchctl bootout*") || hasCall(calls, "systemctl --user disable*") || hasCall(calls, "systemctl --user stop*")) {
				t.Errorf("the other directory called %q", calls)
			}
			if out, err := glitched(filepath.Join(s.home, "prod"), "daemon", "uninstall"); err != nil {
				t.Fatalf("daemon uninstall from its own directory: %v\n%s", err, out)
			}
			if _, ok := s.definition(); ok {
				t.Error("daemon uninstall from its own directory left the definition")
			}
		})
	}
}

// scriptIn runs scripts/glitched's functions under home: body runs after
// the script is sourced, so a test can call or replace one.
func scriptIn(t *testing.T, home string, env []string, body string) (string, error) {
	t.Helper()
	script, err := filepath.Abs(filepath.Join("..", "..", "scripts", "glitched"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", "-c", `source "$1"; `+body, "glitched-test", script)
	cmd.Env = append(os.Environ(), append([]string{"HOME=" + home}, env...)...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// Rebuilding the GUI quits its window, not the background service Settings
// installed, which runs the same executable as serve.
func TestScriptQuitsTheWindowNotTheService(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("on macOS quit_gui asks the app to quit")
	}
	home := t.TempDir()
	exe := filepath.Join(home, ".local", "glitched.app", "glitched")
	// Each runs as exe with args, as ps shows it.
	spawn := func(args ...string) (exited <-chan struct{}) {
		cmd := exec.Command("bash", append([]string{"-c", `exec -a "$0" bash -c 'sleep 30; :' "$@"`, exe}, args...)...)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		done := make(chan struct{})
		go func() { cmd.Wait(); close(done) }()
		t.Cleanup(func() { cmd.Process.Kill(); <-done })
		return done
	}
	window := spawn()
	svc := spawn("serve", "-dir", filepath.Join(home, ".glitched"), "-wait-lock")
	time.Sleep(200 * time.Millisecond) // for ps to show them as exe
	if out, err := scriptIn(t, home, nil, "quit_gui"); err != nil {
		t.Fatalf("quit_gui: %v\n%s", err, out)
	}
	select {
	case <-window:
	case <-time.After(5 * time.Second):
		t.Error("quit_gui left the window running")
	}
	select {
	case <-svc:
		t.Error("quit_gui stopped the background service")
	default:
	}
}

// daemon install rebuilds a CLI too old to know serve's -wait-lock, which
// the service it writes runs, and keeps one that knows it.
func TestScriptRebuildsACLIThatLacksWaitLock(t *testing.T) {
	if osManager == nil {
		t.Skip("no background service on this OS")
	}
	for _, tc := range []struct {
		name, help string
		rebuild    bool
	}{
		{"old", "  -dir string", true},
		{"current", "  -wait-lock", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newStub(t, osManager)
			bin := t.TempDir()
			cli := "#!/bin/sh\ncase \"$1 $2\" in \"serve -h\") echo '" + tc.help + "' >&2 ;; esac\n[ \"$1\" = status ] && echo 'engine     serving as test'\nexit 0\n"
			if err := os.WriteFile(filepath.Join(bin, "glitched"), []byte(cli), 0o755); err != nil {
				t.Fatal(err)
			}
			built := filepath.Join(bin, "built")
			out, err := scriptIn(t, s.home, []string{"GLITCHED_DIR=" + filepath.Join(s.home, "state"), "GLITCHED_BIN_DIR=" + bin},
				`build_daemon() { : > "`+built+`"; }; daemon_install`)
			if err != nil {
				t.Fatalf("daemon install: %v\n%s", err, out)
			}
			if _, err := os.Stat(built); (err == nil) != tc.rebuild {
				t.Errorf("rebuilt %v, want %v\n%s", err == nil, tc.rebuild, out)
			}
		})
	}
}

// scripts/glitched run for a state directory through a symlink manages
// the service installed for it.
func TestScriptSeesASymlinkedStateDirectoryAsItsOwn(t *testing.T) {
	if osManager == nil {
		t.Skip("no background service on this OS")
	}
	s := newStub(t, osManager)
	real := s.opts("real")
	if err := os.MkdirAll(real.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := s.opts("alias")
	if err := os.Symlink(real.Dir, alias.Dir); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(context.Background(), alias); err != nil {
		t.Fatal(err)
	}
	if out, err := scriptIn(t, s.home, []string{"GLITCHED_DIR=" + real.Dir}, "daemon_uninstall"); err != nil {
		t.Fatalf("daemon uninstall from %s: %v\n%s", real.Dir, err, out)
	}
	if _, ok := s.definition(); ok {
		t.Error("daemon uninstall left the definition")
	}
}

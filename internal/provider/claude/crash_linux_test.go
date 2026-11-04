package claude_test

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/provider"
	"glitched/internal/provider/claude"
	"glitched/internal/runtime"
	"glitched/internal/store"
)

// runOf is the state of thread's run, once ok holds for it or turnWait
// passes.
func runOf(t *testing.T, h *runtime.Host, thread api.ID, ok func(api.RunState) bool) api.RunState {
	t.Helper()
	var state api.RunState
	for deadline := time.Now().Add(turnWait); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		snap, err := h.Read(ctx, api.Query{Thread: thread})
		if err != nil {
			t.Fatal(err)
		}
		for _, th := range snap.Threads {
			if th.ID == thread {
				state = th.Run
			}
		}
		if ok(state) {
			break
		}
	}
	return state
}

func uncertain(s api.RunState) bool { return s == api.RunUncertain }

// waitFor waits for the fake to note a process in role.
func waitFor(t *testing.T, pid func(string) int, role string) int {
	t.Helper()
	for deadline := time.Now().Add(turnWait); ; time.Sleep(5 * time.Millisecond) {
		if p := pid(role); p > 0 {
			return p
		}
		if time.Now().After(deadline) {
			t.Fatalf("no %s process started", role)
		}
	}
}

// queue commits a claude thread in ws and its first message to a new
// store at path, which no engine serves yet. The run takes the message's id.
func queue(t *testing.T, path, ws, text string) (thread, run api.ID) {
	t.Helper()
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	thread, run = api.NewID(), api.NewID()
	for _, c := range []api.Command{
		{ID: api.NewID(), Body: api.ThreadCreate{Workspace: ws, Title: "crash", Access: api.AccessFull, Model: api.ModelRef{Provider: "claude", Model: "haiku"}}},
		{ID: run, Body: api.MessageDispatch{Text: text, Mode: api.StartNow}},
	} {
		c.Target = thread
		if _, err := st.Submit(ctx, c, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	return thread, run
}

// marked is the processes that carry run as theirs: its claude, and all
// that claude started.
func marked(run api.ID) []int {
	want := []byte("GLITCHED_CLAUDE_RUN=" + run.String())
	var pids []int
	dirs, _ := os.ReadDir("/proc")
	for _, d := range dirs {
		pid, err := strconv.Atoi(d.Name())
		if err != nil {
			continue
		}
		env, _ := os.ReadFile(filepath.Join("/proc", d.Name(), "environ"))
		if slices.ContainsFunc(bytes.Split(env, []byte{0}), func(kv []byte) bool { return bytes.Equal(kv, want) }) {
			pids = append(pids, pid)
		}
	}
	return pids
}

// noneLeft waits up to d for run's processes to be gone.
func noneLeft(run api.ID, d time.Duration) bool {
	for deadline := time.Now().Add(d); ; time.Sleep(20 * time.Millisecond) {
		if len(marked(run)) == 0 {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
	}
}

// killedEngine serves the store at path in a process of its own until
// ready says the run is far enough along, then kills that process.
func killedEngine(t *testing.T, path string, ready func() bool) {
	t.Helper()
	eng := exec.Command(must(os.Executable()), "-test.run=^$")
	eng.Env = append(os.Environ(), envEngine+"="+path)
	if err := eng.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { eng.Process.Kill(); eng.Wait() })
	for deadline := time.Now().Add(turnWait); !ready(); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the run never got going")
		}
	}
	eng.Process.Kill()
	eng.Wait()
}

// The plan's check: an engine killed mid-tool-call leaves no claude and no
// tool shell running, the next engine finds the run uncertain, and the
// thread's next turn resumes its Claude session.
func TestAKilledEngineLeavesNoProcessAndTheNextTurnResumes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "host.db")
	thread, run := queue(t, path, t.TempDir(), "Run sleep 60.")
	argv := replay(t, "busy-tool", "")
	pid := processes(t)
	killedEngine(t, path, func() bool { return pid("tool") > 0 })
	if !exits(pid("claude"), 6*time.Second) || !exits(pid("tool"), 6*time.Second) || !noneLeft(run, 0) {
		t.Fatal("claude or its tool shell outlived the engine that started them")
	}

	h := hostAt(t, path, nil)
	if got := runOf(t, h, thread, uncertain); got != api.RunUncertain {
		t.Fatalf("the run is %v after the restart, want uncertain", got)
	}
	submit(t, h, thread, api.TurnInterrupt{})
	replay(t, "again", "again-resume")
	t.Setenv(envArgv, argv)
	got := turn(t, h, thread, "What did I ask you to do last time? Answer in under ten words.")
	if got.ended.Outcome != api.OutcomeCompleted || !strings.Contains(got.text, "say hi") {
		t.Errorf("the next turn %+v, want Claude to answer from the session", got)
	}
	var sessions [][2]string
	for _, l := range strings.Split(strings.TrimSpace(string(must(os.ReadFile(argv)))), "\n") {
		var args []string
		json.Unmarshal([]byte(l), &args)
		sessions = append(sessions, [2]string{args[len(args)-2], args[len(args)-1]})
	}
	if len(sessions) != 3 || sessions[0][0] != "--session-id" || sessions[2] != [2]string{"--resume", sessions[0][1]} {
		t.Errorf("claude ran with sessions %v, want the crashed run's session resumed", sessions)
	}
}

// A tool shell runs in a session of its own and outlives a claude that
// dies without stopping it. The next engine finds and stops it.
func TestReconcileStopsAToolShellItsClaudeLeftBehind(t *testing.T) {
	path := filepath.Join(t.TempDir(), "host.db")
	thread, _ := queue(t, path, t.TempDir(), "Run sleep 60.")
	replay(t, "orphan-tool", "")
	pid := processes(t)
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	h, err := runtime.New(ctx, st, provider.Mux{"claude": &claude.Adapter{Path: must(os.Executable())}})
	if err != nil {
		t.Fatal(err)
	}
	tool := waitFor(t, pid, "tool")
	h.Close()
	st.Close()
	syscall.Kill(pid("claude"), syscall.SIGKILL)
	if !exits(pid("claude"), 3*time.Second) || exits(tool, 200*time.Millisecond) {
		t.Fatal("want claude gone and its tool shell still running")
	}

	h = hostAt(t, path, nil)
	if got := runOf(t, h, thread, uncertain); got != api.RunUncertain {
		t.Errorf("the run is %v after the restart, want uncertain", got)
	}
	if !exits(tool, 0) {
		t.Error("the tool shell still runs after the next engine reconciled its run")
	}
}

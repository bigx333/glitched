package claude

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/engine"
	"glitched/internal/provider"
	"glitched/internal/runtime"
	"glitched/internal/store"
	"golang.org/x/sys/unix"
)

type startWithBlockedSignals struct {
	*Adapter
	t *testing.T
}

func (a startWithBlockedSignals) Open(ctx context.Context, spec provider.SessionSpec) (provider.Agent, error) {
	type result struct {
		agent provider.Agent
		err   error
	}
	ch := make(chan result, 1)
	go func() {
		deny(a.t, unix.SYS_PIDFD_SEND_SIGNAL, unix.EPERM)
		ag, err := a.Adapter.Open(ctx, spec)
		ch <- result{agent: ag, err: err}
	}()
	r := <-ch
	return r.agent, r.err
}

func TestDeletingAfterAFailedStartStopsWhatItLeft(t *testing.T) {
	if scopes().dir != "" {
		t.Skip("fixture needs pidfd-only sweeping without cgroup.kill")
	}
	dir := t.TempDir()
	tape := filepath.Join(dir, "init-failed.jsonl")
	pids := filepath.Join(dir, "pids")
	if err := os.WriteFile(tape, []byte(`{"sent":{"request":{"subtype":"initialize"},"type":"control_request"}}
{"orphan":["sleep","30"]}
{"recv":{"type":"control_response","response":{"subtype":"error","request_id":"glitched-init","error":"refused by fixture"}}}
{"exit":1}
`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GLITCHED_FAKE_CLAUDE", tape)
	t.Setenv("GLITCHED_FAKE_CLAUDE_PIDS", pids)
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	adapter := &Adapter{Path: bin}
	defer adapter.Close()
	ctx := context.Background()
	dbPath := filepath.Join(dir, "db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	th := api.NewID()
	send := func(b api.Intent) {
		t.Helper()
		if _, err := st.Submit(ctx, api.Command{ID: api.NewID(), Target: th, Body: b}, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	h, err := runtime.New(ctx, st, provider.Mux{"claude": startWithBlockedSignals{adapter, t}})
	if err != nil {
		t.Fatal(err)
	}
	func() {
		defer h.Close()
		send(api.ThreadCreate{Workspace: dir, Title: "thread", Model: api.ModelRef{Provider: "claude", Model: "haiku"}})
		send(api.MessageDispatch{Text: "first", Mode: api.StartNow})
		for deadline := time.Now().Add(8 * time.Second); ; time.Sleep(5 * time.Millisecond) {
			pr, err := st.Thread(ctx, th)
			if err != nil {
				t.Fatal(err)
			}
			if len(pr.Runs) == 1 && pr.Runs[0].State == engine.RunFinished {
				r := pr.Runs[0]
				if r.Outcome != api.OutcomeFailed || !strings.Contains(r.Error, "refused by fixture") {
					t.Fatalf("first start wasn't refused by the launched Claude: %+v", r)
				}
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("failed initialize never ended")
			}
		}
	}()
	pidsText, err := os.ReadFile(pids)
	if err != nil {
		t.Fatal(err)
	}
	var tool int
	for _, line := range strings.Split(string(pidsText), "\n") {
		var role string
		var p int
		if n, _ := fmt.Sscan(line, &role, &p); n == 2 && role == "tool" {
			tool = p
		}
	}
	if tool <= 1 || !carries(tool, threadMark(th)) {
		t.Fatalf("failed initialize did not leave a current-marked descendant: %q", pidsText)
	}
	defer syscall.Kill(tool, syscall.SIGKILL)
	send(api.MessageDispatch{Text: "second", Mode: api.StartNow})
	send(api.ThreadDelete{})
	projection, err := st.Thread(ctx, th)
	if err != nil {
		t.Fatal(err)
	}
	if !projection.Deleted || projection.Generation != 2 || len(projection.Queue) != 2 ||
		projection.Queue[0].State != engine.QueueCancelled || projection.Queue[1].State != engine.QueueCancelled {
		t.Fatalf("fixture needs two cancelled messages after delete: %+v", projection.Queue)
	}

	h, err = runtime.New(ctx, st, provider.Mux{"claude": adapter})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		var state string
		if err := db.QueryRow(`SELECT state FROM effects WHERE thread = ? AND kind = 'agent.close'`, th[:]).Scan(&state); err != nil {
			t.Fatal(err)
		}
		if state == "done" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("agent.close never finished, state=%s", state)
		}
	}
	if left := (sweep{m: threadMark(th)}).scan(); len(left) != 0 {
		skipErr := fmt.Sprintf("delete completed agent.close, but current-marked descendant %d survived (%v)", tool, left)
		_, err := adapter.Reconcile(ctx, provider.RuntimeIdentity{Thread: th, Run: projection.Runs[1].ID, Generation: 2, Provider: "claude"})
		if err != nil || len((sweep{m: threadMark(th)}).scan()) > 0 {
			t.Fatalf("fixture could not prove the ordinary Reconcile can clean up: %v", err)
		}
		t.Fatal(skipErr)
	}
}

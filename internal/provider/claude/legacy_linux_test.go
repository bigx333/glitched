package claude_test

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/provider"
	"glitched/internal/provider/claude"
)

func legacyTool(t *testing.T, run api.ID) (int, <-chan struct{}) {
	t.Helper()
	mark := "GLITCHED_CLAUDE_RUN=" + run.String()
	p := exec.Command("sleep", "30")
	p.Env = append(os.Environ(), mark)
	if err := p.Start(); err != nil {
		t.Fatal(err)
	}
	gone := make(chan struct{})
	go func() { p.Wait(); close(gone) }()
	t.Cleanup(func() { p.Process.Kill(); <-gone })
	env := fmt.Sprintf("/proc/%d/environ", p.Process.Pid)
	for deadline := time.Now().Add(time.Second); ; time.Sleep(time.Millisecond) {
		if raw, err := os.ReadFile(env); err == nil && bytes.Contains(raw, []byte(mark+"\x00")) {
			return p.Process.Pid, gone
		}
		if time.Now().After(deadline) {
			t.Fatalf("legacy marker never appeared in %s", env)
		}
	}
}

func TestReconcileStopsAToolTheOldMarkNamed(t *testing.T) {
	id := provider.RuntimeIdentity{Thread: api.NewID(), Run: api.NewID(), Generation: 1, Provider: "claude"}
	pid, gone := legacyTool(t, id.Run)
	if _, err := (&claude.Adapter{}).Reconcile(ctx, id); err != nil {
		t.Fatal(err)
	}
	select {
	case <-gone:
	case <-time.After(time.Second):
		t.Fatalf("Reconcile did not stop ready legacy process %d", pid)
	}
}

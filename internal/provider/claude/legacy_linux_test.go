package claude_test

import (
	"os"
	"os/exec"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/provider"
	"glitched/internal/provider/claude"
)

func TestReconcileStopsAToolTheOldMarkNamed(t *testing.T) {
	id := provider.RuntimeIdentity{Thread: api.NewID(), Run: api.NewID(), Generation: 1, Provider: "claude"}
	p := exec.Command("sleep", "30")
	p.Env = append(os.Environ(), "GLITCHED_CLAUDE_RUN="+id.Run.String())
	if err := p.Start(); err != nil {
		t.Fatal(err)
	}
	gone := make(chan error, 1)
	go func() { gone <- p.Wait() }()
	t.Cleanup(func() { p.Process.Kill() })
	result, err := (&claude.Adapter{}).Reconcile(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-gone:
	case <-time.After(time.Second):
		t.Fatalf("Reconcile returned %+v without stopping pre-upgrade tool %d", result, p.Process.Pid)
	}
}

package daemon

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The Mac's hold goes with the engine's process, also when the engine is
// killed before it can let go.
func TestTheHoldGoesWithAKilledEngine(t *testing.T) {
	if os.Getenv("GLITCHED_HOLDING_ENGINE") != "" {
		inhibit(context.Background())
		return
	}
	engine := exec.Command(os.Args[0], "-test.run=^TestTheHoldGoesWithAKilledEngine$")
	engine.Env = append(os.Environ(), "GLITCHED_HOLDING_ENGINE=1")
	if err := engine.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { engine.Process.Kill(); engine.Wait() })
	var hold int
	for deadline := time.Now().Add(10 * time.Second); hold == 0; time.Sleep(20 * time.Millisecond) {
		if out, err := exec.Command("/usr/bin/pgrep", "-P", strconv.Itoa(engine.Process.Pid), "caffeinate").Output(); err == nil {
			hold, _ = strconv.Atoi(strings.Fields(string(out))[0])
		}
		if time.Now().After(deadline) {
			t.Fatal("the engine never held the Mac awake")
		}
	}
	engine.Process.Kill()
	engine.Wait()
	for deadline := time.Now().Add(10 * time.Second); syscall.Kill(hold, 0) == nil; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			syscall.Kill(hold, syscall.SIGKILL)
			t.Fatal("the hold outlived the killed engine")
		}
	}
}

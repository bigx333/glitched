package claude

import (
	"context"
	"os"
	"sync"
	"testing"

	"glitched/internal/api"
)

// Two stops of one run at once must not both close its jobs' handles,
// nor read a handle the other closed as a job still running, and neither
// may say the run is over before it is.
func TestTwoStopsOfARunAtOnceBothEndIt(t *testing.T) {
	for range 20 {
		run := api.NewID().String()
		self, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		cmd := Command(context.Background(), self)
		cmd.Env = append(cmd.Env, "GLITCHED_FAKE_SLEEP=60s")
		contain(cmd, run)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		if err := confine(cmd, run); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		errs := make([]error, 2)
		found := make([]bool, 2)
		for i := range errs {
			wg.Go(func() { found[i], errs[i] = stopRun(context.Background(), run) })
		}
		wg.Wait()
		cmd.Wait()
		for i, err := range errs {
			if err != nil || !found[i] {
				t.Fatalf("stopRun = %v, %v, want the run's process found and stopped", found[i], err)
			}
		}
	}
}

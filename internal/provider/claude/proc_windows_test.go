package claude

import (
	"context"
	"os"
	"sync"
	"testing"

	"glitched/internal/api"
)

// Two stops of one run at once must not both close its jobs' handles,
// nor read a handle the other closed as a job still running.
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
		for i := range errs {
			wg.Go(func() { _, errs[i] = stopRun(context.Background(), run) })
		}
		wg.Wait()
		cmd.Wait()
		for _, err := range errs {
			if err != nil {
				t.Fatalf("stopRun: %v", err)
			}
		}
	}
}

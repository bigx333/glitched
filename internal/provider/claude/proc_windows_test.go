package claude

import (
	"context"
	"os"
	"sync"
	"testing"

	"glitched/internal/api"

	"golang.org/x/sys/windows"
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
		h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(cmd.Process.Pid))
		if err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		errs := make([]error, 2)
		found := make([]bool, 2)
		early := make([]bool, 2)
		for i := range errs {
			wg.Go(func() {
				found[i], errs[i] = stopRun(context.Background(), run)
				ev, _ := windows.WaitForSingleObject(h, 0)
				early[i] = errs[i] == nil && ev != windows.WAIT_OBJECT_0
			})
		}
		wg.Wait()
		cmd.Wait()
		windows.CloseHandle(h)
		for i, err := range errs {
			if err != nil || early[i] {
				t.Fatalf("stopRun = %v, %v, with the process still running: %v", found[i], err, early[i])
			}
		}
		if !found[0] && !found[1] {
			t.Fatal("neither stop found the run's process")
		}
	}
}

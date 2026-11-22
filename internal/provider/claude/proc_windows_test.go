package claude

import (
	"context"
	"os"
	"sync"
	"testing"

	"glitched/internal/api"

	"golang.org/x/sys/windows"
)

// Two stops of one launch at once, one by its mark and one by its
// thread's, must not both close its job's handle, nor read a handle the
// other closed as a job still running, and neither may say the launch is
// over before it is.
func TestTwoStopsOfALaunchAtOnceBothEndIt(t *testing.T) {
	for range 20 {
		thread := api.NewID()
		m := launchMark(thread, api.NewID())
		marks := []mark{m, threadMark(thread)}
		self, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		cmd := Command(context.Background(), self)
		cmd.Env = append(cmd.Env, "GLITCHED_FAKE_SLEEP=60s")
		contain(cmd, m)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		if err := confine(cmd, m); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { stopRun(context.Background(), m); cmd.Process.Kill() })
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
				found[i], errs[i] = stopRun(context.Background(), marks[i])
				ev, _ := windows.WaitForSingleObject(h, 0)
				early[i] = errs[i] == nil && ev != windows.WAIT_OBJECT_0
			})
		}
		wg.Wait()
		windows.CloseHandle(h)
		for i, err := range errs {
			if err != nil || early[i] {
				t.Fatalf("stopRun = %v, %v, with the process still running: %v", found[i], err, early[i])
			}
		}
		if !found[0] && !found[1] {
			t.Fatal("neither stop found the run's process")
		}
		cmd.Wait()
	}
}

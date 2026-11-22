package claude

import (
	"context"
	"slices"
	"testing"

	"golang.org/x/sys/unix"

	"glitched/internal/api"
)

// execd is whether pid runs sleep.
func execd(pid int) bool {
	p, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	return err == nil && unix.ByteSliceToString(p.Proc.P_comm[:]) == "sleep"
}

// The scan finds a marked claude by its environment, which macOS shows no
// file for, and what it started under it, whose environment, as sleep's,
// macOS hides; stopRun ends them all.
func TestScanFindsAMarkedClaudeAndWhatItStarted(t *testing.T) {
	m := launchMark(api.NewID(), api.NewID())
	l := startHelper(t, m)
	other := startHelper(t, launchMark(api.NewID(), api.NewID()))
	found := sweep{m: m}.scan()
	slices.Sort(found)
	want := []int{l.claude.Process.Pid, l.server, l.tool}
	slices.Sort(want)
	if !slices.Equal(found, want) {
		t.Fatalf("scan = %v, want claude, its server and its tool, %v", found, want)
	}
	if ok, err := stopRun(context.Background(), m); !ok || err != nil {
		t.Fatalf("stopRun = %v, %v, want them found and stopped", ok, err)
	}
	if !l.gone() {
		t.Error("a marked process still runs")
	}
	if !other.running() {
		t.Error("a process of another launch was stopped")
	}
}

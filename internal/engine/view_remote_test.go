package engine

import (
	"testing"

	"glitched/internal/api"
)

// A placeholder shows its child's run there as the user sees one here: a
// silent run shows nothing, and an uncertain one stays uncertain.
func TestAPlaceholdersViewHidesASilentRunThere(t *testing.T) {
	for _, c := range []struct {
		silent bool
		slot   api.RunState
		want   api.RunState
	}{
		{false, api.RunRunning, api.RunRunning},
		{true, api.RunRunning, api.RunIdle},
		{true, api.RunUncertain, api.RunUncertain},
		{false, api.RunWaiting, api.RunWaiting},
	} {
		th := Thread{ID: api.NewID(), Remote: &Remote{Engine: api.NewID(), Link: api.NewID(), State: RemoteBoundState, Thread: api.NewID(), Run: api.NewID(), Silent: c.silent, Slot: c.slot}}
		v := View(th)
		if v.Run != c.want || v.Remote.Run != th.Remote.Run || v.Remote.Thread != th.Remote.Thread {
			t.Errorf("silent %v, slot %v: the view runs %v at %+v; want %v with the run and thread there", c.silent, c.slot, v.Run, v.Remote, c.want)
		}
	}
}

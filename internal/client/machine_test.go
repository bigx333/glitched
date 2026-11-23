package client

import (
	"fmt"
	"sync"
	"testing"

	"glitched/internal/daemon"
)

// A setting saved while the first engine embedded here is still starting
// reaches that engine: it read engine.json before the save, so it starts
// again from the saved file.
func TestASettingSavedAsTheFirstEngineStartsReachesIt(t *testing.T) {
	dir := stateDir(t)
	g := &gui{wake: make(chan struct{}, 1)}
	read, release := make(chan struct{}), make(chan struct{})
	var mu sync.Mutex
	var awake []bool
	m := New(Config{Dir: dir, Post: g.post, Engine: func(cfg *daemon.Config) {
		mu.Lock()
		awake = append(awake, cfg.KeepAwakeEnabled())
		first := len(awake) == 1
		mu.Unlock()
		if first {
			close(read)
			<-release
		}
	}})
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	<-read
	off := false
	if err := m.Configure(func(c *daemon.Config) { c.KeepAwake = &off }); err != nil {
		t.Fatal(err)
	}
	close(release)
	starts := func() []bool {
		mu.Lock()
		defer mu.Unlock()
		return append([]bool(nil), awake...)
	}
	g.until(t, "the engine started again with keep awake off", func() bool {
		s := starts()
		return m.machine.Embedded && len(s) == 2 && !s[1]
	}, func() string { return fmt.Sprint(starts(), m.machine.Embedded) })
}

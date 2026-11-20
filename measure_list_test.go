package main

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"glitched/internal/api"
	"glitched/internal/daemon"
	"glitched/internal/provider/fake"
)

// The sidebar drawn natively with 10,000 threads, against a plugin's list
// of 10,000 rows, frame by frame. Run with GLITCHED_MEASURE=1; CI skips it.
func TestMeasureTenThousandRows(t *testing.T) {
	if os.Getenv("GLITCHED_MEASURE") == "" {
		t.Skip("set GLITCHED_MEASURE=1 to measure the sidebar and a plugin's list at 10,000 rows")
	}
	const n = 10000
	frame := func(h *harness) time.Duration {
		h.tt.Frame()
		begin := time.Now()
		const frames = 20
		for range frames {
			h.tt.Frame()
		}
		return time.Since(begin) / frames
	}

	empty := pluginApp(t)
	t.Logf("empty app: frame %v", frame(empty))

	dir := tempDir(t)
	d := engine(t, daemon.Config{Dir: dir}, fake.New(fake.Turn(false)))
	seeding := time.Now()
	var wg sync.WaitGroup
	ids := make(chan int)
	for range 16 {
		wg.Go(func() {
			ep := d.Endpoint()
			for i := range ids {
				body := api.ThreadCreate{Workspace: fmt.Sprintf("/work/p%d", i%5), Title: fmt.Sprintf("Thread %d of the sidebar", i), Model: fakeModel}
				if _, err := ep.Submit(context.Background(), api.Command{ID: api.NewID(), Target: api.NewID(), Body: body}); err != nil {
					t.Error(err)
					return
				}
			}
		})
	}
	for i := range n {
		ids <- i
	}
	close(ids)
	wg.Wait()
	t.Logf("seeded %d threads in %v", n, time.Since(seeding))
	native := start(t, dir)
	native.until("10,000 threads in the sidebar", func() bool { return native.tt.HasText("Thread 0 of the sidebar") && len(native.a.itemsIn(catInbox)) == n })
	t.Logf("native sidebar, %d threads: frame %v", n, frame(native))

	list := pluginApp(t, "long-list")
	list.click("Long list")
	list.until("the rows", func() bool { return list.tt.HasText("Row 3") })
	t.Logf("plugin list, %d rows: frame %v", n, frame(list))
	s, _ := list.a.plugins.Session("long-list", "home")
	rev := s.Rev
	at := time.Now()
	list.click("Row 3")
	list.until("the next view", func() bool { return s.Rev > rev })
	t.Logf("plugin list: click to the plugin's next view of %d items %v", n, time.Since(at))
	at = time.Now()
	list.tt.Key(0, ui.KeyEnd)
	list.until("the last row", func() bool { list.tt.Frame(); return list.tt.HasText("Row 9999") })
	t.Logf("plugin list: End to the last row drawn %v", time.Since(at))
}

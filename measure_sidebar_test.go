package main

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/daemon"
	"glitched/internal/provider/fake"
)

// The sidebar at 1,000, 10,000 and 50,000 threads: how long it takes to show
// them, a frame once it has, and a new thread's way to its row. Run with
// GLITCHED_MEASURE=1; CI skips it.
func TestMeasureTheSidebar(t *testing.T) {
	if os.Getenv("GLITCHED_MEASURE") == "" {
		t.Skip("set GLITCHED_MEASURE=1 to measure the sidebar at 1,000, 10,000 and 50,000 threads")
	}
	for _, n := range []int{1000, 10000, 50000} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			dir := tempDir(t)
			d := engine(t, daemon.Config{Dir: dir}, fake.New(fake.Turn(false)))
			create := func(title string, i int) error {
				body := api.ThreadCreate{Workspace: fmt.Sprintf("/work/p%d", i%5), Title: title, Model: fakeModel}
				_, err := d.Endpoint().Submit(context.Background(), api.Command{ID: api.NewID(), Target: api.NewID(), Body: body})
				return err
			}
			var wg sync.WaitGroup
			ids := make(chan int)
			for range 16 {
				wg.Go(func() {
					for i := range ids {
						if err := create(fmt.Sprintf("Thread %d of the sidebar", i), i); err != nil {
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

			at := time.Now()
			h := start(t, dir)
			h.until("the threads in the sidebar", func() bool {
				return len(h.a.itemsIn(catInbox)) == n && h.tt.HasText(fmt.Sprintf("Thread %d of the sidebar", n-1))
			})
			shown := time.Since(at)

			h.tt.Frame()
			const frames = 20
			at = time.Now()
			for range frames {
				h.tt.Frame()
			}
			frame := time.Since(at) / frames

			at = time.Now()
			if err := create("A newcomer", 0); err != nil {
				t.Fatal(err)
			}
			h.until("the newcomer's row", func() bool { h.tt.Frame(); return h.tt.HasText("A newcomer") })
			newcomer := time.Since(at)
			t.Logf("%d threads: shown in %v, a frame %v, a new thread to its row %v", n, shown.Round(time.Millisecond), frame.Round(time.Microsecond), newcomer.Round(time.Millisecond))
		})
	}
}

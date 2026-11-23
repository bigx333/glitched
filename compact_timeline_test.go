package main

import (
	"context"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/daemon"
	"glitched/internal/provider/fake"
)

func TestACompactionAtWorkShowsNothingInTheTimeline(t *testing.T) {
	dir := tempDir(t)
	ad := &idleClaude{Adapter: fake.New(nil), compacting: make(chan struct{})}
	d := engine(t, daemon.Config{Dir: dir}, ad)
	const title = "Cache the sidebar sort"
	id := api.NewID()
	for _, b := range []api.Intent{
		api.ThreadCreate{Workspace: t.TempDir(), Title: title, Model: api.ModelRef{Provider: "claude"}},
		api.MessageDispatch{Text: "Cache it.", Mode: api.StartNow},
	} {
		if _, err := d.Endpoint().Submit(context.Background(), api.Command{ID: api.NewID(), Target: id, Body: b}); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case <-ad.compacting:
	case <-time.After(10 * time.Second):
		t.Fatal("the idle compaction never started")
	}

	h := start(t, dir)
	th := h.thread(title)
	h.a.select_(th)
	h.until("the prior turn's transcript and the silent run", func() bool {
		return h.tt.HasText("done") && th.View.Turn != nil && th.View.Turn.Silent
	})
	h.tt.Advance(time.Second)
	if h.tt.HasText("Compacting before cache expires") {
		t.Fatal("timeline shows Compacting before cache expires… while a silent run is working")
	}
}

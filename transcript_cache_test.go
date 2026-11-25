package main

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"glitched/internal/api"
	"glitched/internal/client"
	"glitched/internal/daemon"
	"glitched/internal/provider/fake"
)

// The manager keeps the transcripts of the last threads shown. Going back
// to the oldest of them keeps its own; a transcript it lets go leaves the
// window's rows too, and the thread shows in full when opened again.
func TestTheWindowLetsGoOfATranscriptTheManagerDrops(t *testing.T) {
	dir := tempDir(t)
	d := engine(t, daemon.Config{Dir: dir}, fake.New(fake.Turn(false)))
	var ids []api.ID
	for i := range 22 {
		ids = append(ids, seed(t, d, "/work/a", fmt.Sprintf("Thread %d", i), fmt.Sprintf("Hello %d", i)))
	}
	h := start(t, dir)
	show := func(i int) *client.Transcript {
		t.Helper()
		th := h.thread(fmt.Sprintf("Thread %d", i))
		h.a.select_(th)
		want := fmt.Sprintf("Hello %d", i)
		h.until(want+" shown", func() bool {
			h.tt.Frame()
			return slices.ContainsFunc(h.tt.Texts(), func(s string) bool { return strings.Contains(s, want) })
		})
		return th.Transcript
	}

	// 20 closed and the 21st shown: the manager drops none of them.
	first := show(0)
	for i := 1; i <= 20; i++ {
		show(i)
	}
	if again := show(0); again != first {
		t.Fatal("going back to the oldest transcript kept dropped it: closing the shown thread first made room by it")
	}

	// One more shown, and Thread 1, the oldest closed, is dropped.
	show(21)
	h.tt.Frame()
	if th := h.local().Threads[ids[1]]; th.Transcript != nil {
		t.Fatal("the manager kept Thread 1's transcript, the 21st closed")
	}
	if _, ok := h.a.rows[ids[1]]; ok {
		t.Fatal("the window kept the rows of Thread 1, whose transcript the manager dropped")
	}
	show(1)
}

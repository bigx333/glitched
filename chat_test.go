package main

import (
	"glitched/internal/api"
	"glitched/internal/client"
	"testing"
	"time"
)

// A message sent to a server's thread, which the server is removed before
// it lands, is no longer waited on.
func TestASteerToARemovedServerIsDropped(t *testing.T) {
	h := machines(t)
	th := h.thread("Rotate the API keys")
	h.a.select_(th)
	h.until("the server's transcript", func() bool { return h.tt.HasText("Which keys are older than 90 days?") })
	h.a.draft = "Only the billing ones."
	h.a.send(th)
	if len(h.a.steers) != 1 {
		t.Fatalf("%d steers after a send, want 1", len(h.a.steers))
	}
	if err := h.a.backend.RemoveServer("devbox"); err != nil {
		t.Fatal(err)
	}
	h.until("the server gone", func() bool { return len(h.a.backend.Envs()) == 1 })
	h.tt.Advance(time.Second)
	for range 20 {
		h.p.run()
		h.tt.Frame()
		time.Sleep(10 * time.Millisecond)
	}
	if len(h.a.steers) != 0 {
		t.Fatalf("%d steers kept for a server removed", len(h.a.steers))
	}
}

// A message the engine can't be sent, as to a thread it no longer has,
// stays in the composer.
func TestAFailedSendKeepsTheDraft(t *testing.T) {
	h := start(t, tempDir(t))
	gone := thread{h.local(), &client.Thread{View: api.Thread{ID: api.NewID(), Title: "Gone", Workspace: t.TempDir()}}}
	h.a.draft = "Keep this."
	h.a.send(gone)
	if h.a.draft != "Keep this." {
		t.Fatalf("the draft is %q after a send that went nowhere", h.a.draft)
	}
}

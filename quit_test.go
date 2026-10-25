package main

import (
	"testing"

	"github.com/egoist/mygo"
)

// A window that hides on close hides until a quit starts, and then lets the
// quit close it, so the quit completes whatever the windows' order.
func TestHideUnlessQuitting(t *testing.T) {
	a := &app{}
	hidden := 0
	onClose := a.hideUnlessQuitting(func() { hidden++ })

	e := &mygo.CloseEvent{}
	onClose(e)
	if !e.DefaultPrevented() || hidden != 1 {
		t.Fatalf("before a quit: prevented %v, hidden %d; want true, 1", e.DefaultPrevented(), hidden)
	}

	a.beforeQuit(&mygo.QuitEvent{})
	e = &mygo.CloseEvent{}
	onClose(e)
	if e.DefaultPrevented() || hidden != 1 {
		t.Fatalf("quitting: prevented %v, hidden %d; want false, 1", e.DefaultPrevented(), hidden)
	}
}

// The quitting mark stays once the quit has closed the window, as the main
// loop's next turn checks.
func TestQuittingStaysOnceClosed(t *testing.T) {
	a := &app{}
	var later []func()
	a.nextTurn = func(f func()) { later = append(later, f) }
	a.beforeQuit(&mygo.QuitEvent{})
	if len(later) != 1 {
		t.Fatalf("%d checks queued, want 1", len(later))
	}
	later[0]() // no window open: the quit went through
	if !a.quitting {
		t.Error("the mark came off a quit that went through")
	}
}

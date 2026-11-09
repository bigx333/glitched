package main

import (
	"sync"
	"testing"

	"github.com/egoist/mygo"
)

// What's posted before the window exists runs once it does, in order,
// and nothing posted then waits.
func TestPostsWaitForTheWindow(t *testing.T) {
	var q windowQueue
	var mu sync.Mutex
	var got []int
	add := func(n int) func(*mygo.Window) {
		return func(*mygo.Window) { mu.Lock(); got = append(got, n); mu.Unlock() }
	}
	q.post(add(1))
	q.post(add(2))
	if len(got) != 0 {
		t.Fatal("ran before the window")
	}
	q.ready(&mygo.Window{})
	q.post(add(3))
	if len(got) != 3 || got[0] != 1 || got[2] != 3 {
		t.Fatalf("ran %v", got)
	}
}

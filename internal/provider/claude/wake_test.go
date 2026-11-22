package claude

import "testing"

func TestWakeRejectsAReapedClaude(t *testing.T) {
	p := &proc{lines: make(chan []byte), reaped: make(chan struct{})}
	p.park()
	// supervise reaps the process before watch finishes draining its readers
	// and closes its output. A Take can reach Wake in that interval.
	close(p.reaped)
	if p.Wake() {
		t.Fatal("Wake said that a Claude process already reaped can take the next turn")
	}
}

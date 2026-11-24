package claude

import (
	"context"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"glitched/internal/provider"
)

// sizes is a tap that keeps the length of each line it sees on pipe.
func sizes(pipe provider.Pipe) (provider.Tap, func() []int) {
	var mu sync.Mutex
	var seen []int
	return func(_ provider.Protocol, p provider.Pipe, _ []byte, size int) {
			if p == pipe {
				mu.Lock()
				seen = append(seen, size)
				mu.Unlock()
			}
		}, func() []int {
			mu.Lock()
			defer mu.Unlock()
			return slices.Clone(seen)
		}
}

// claude's stderr reaches the diagnostic tail as it is read, a line not
// ended yet too, with a provider log's tap or without one; a tap sees
// the length of each line, without its line ending, once it ends.
func TestStderrReachesTheTailAsItIsRead(t *testing.T) {
	for _, tapped := range []bool{false, true} {
		t.Run(map[bool]string{false: "no tap", true: "a tap"}[tapped], func(t *testing.T) {
			outR, outW, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			errR, errW, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			tap, seen := sizes(provider.Stderr)
			p := &proc{stderr: &tail{}, reaped: make(chan struct{}), lines: make(chan []byte), done: make(chan struct{})}
			if tapped {
				p.tap = tap
			}
			go p.watch(outR, errR)
			if _, err := errW.WriteString("partial diagnostic"); err != nil {
				t.Fatal(err)
			}
			for until := time.Now().Add(5 * time.Second); p.stderr.String() != "partial diagnostic"; time.Sleep(time.Millisecond) {
				if time.Now().After(until) {
					t.Fatalf("the tail holds %q while stderr stays open, want the bytes read", p.stderr.String())
				}
			}
			errW.WriteString(" ends\r\nnext\n\nlast")
			outW.Close()
			errW.Close()
			close(p.reaped)
			for range p.lines {
			}
			if want := map[bool][]int{true: {23, 4, 4}}[tapped]; !slices.Equal(seen(), want) {
				t.Errorf("the tap saw lines of %v bytes, want %v", seen(), want)
			}
		})
	}
}

// A line to claude's stdin is tapped only once written whole, by its
// length without its line ending.
func TestStdinIsTappedOnceWritten(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	r.Close()
	tap, seen := sizes(provider.Stdin)
	p := &proc{stdin: w, tap: tap}
	if err := p.sendLocked(context.Background(), map[string]string{"type": "user"}); err == nil {
		t.Fatal("a write to a closed pipe went through")
	}
	if got := seen(); len(got) != 0 {
		t.Errorf("the tap saw %v of a line never written", got)
	}
	r, w, err = os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	go func() { b := make([]byte, 64); r.Read(b) }()
	p = &proc{stdin: w, tap: tap}
	if err := p.sendLocked(context.Background(), map[string]string{"type": "user"}); err != nil {
		t.Fatal(err)
	}
	if got := seen(); !slices.Equal(got, []int{len(`{"type":"user"}`)}) {
		t.Errorf("the tap saw lines of %v bytes, want one of %d", got, len(`{"type":"user"}`))
	}
}

//go:build linux || darwin

package claude

import (
	"context"
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"
)

// sweep is the processes a stop ends: m's, less those in process group
// spare, unless spare is 0.
type sweep struct {
	m     mark
	spare int
	// seen is what the sweep found on macOS, by PID, with its start time:
	// there a process is found by its ancestry, which it loses once its
	// marked ancestor exits.
	seen map[int]int64
}

// stopRun stops every process of m's: claude, and what it started, tool
// shells in sessions of their own included. They get SIGTERM, on which
// claude stops its shells itself, then SIGKILL once killGrace passes or ctx
// ends. It reports whether any process was found, and fails while one is
// left.
func stopRun(ctx context.Context, m mark) (bool, error) {
	s := sweep{m: m, seen: map[int]int64{}}
	if len(s.scan()) == 0 {
		return false, nil
	}
	return true, s.stop(ctx)
}

// stopTools stops the processes of m's outside process group group, as
// stopRun does. Claude runs as the leader of group, and the MCP servers it
// starts stay in it, while Claude Code runs each tool call in a shell of
// its own session: so a claude parked between turns keeps its servers, and
// the turn's tools end with the turn.
func stopTools(ctx context.Context, m mark, group int) error {
	return sweep{m: m, spare: group, seen: map[int]int64{}}.stop(ctx)
}

func (s sweep) stop(ctx context.Context) error {
	err := s.signal(syscall.SIGTERM)
	if s.settled(ctx, 0) == nil {
		return nil
	}
	if left := s.settled(context.Background(), syscall.SIGKILL); left != nil {
		return errors.Join(left, err)
	}
	return nil
}

// settled waits up to killGrace, or until ctx ends, for s's processes to
// exit. Each time it finds some left, it sends them sig, unless sig is 0.
// It fails with the processes left, and why a signal to them failed.
func (s sweep) settled(ctx context.Context, sig syscall.Signal) error {
	deadline := time.Now().Add(killGrace)
	var failed error
	for {
		left := s.scan()
		if len(left) == 0 {
			return nil
		}
		if ctx.Err() != nil || time.Now().After(deadline) {
			return errors.Join(fmt.Errorf("claude: processes %v of %s still run", left, s.m), failed)
		}
		if sig == syscall.SIGKILL {
			if err := s.kill(); err != nil {
				failed = err
			}
		}
		if sig != 0 {
			if err := s.signal(sig); err != nil {
				failed = err
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// terminate asks claude to stop: on SIGTERM it stops its tool shells.
func terminate(p *os.Process) error { return p.Signal(syscall.SIGTERM) }

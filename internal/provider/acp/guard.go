package acp

import (
	"fmt"
	"os"
	"testing"
)

// testAgent names, for a test binary and every process it starts, the one
// agent they may run: the test binary playing one. A program a test starts
// is not under go test itself, and learns of the test from this.
const testAgent = "GLITCHED_TEST_ACP"

func init() {
	if live || !testing.Testing() {
		return
	}
	if self, err := os.Executable(); err == nil {
		os.Setenv(testAgent, self)
	}
}

// guard stops a test from running the user's real, signed-in agent.
// Without the acplive tag, a test and what it starts may run only the
// test binary playing an agent (package acptest). A test binary panics;
// any other program gets the error, so it fails the turn and not the
// whole daemon.
func guard(bin string) error {
	if live {
		return nil
	}
	allowed, underTest := os.LookupEnv(testAgent)
	if !underTest && !testing.Testing() || sameFile(bin, allowed) {
		return nil
	}
	err := fmt.Errorf("acp: a test without the acplive tag tried to run %s; play the agent with acptest instead", bin)
	if testing.Testing() {
		panic(err.Error())
	}
	return err
}

func sameFile(a, b string) bool {
	fa, errA := os.Stat(a)
	fb, errB := os.Stat(b)
	return errA == nil && errB == nil && os.SameFile(fa, fb)
}

//go:build unix

package claude_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"glitched/internal/provider/claude"
)

// hiddenFlags are the flags of Claude Code's that branch a conversation.
var hiddenFlags = []string{"--resume-session-at", "--resume-drops-turn", "--fork-session"}

// wrapper writes a script that starts the test binary as claude, holding
// flags in a comment as Claude Code's program holds its hidden flags, and
// has the test run it: the test binary holds the adapter's own flags, so
// a probe of it would always find them.
func wrapper(t *testing.T, flags ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "claude")
	writeWrapper(t, path, flags...)
	claude.RunAs(t, path)
	return path
}

func writeWrapper(t *testing.T, path string, flags ...string) {
	t.Helper()
	script := "#!/bin/sh\n# " + strings.Join(flags, " ") + "\nexec '" + must(os.Executable()) + "' \"$@\"\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

// sessionArgs is each start's session flags, which argv ends with.
func sessionArgs(t *testing.T, argv string) [][]string {
	t.Helper()
	var got [][]string
	for _, l := range strings.Split(strings.TrimSpace(string(must(os.ReadFile(argv)))), "\n") {
		var args []string
		json.Unmarshal([]byte(l), &args)
		i := slices.IndexFunc(args, func(a string) bool { return a == "--resume" || a == "--session-id" })
		got = append(got, args[i:])
	}
	return got
}

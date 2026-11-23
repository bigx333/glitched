//go:build unix

package skills

import (
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// A pipe in a skill's tree is neither a file nor a folder, and Glitched
// never opens it.
func TestFindRefusesAPipe(t *testing.T) {
	state, _ := folders(t)
	dir := skill(t, filepath.Join(state, "skills"), "piped", "Holds a pipe.")
	if err := syscall.Mkfifo(filepath.Join(dir, "fifo"), 0o644); err != nil {
		t.Fatal(err)
	}
	set := Find(state, "", "")
	if len(set.Skills) != 0 || !strings.Contains(why(set, "piped"), "fifo, which is neither a file nor a folder") {
		t.Errorf("skills %v, skipped %+v", names(set.Skills), set.Skipped)
	}
}

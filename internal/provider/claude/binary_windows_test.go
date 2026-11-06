package claude

import (
	"os"
	"path/filepath"
	"testing"
)

// Windows has no execute bit, and Claude Code's native installer puts
// claude.exe in ~/.local/bin, which may not be on PATH.
func TestClaudeIsFoundOnWindows(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if got, err := (&Adapter{Path: self}).binary(); err != nil || got != self {
		t.Errorf("binary with claude_path set = %q, %v, want %s", got, err, self)
	}
	home := t.TempDir()
	bin := filepath.Join(home, ".local", "bin", "claude.exe")
	os.MkdirAll(filepath.Dir(bin), 0o755)
	if err := os.WriteFile(bin, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("USERPROFILE", home)
	t.Setenv("PATH", t.TempDir())
	if got, err := (&Adapter{}).binary(); err != nil || got != bin {
		t.Errorf("binary = %q, %v, want %s", got, err, bin)
	}
}

// Windows runs a program only by a name with an extension it knows, so a
// claude_path without one would fail at start with a bare "file does not
// exist" instead of saying how to install Claude Code.
func TestAClaudePathWindowsCannotRunIsNotInstalled(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(bin, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := (&Adapter{Path: bin}).binary(); err != ErrNotInstalled {
		t.Errorf("binary with claude_path %s = %q, %v, want ErrNotInstalled", bin, got, err)
	}
}

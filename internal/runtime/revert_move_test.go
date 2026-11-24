package runtime

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// A move that falls back to a copy writes it beside the file under a name
// of its own, never over a file already there.
func TestACopyIntoPlaceLeavesItsNeighboursAlone(t *testing.T) {
	root := t.TempDir()
	staged := filepath.Join(root, "staged")
	if err := os.Mkdir(staged, 0o755); err != nil {
		t.Fatal(err)
	}
	to := filepath.Join(root, "notes")
	from := filepath.Join(staged, "notes")
	beside := filepath.Join(root, ".glitched-restore-notes")
	for name, data := range map[string]string{from: "target", to: "old", beside: "unrelated user data"} {
		if err := os.WriteFile(name, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(staged, 0o555); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(staged, 0o755)
	if err := os.Rename(from, to); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("fixture needs a failed rename, got %v", err)
	}
	if err := moveInto(from, to); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(beside); err != nil || string(got) != "unrelated user data" {
		t.Fatalf("fallback removed neighbor: got %q, %v", got, err)
	}
}

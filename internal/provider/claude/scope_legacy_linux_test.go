package claude

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"glitched/internal/api"
)

func TestALegacyScopeFindsOnlyItsRun(t *testing.T) {
	d := t.TempDir()
	original := scopes
	scopes = func() slice { return slice{dir: d} }
	t.Cleanup(func() { scopes = original })
	thread, run, other, launch := api.NewID(), api.NewID(), api.NewID(), api.NewID()
	legacy := filepath.Join(d, "glitched-run-"+run.String()+"-"+launch.String()+".scope")
	current := filepath.Join(d, "glitched-claude-"+thread.String()+"-"+launch.String()+".scope")
	otherLegacy := filepath.Join(d, "glitched-run-"+other.String()+"-"+launch.String()+".scope")
	for _, path := range []string{legacy, current, otherLegacy} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if got := scopesOf(legacyMark(run)); !slices.Equal(got, []string{legacy}) {
		t.Fatalf("legacy scope discovery: got %v, want [%s]", got, legacy)
	}
	if got := scopesOf(threadMark(thread)); !slices.Equal(got, []string{current}) {
		t.Fatalf("current scope discovery: got %v, want [%s]", got, current)
	}
}

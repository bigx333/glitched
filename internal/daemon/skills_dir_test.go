package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/provider"
	"glitched/internal/provider/fake"
	"glitched/internal/skills/skillstest"
	"glitched/internal/transport"
)

// An engine started with a relative --dir gives its agents skill paths
// they can read from their own workspaces.
func TestARelativeDirGivesAgentsAbsoluteSkillPaths(t *testing.T) {
	root, ws := t.TempDir(), t.TempDir()
	t.Chdir(root)
	skillstest.Write(t, filepath.Join("state", "skills"), "sample", "A sample.")
	a := fake.New(fake.Turn(false))
	seen := make(chan provider.SessionSpec, 1)
	a.Confirms = func(s provider.SessionSpec) []provider.Event {
		seen <- s
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	d, err := Start(context.Background(), Config{Dir: "state", Hub: true, Providers: provider.Mux{"fake": a}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	ep := transport.NewEndpointClient(Socket("state"), api.ID{})
	defer ep.Close()
	id := api.NewID()
	for _, intent := range []api.Intent{
		api.ThreadCreate{Workspace: ws, Title: "t", Model: api.ModelRef{Provider: "fake", Model: "fake"}},
		api.MessageDispatch{Text: "hi", Mode: api.StartNow},
	} {
		if _, err := ep.Submit(ctx, api.Command{ID: api.NewID(), Target: id, Body: intent}); err != nil {
			t.Fatal(err)
		}
	}
	var s provider.SessionSpec
	select {
	case s = <-seen:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if len(s.Skills.List) != 1 {
		t.Fatalf("got %d skills", len(s.Skills.List))
	}
	file := s.Skills.List[0].Path
	fromProvider := file
	if !filepath.IsAbs(file) {
		fromProvider = filepath.Join(ws, file)
	}
	if _, err := os.Stat(fromProvider); err != nil {
		t.Fatalf("provider working dir %q cannot read its staged SKILL.md %q: %v", ws, file, err)
	}
}

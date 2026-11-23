package daemon

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"glitched/internal/api"
	"glitched/internal/skills/skillstest"
	"glitched/internal/transport"
)

// Each engine says what skills it gives its own threads' agents, from its
// own state folder and its machine's folders, to a paired client on the
// tailnet, to one on its unix socket and to the GUI that embeds it.
func TestEachEngineSaysItsThreadsSkills(t *testing.T) {
	ctx := context.Background()
	for _, own := range []string{"here", "there"} {
		dir, ws := filepath.Join(stateDir(t), "state"), stateDir(t)
		skillstest.Write(t, filepath.Join(dir, "skills"), own, "This machine's.")
		skillstest.Write(t, filepath.Join(dir, "skills"), "review", "My review.")
		skillstest.Write(t, filepath.Join(ws, ".glitched", "skills"), "review", "The project's review.")
		token, err := Pair(dir, "laptop")
		if err != nil {
			t.Fatal(err)
		}
		d := start(t, Config{Dir: dir, Listen: []string{"127.0.0.1:0"}})
		unix := transport.NewEndpointClient(Socket(dir), api.ID{})
		t.Cleanup(func() { unix.Close() })
		thread := api.NewID()
		if _, err := unix.Submit(ctx, api.Command{ID: api.NewID(), Target: thread, Body: api.ThreadCreate{Workspace: ws, Title: "t", Model: api.ModelRef{Provider: "fake", Model: "fake"}}}); err != nil {
			t.Fatal(err)
		}
		embedded, ok := d.Endpoint().(transport.Skills)
		if !ok {
			t.Fatalf("the embedded engine, a %T, serves no Skills", d.Endpoint())
		}
		want := []api.Skill{
			{Name: own, Description: "This machine's.", Source: "home", Shown: filepath.Join(dir, "skills", own)},
			{Name: "review", Description: "The project's review.", Source: "project", Shown: filepath.Join(filepath.Base(ws), ".glitched", "skills", "review"), Overrides: true},
		}
		slices.SortFunc(want, func(a, b api.Skill) int { return strings.Compare(a.Name, b.Name) })
		for name, k := range map[string]transport.Skills{"the tailnet": remote(t, d, token), "the unix socket": unix, "the embedded engine": embedded} {
			list, err := k.Skills(ctx, api.SkillQuery{Thread: thread})
			if err != nil || !slices.Equal(list.Skills, want) || len(list.Skipped) != 0 {
				t.Errorf("%s of the %s engine says %+v, %v; want %+v", name, own, list, err, want)
			}
			if _, err := k.Skills(ctx, api.SkillQuery{Thread: api.NewID()}); err == nil {
				t.Errorf("%s says the skills of a thread it doesn't have", name)
			}
		}
	}
}

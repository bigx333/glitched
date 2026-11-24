package engine_test

import (
	"strings"
	"testing"

	"glitched/internal/api"
	"glitched/internal/engine"
)

// A child's report from a worktree of its own names its branch, for the
// parent to merge; one on the parent's own branch, or in its folder,
// names none.
func TestAWorktreeChildsReportNamesItsBranch(t *testing.T) {
	for _, tc := range []struct {
		name, child, parent string
		want                bool
	}{
		{"own worktree", "glitched/review-abc123", "", true},
		{"own worktree under a worktree", "glitched/review-abc123", "glitched/parent-def456", true},
		{"the parent's worktree", "glitched/parent-def456", "glitched/parent-def456", false},
		{"the parent's folder", "", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := threadIn(t, "", tc.parent)
			r := report(api.ReportDone, "Review", "Looks fine.")
			r.Branch = tc.child
			s, ok := started(f.see(r))
			if !ok {
				t.Fatal("the report started no turn")
			}
			if got := strings.Contains(s.Text, `branch="`+tc.child+`"`); got != tc.want {
				t.Errorf("the wake %q names the branch: %v, want %v", s.Text, got, tc.want)
			}
		})
	}
}

// A delete that removes worktrees leaves the removal of the thread's
// own as an effect, after its agent's close; a plain delete, or one of a
// thread in its project's folder, leaves none.
func TestADeleteThatRemovesTheWorktreeLeavesItsRemovalAsAnEffect(t *testing.T) {
	removal := func(effects []engine.Effect) (engine.RemoveWorktree, int) {
		for i, e := range effects {
			if r, ok := e.(engine.RemoveWorktree); ok {
				return r, i
			}
		}
		return engine.RemoveWorktree{}, -1
	}
	in := threadIn(t, "/wt/x/1", "glitched/x-1")
	_, effects, err := in.submit(api.ThreadDelete{RemoveWorktree: true})
	r, i := removal(effects)
	if err != nil {
		t.Fatal(err)
	}
	if i < 0 || r != (engine.RemoveWorktree{Workspace: "/wt/x/1", Project: "/src/app", Branch: "glitched/x-1"}) {
		t.Errorf("removal %+v at %d, want the thread's worktree", r, i)
	}
	kept := threadIn(t, "/wt/x/2", "glitched/x-2")
	if _, effects, _ := kept.submit(api.ThreadDelete{}); func() bool { _, i := removal(effects); return i >= 0 }() {
		t.Errorf("a plain delete removes the worktree")
	}
	folder := threadIn(t, "/src/app", "")
	if _, effects, _ := folder.submit(api.ThreadDelete{RemoveWorktree: true}); func() bool { _, i := removal(effects); return i >= 0 }() {
		t.Errorf("a thread in its project's folder removes a worktree")
	}
}

// threadIn is a thread of the user's in workspace, on branch in a worktree
// of /src/app when branch isn't "".
func threadIn(t *testing.T, workspace, branch string) *thread {
	t.Helper()
	project := ""
	if branch != "" {
		project = "/src/app"
	}
	evs, _, err := engine.Decide(engine.Thread{}, api.Command{ID: api.NewID(), Target: api.NewID(), Body: api.ThreadCreate{
		Title: "t", Workspace: workspace, Project: project, Branch: branch,
	}}, now)
	if err != nil {
		t.Fatal(err)
	}
	f := &thread{t: t}
	f.fold(evs)
	return f
}

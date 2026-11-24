package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"glitched/internal/daemon"
	"glitched/internal/provider"
	"glitched/internal/provider/fake"
)

// branching rewinds natively: at once, as Codex forks its thread, or,
// with at, only at the thread's next run from the branch at names, as
// Claude Code does.
type branching struct {
	provider.Adapter
	at string
}

func (b branching) Rewind(_ context.Context, spec provider.RewindSpec) (provider.Rewind, error) {
	return provider.Rewind{Session: "branch-" + spec.Revert.String(), Native: true, At: b.at}, nil
}

// rewoundAt760 is a thread in a worktree of its own whose second turn
// came after notes.txt was written by hand, its agent ad's, rewound to
// its first turn with its files, in the window at its narrowest.
func rewoundAt760(t *testing.T, ad provider.Adapter) *harness {
	t.Helper()
	h := startWith(t, tempDir(t), func(c *daemon.Config) { c.Providers = daemon.FakeProviders(ad) })
	h.tt.SetSize(760, 788)
	h.a.startIn(draft{env: h.local(), workspace: gitWorkspace(t)})
	h.tt.Frame()
	h.a.draft = "Write the notes"
	h.click("Workspace")
	h.tt.Advance(time.Second)
	if err := h.tt.ChooseMenuItem("New worktree"); err != nil {
		t.Fatal(err)
	}
	h.tt.Advance(time.Second)
	h.click("Send")
	h.until("the first turn's checkpoint", func() bool {
		th := h.a.current()
		return th.View.Branch != "" && !th.View.Live() && th.Transcript != nil && len(th.Transcript.Checkpoints) == 2
	})
	if err := os.WriteFile(filepath.Join(h.a.current().View.Workspace, "notes.txt"), []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.a.draft = "Keep going"
	h.click("Send")
	h.until("the second turn's checkpoint", func() bool {
		th := h.a.current()
		return !th.View.Live() && th.Transcript != nil && len(th.Transcript.Checkpoints) == 3
	})
	h.tt.Advance(time.Second)
	h.click("No file changes")
	h.tt.Advance(time.Second)
	if err := h.tt.ChooseMenuItem("Rewind conversation and files to here"); err != nil {
		t.Fatal(err)
	}
	h.tt.Advance(time.Second)
	h.click("Rewind")
	h.until("the rewind's row", func() bool { h.tt.Frame(); return h.tt.HasText("Rewound the conversation and restored 1 file") })
	h.tt.Advance(time.Second)
	return h
}

// A rewind's Undo says before the click whether the conversation comes
// back as the agent's own or handed to a new one: native where the agent
// forked it at once, as Codex does, and portable where it can't fork it,
// or would only at its next run, as Claude Code does.
func TestARewindsUndoSaysBeforeTheClickWhetherItIsPortable(t *testing.T) {
	for name, tc := range map[string]struct {
		at, rewound, want string
		native            bool
	}{
		"an agent that can't fork":            {rewound: "Portable", want: "Portable: "},
		"an agent that forks at its next run": {native: true, at: "branch", rewound: "Native", want: "Portable: "},
		"an agent that forks at once":         {native: true, rewound: "Native", want: "Native: "},
	} {
		t.Run(name, func(t *testing.T) {
			var ad provider.Adapter = fake.New(fake.Turn(false))
			if tc.native {
				ad = branching{ad, tc.at}
			}
			h := rewoundAt760(t, ad)
			if !h.tt.HasText(tc.rewound) {
				t.Fatalf("the rewind's row reads %q; want it %s", h.tt.Texts(), tc.rewound)
			}
			undo, ok := h.tt.Access("Undo")
			if !ok {
				t.Fatalf("no Undo on the rewind's row: %q", h.tt.Texts())
			}
			if !strings.HasPrefix(undo.Description, tc.want) {
				t.Fatalf("the Undo's tooltip is %q; want it to start %q", undo.Description, tc.want)
			}
		})
	}
}

// At the window's narrowest, the row after an Undo of a rewind shows all
// of its sentence, its badge and its Undo files only: the sentence wraps
// rather than cut off what came back.
func TestAnUndoRowShowsItsWholeSentenceAtTheNarrowestWindow(t *testing.T) {
	h := rewoundAt760(t, fake.New(fake.Turn(false)))
	h.click("Undo")
	sentence := "Brought back the conversation and put back 1 file"
	h.until("the undo's row", func() bool { h.tt.Frame(); return h.tt.HasText(sentence) })
	h.tt.Advance(time.Second)
	for _, s := range []struct {
		text string
		size float32
	}{{sentence, 11.5}, {"Undo files only", 11.5}} {
		box, ok := h.tt.Find(s.text)
		if !ok {
			t.Fatalf("no %q in %q", s.text, h.tt.Texts())
		}
		whole := wholeBox(s.text, s.size, box.W)
		if whole.W > box.W+0.5 || whole.H > box.H+0.5 {
			t.Errorf("%q takes %.1f×%.1f shown whole at its box's width, but its box is %.1f×%.1f: it's cut off", s.text, whole.W, whole.H, box.W, box.H)
		}
	}
	var badge ui.AccessInfo
	for _, a := range h.tt.AccessAll() {
		if a.Name == "Portable" {
			badge = a
		}
	}
	if natural := wholeBox("Portable", 10.5, 0); badge.Bounds.W < natural.W {
		t.Errorf("the undo's badge is %.1f wide, narrower than its word's %.1f", badge.Bounds.W, natural.W)
	}
}

// wholeBox is the box text takes at size, all of it shown, wrapping at
// width, or on one line where width is 0, as a window draws it.
func wholeBox(text string, size, width float32) ui.Rect {
	tt := ui.NewTester(func(c *ui.Context) {
		box := ui.Column(c).AlignItems(ui.Start)
		if width > 0 {
			box.Width(width)
		}
		box.Children(func() { ui.Text(c, text).FontSize(size).TextAlign(ui.Center) })
	}, 2000, 400)
	tt.Frame()
	r, _ := tt.Find(text)
	return r
}

package main

import (
	"fmt"
	"math"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/egoist/mygo/plugins/glass"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
)

const (
	contentWidth = 800
	headerHeight = 52
	// composerRoom is the room left under the timeline for the composer
	// floating over it.
	composerRoom = 200
)

func (a *app) chatPage(c *ui.Context) {
	p, t := a.current()
	// The thread open is read, as its reply comes in.
	t.unread = false
	// The layout makes room for a panel at once, so the chat rewraps
	// once; the panel itself slides a little in from its edge as it fades.
	panel := func(x, y float32) ui.ElementTransition {
		if a.panelsJump {
			return ui.ElementTransition{}
		}
		return slideFrom(x, y)
	}
	ui.Row(c).Fill().AlignItems(ui.Stretch).Children(func() {
		ui.Column(c).Key("chat").Grow(1).Shrink(1).MinWidth(360).Children(func() {
			pane := ui.Box(c).Grow(1)
			a.windBackground(c, pane)
			pane.Children(func() {
				a.timeline(c, t)
				// What scrolls under the header frosts, and fades out
				// under the composer.
				ui.Box(c).Absolute().Top(0).Left(0).Right(0).Height(headerHeight).PassThrough().
					Material(glass.ScrollEdge{Hard: true, Background: palette.bg})
				ui.Box(c).Absolute().Bottom(0).Left(0).Right(0).Height(composerRoom - 40).PassThrough().
					Material(glass.ScrollEdge{Bottom: true, Background: palette.bg})
				a.header(c, p, t)
				a.composer(c, t)
			})
			if a.showTerminal {
				ui.Column(c).Key("terminal").Height(220).Transition(panel(0, 16)).Children(func() { a.terminal(c, p) })
			}
		})
		if a.showDiff {
			ui.Column(c).Key("diff").Width(a.diffWidth()).Transition(panel(16, 0)).Children(func() { a.diffPanel(c, t) })
		}
	})
}

// windBackground draws the wind's field of glyphs behind the chat: the
// pointer warms the air along its path and a click blows a ripple, while
// the chat's own controls still take them; typing keeps it blowing.
func (a *app) windBackground(c *ui.Context, pane ui.Element) {
	if !a.settings.wind {
		return
	}
	a.wind.SetStill(c.Preferences().ReduceMotion)
	if a.draft != a.typed {
		a.typed = a.draft
		a.wind.Wake()
	}
	dark := c.Theme().Dark
	pane.Draw(func(p *ui.Painter, r ui.Rect) { a.wind.Draw(p, r, dark) })
	pane.HandleInput(func(ev ui.InputEvent) bool {
		switch ev.Kind {
		case ui.InputPointerMove:
			// A frame, to wake the texture as it rests.
			return a.wind.Move(ev.X, ev.Y)
		case ui.InputPointerDown:
			if ev.Button == 0 {
				a.wind.Tap(ev.X, ev.Y)
			}
		}
		return false
	})
}

// ambient is a faint glow of the accent at the top of the window, which
// the glass of the sidebar and the composer frost and bend.
func (a *app) ambient(c *ui.Context) {
	ui.Box(c).Absolute().Top(0).Left(0).Right(0).Height(420).PassThrough().LinearGradient(ui.LinearGradient{
		Stops: []ui.GradientStop{
			{Color: palette.accent.Alpha(0.16), At: 0},
			{Color: palette.accent.Alpha(0.04), At: 0.55},
			{Color: palette.accent.Alpha(0), At: 1},
		},
		Angle: 180,
		Oklab: true,
	})
}

func (a *app) header(c *ui.Context, p *project, t *thread) {
	bar := c.TitleBar()
	// Clear of the traffic lights as the sidebar slides away from them.
	left := 16 + bar.Left*(1-a.sidebarOpen)
	ui.Row(c).Absolute().Top(0).Left(0).Right(0).Height(max(bar.Height, headerHeight)).
		Padding(0, 12, 0, left).Gap(8).AlignItems(ui.Center).DragWindow().Children(func() {
		if !a.showSidebar {
			if iconButton(c, "show-sidebar", icPanelLeft, "Show sidebar  ⌘B", false).Clicked() {
				a.toggleSidebar(false)
			}
		}
		ui.Text(c, t.title).FontWeight(600).SingleLine().Shrink(1)
		pill(c, p.name)
		ui.Spacer(c)

		a.glassButton(c, "open").Children(func() {
			ui.Icon(c, icCode).FontSize(14)
			ui.Text(c, "Open").SingleLine()
			ui.Icon(c, icChevronD).FontSize(11)
		}).Menu(func(m *ui.Menu) {
			m.Item("Cursor")
			m.Item("VS Code")
			m.Item("Zed")
			m.Separator()
			m.Item("Finder")
		})
		if a.glassButton(c, "commit").Children(func() {
			ui.Icon(c, icUpload).FontSize(14)
			ui.Text(c, "Commit & push").SingleLine()
		}).Clicked() {
			c.AddToast(ui.Toast{Title: "Pushed to origin", Description: t.branch + " · 2 files, +23 −2"})
		}
		ui.Row(c).Padding(2).Gap(2).Radius(10).Children(func() {
			if iconButton(c, "terminal", icTerminal, "Toggle terminal  ⌘J", a.showTerminal).Clicked() {
				a.showTerminal, a.panelsJump = !a.showTerminal, false
			}
			if iconButton(c, "diff", icDiff, "Toggle diff  ⌘D", a.showDiff).Clicked() {
				a.showDiff, a.panelsJump = !a.showDiff, false
			}
		})
	})
}

// glassButton is a pill of interactive glass, which swells as it's pressed.
func (a *app) glassButton(c *ui.Context, key any) ui.Element {
	b := ui.ButtonBase(c.Key(key)).Gap(6).AlignItems(ui.Center).Padding(6, 12).Radius(999).
		TextColor(palette.text).Cursor(ui.CursorPointer)
	return a.pane(b, glass.Glass{Interactive: true})
}

func pill(c *ui.Context, label string) ui.Element {
	return ui.Text(c, label).FontSize(11).TextColor(palette.muted).SingleLine().
		Padding(2, 7).Radius(999).Border(1, palette.border)
}

func (a *app) timeline(c *ui.Context, t *thread) {
	// Follow the end while the user is there, as a reply streams in.
	if a.scroll.Y >= a.scroll.MaxY-48 {
		a.scroll.Y = math.MaxFloat32
	}
	ui.Scroll(c).Key(t.id).TrackScroll(&a.scroll).Fill().AlignItems(ui.Center).
		Padding(headerHeight+20, 24, composerRoom).ScrollbarInsets(headerHeight, 0, composerRoom-40, 0).Children(func() {
		ui.Column(c).WidthPercent(100).MaxWidth(contentWidth).Gap(22).Children(func() {
			if len(t.entries) == 0 {
				a.emptyState(c)
			}
			for i, e := range t.entries {
				a.entry(c, t, i, e)
			}
		})
	})
}

func (a *app) emptyState(c *ui.Context) {
	ui.Column(c).Key("empty").Center().Gap(12).Padding(140, 0, 0).Children(func() {
		ui.Icon(c, icSparkles).FontSize(30).TextColor(palette.accent)
		ui.Text(c, "What should we build?").FontSize(18).FontWeight(600)
		ui.Text(c, "Send a message to start the conversation.").TextColor(palette.muted)
	})
}

func (a *app) entry(c *ui.Context, t *thread, i int, e entry) {
	key := fmt.Sprintf("%s/%d", t.id, i)
	switch e.kind {
	case entryUser:
		ui.Row(c).Key(key).Transition(riseIn).Children(func() {
			ui.Spacer(c)
			ui.Text(c, e.text).Selectable().LineHeight(1.5).Padding(10, 14).Radius(16, 16, 4, 16).
				Background(palette.surface).Border(1, palette.border).MaxWidthPercent(80)
		})
	case entryAssistant:
		ui.Column(c).Key(key).LineHeight(1.55).Transition(riseIn).Children(func() {
			if e.streaming {
				markdown.Stream(c, e.text, &a.md)
			} else {
				markdown.View(c, e.text, &a.md)
			}
		})
	case entryWork:
		a.workLog(c, key, e)
	case entryChanges:
		a.changes(c, key, e)
	}
}

func (a *app) workLog(c *ui.Context, key string, e entry) {
	working := strings.HasSuffix(e.took, "…")
	open := !a.collapsed[key]
	ui.Column(c).Key(key).Radius(12).Border(1, palette.border).Background(palette.bg.Alpha(0.6)).Clip().
		Transition(riseIn).Children(func() {
		if ghost(c, "head", false).Radius(0).Padding(8, 12).Children(func() {
			if working {
				statusMark(c, statusWorking)
			}
			ui.Text(c, e.took).FontSize(12).Grow(1)
			ui.Textf(c, "%d steps", len(e.work)).FontSize(11).TextColor(palette.faint)
			chevron(c, open)
		}).Clicked() {
			a.collapsed[key] = open
		}
		if !open {
			return
		}
		ui.Column(c).Key("steps").Padding(6, 12, 10).Gap(6).BorderWidth(1, 0, 0, 0).BorderColor(palette.border).
			ClipY().Transition(fold).Children(func() {
			for i, w := range e.work {
				// Each step the agent takes unfolds below the last.
				ui.Row(c).Key(i).Gap(8).AlignItems(ui.Center).ClipY().Transition(fold).Children(func() {
					icon := icFile
					switch w.kind {
					case "command":
						icon = icTerminal
					case "edit":
						icon = icPencil
					case "search":
						icon = icSearch
					}
					ui.Icon(c, icon).FontSize(12).TextColor(palette.faint)
					ui.Text(c, w.label).FontSize(12).TextColor(palette.muted).SingleLine().Width(64)
					ui.Text(c, w.detail).FontSize(12).Font("monospace").SingleLine().Shrink(1)
					counts(c, w.add, w.del)
				})
			}
		})
	})
}

func counts(c *ui.Context, add, del int) {
	if add > 0 {
		ui.Textf(c, "+%d", add).FontSize(11.5).Font("monospace").TextColor(palette.green)
	}
	if del > 0 {
		ui.Textf(c, "−%d", del).FontSize(11.5).Font("monospace").TextColor(palette.red)
	}
}

func (a *app) changes(c *ui.Context, key string, e entry) {
	// The counts are the diff's, as it stands after any review.
	add, del := 0, 0
	for _, f := range e.files {
		fa, fd := a.diffStats(f.path)
		add, del = add+fa, del+fd
	}
	ui.Column(c).Key(key).Radius(12).Border(1, palette.border).Background(palette.surface).Transition(riseIn).Children(func() {
		ui.Row(c).Padding(8, 8, 8, 12).Gap(8).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(palette.border).Children(func() {
			ui.Textf(c, "%d files changed", len(e.files)).FontSize(12).FontWeight(500)
			counts(c, add, del)
			ui.Spacer(c)
			if ghost(c, "view-diff", false).Children(func() {
				ui.Icon(c, icEye).FontSize(13)
				ui.Text(c, "View diff").FontSize(12)
			}).Clicked() {
				a.showDiff = true
			}
		})
		ui.Column(c).Padding(4).Children(func() {
			for i, f := range e.files {
				if ghost(c, f.path, false).Padding(5, 8).Children(func() {
					ui.Icon(c, icFile).FontSize(12).TextColor(palette.faint)
					ui.Text(c, f.path).FontSize(12).Font("monospace").SingleLine().Grow(1).Shrink(1).TextColor(palette.text)
					fa, fd := a.diffStats(f.path)
					counts(c, fa, fd)
				}).Clicked() {
					a.showDiff, a.diffFile = true, i
				}
			}
		})
	})
}

func (a *app) composer(c *ui.Context, t *thread) {
	ui.Column(c).Absolute().Bottom(0).Left(0).Right(0).AlignItems(ui.Center).Padding(0, 24, 14).Children(func() {
		ui.Column(c).WidthPercent(100).MaxWidth(contentWidth).Gap(8).Children(func() {
			card := ui.Column(c).Key("composer").Radius(20)
			if card.FocusWithin() {
				card.Shadow(0, 0, 0, 3, palette.accent.Alpha(0.22))
			}
			a.accessEffects(card)
			a.pane(card, glass.Glass{}).Shadow(0, 12, 32, 0, ui.RGBA(0, 0, 0, 0.28)).
				Transition(ui.ElementTransition{Size: true, Duration: 150 * time.Millisecond, Ease: easeOut}).Children(func() {
				ui.TextArea(c.Key("draft"), &a.draft).
					Placeholder("Ask anything, @tag files/folders, or use / to show available commands").
					Lines(2, 10).Background(ui.Transparent).Border(0, ui.Transparent).FocusRing(false).
					Padding(14, 16, 4).AutoFocus()
				ui.Row(c).Padding(6, 8, 8).Gap(2).AlignItems(ui.Center).Children(func() {
					a.composerControls(c)
					ui.Spacer(c)
					iconButton(c, "attach", icPaperclip, "Attach", false)
					a.sendButton(c, t)
				})
			})
			ui.Row(c).Gap(4).AlignItems(ui.Center).Padding(0, 6).Children(func() {
				ghost(c, "env", false).Padding(3, 6).Children(func() {
					ui.Icon(c, icFolder).FontSize(12)
					ui.Text(c, "Local").FontSize(11.5)
					ui.Icon(c, icChevronD).FontSize(10)
				}).Menu(func(m *ui.Menu) {
					m.Item("Local").Checked(true)
					m.Item("New worktree")
				})
				ui.Spacer(c)
				ghost(c, "branch", false).Padding(3, 6).Children(func() {
					ui.Icon(c, icBranch).FontSize(12)
					ui.Text(c, t.branch).FontSize(11.5).Font("monospace")
					ui.Icon(c, icChevronD).FontSize(10)
				}).Menu(func(m *ui.Menu) {
					for _, b := range []string{t.branch, "main", "dev"} {
						m.Item(b).Checked(b == t.branch)
					}
				})
			})
		})
	})
}

// sendButton presses in as it's pressed; its color says whether there is
// anything to send. While the agent works, it stops it.
func (a *app) sendButton(c *ui.Context, t *thread) {
	working := t.status == statusWorking
	empty := strings.TrimSpace(a.draft) == ""
	send := ui.ButtonBase(c.Key("send")).Label("Send").Size(30, 30).Radius(15).Center().
		Cursor(ui.CursorPointer).Disabled(empty && !working)
	bg := palette.text
	if empty && !working {
		bg = palette.faint.Alpha(0.5)
	}
	send.Background(send.AnimateColor("bg", bg, ui.Animation{Duration: 150 * time.Millisecond, Ease: ease})).TextColor(palette.bg)
	pose := ui.Pose{}
	if send.Pressed() {
		pose.Scale = 0.97
	}
	send.AnimatePose(pose, press)
	send.Children(func() {
		if working {
			ui.Box(c).Size(10, 10).Radius(2).Background(palette.bg)
		} else {
			ui.Icon(c, icArrowUp).FontSize(16)
		}
	})
	if send.Clicked() || c.Shortcut(ui.Cmd, ui.KeyEnter) {
		if working {
			t.status = statusIdle
			t.stop.Store(true)
		} else {
			a.send(t)
		}
	}
}

func (a *app) composerControls(c *ui.Context) {
	a.modelPicker(c)
	ui.Box(c).Size(1, 16).Background(palette.border).Margin(0, 4)

	a.accessPicker(c)
}

var replyCount int

// send appends the draft, then plays a canned turn: the agent's steps
// arrive one by one, then its reply streams in. There is no agent.
func (a *app) send(t *thread) {
	text := strings.TrimSpace(a.draft)
	if text == "" {
		return
	}
	if len(t.entries) == 0 {
		t.title = text
		if len(t.title) > 48 {
			t.title = t.title[:48] + "…"
		}
	}
	a.draft = ""
	a.scroll.Y = math.MaxFloat32
	t.entries = append(t.entries, entry{kind: entryUser, text: text})
	// The thread goes to Working while the agent runs, out of the
	// snoozed and settled ones.
	t.status, t.age, t.settled, t.snoozed = statusWorking, "now", false, ""
	t.stop.Store(false)

	s := scripts[replyCount%len(scripts)]
	replyCount++
	t.entries = append(t.entries, entry{kind: entryWork, took: "Working…"})
	work := len(t.entries) - 1
	if a.win == nil { // in tests, all at once
		t.entries[work].work, t.entries[work].took = s.steps, "Worked for 3s"
		t.entries = append(t.entries, entry{kind: entryAssistant, text: s.reply})
		t.finish()
		return
	}
	go func() {
		started := time.Now()
		for _, step := range s.steps {
			if t.stop.Load() {
				break
			}
			time.Sleep(time.Duration(500+rand.IntN(500)) * time.Millisecond)
			a.win.Update(func() { t.entries[work].work = append(t.entries[work].work, step) })
		}
		time.Sleep(400 * time.Millisecond)
		reply := 0
		a.win.Update(func() {
			t.entries[work].took = fmt.Sprintf("Worked for %ds", int(time.Since(started).Seconds()))
			t.entries = append(t.entries, entry{kind: entryAssistant, streaming: true})
			reply = len(t.entries) - 1
		})
		for i := 0; i < len(s.reply); {
			n := min(len(s.reply)-i, 2+rand.IntN(6))
			for n < len(s.reply)-i && s.reply[i+n]&0xC0 == 0x80 {
				n++
			}
			i += n
			chunk := s.reply[:i]
			time.Sleep(time.Duration(10+rand.IntN(25)) * time.Millisecond)
			if t.stop.Load() {
				break
			}
			a.win.Update(func() { t.entries[reply].text = chunk })
		}
		a.win.Update(func() {
			t.entries[reply].streaming = false
			if t.status == statusWorking {
				t.finish()
			}
		})
	}()
}

func (a *app) terminal(c *ui.Context, p *project) {
	s, err := a.shellFor(p)
	title := "zsh"
	if s != nil {
		title = s.Title()
	}
	ui.Column(c).Height(220).Background(ui.Hex("#08080a")).BorderWidth(1, 0, 0, 0).BorderColor(palette.border).Children(func() {
		ui.Row(c).Padding(4, 8, 4, 12).Gap(6).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(ui.RGBA(255, 255, 255, 0.08)).Children(func() {
			ui.Icon(c, icTerminal).FontSize(12).TextColor(darkColors.muted)
			ui.Text(c, title).FontSize(11.5).TextColor(darkColors.muted).SingleLine().Shrink(1)
			ui.Spacer(c)
			if iconButton(c, "close-terminal", icX, "Close terminal", false).Padding(3).Clicked() {
				a.showTerminal = false
			}
		})
		a.terminalBody(c, s, err)
	})
}

// cannedTerminal is the drawer's output where no shell runs, as in tests.
func (a *app) cannedTerminal(c *ui.Context) {
	ui.Scroll(c).Grow(1).Padding(8, 12).Gap(2).Font("monospace").FontSize(12).Children(func() {
		for _, l := range []struct {
			text string
			col  ui.Color
		}{
			{"~/Projects/t3code (t3code/sidebar-sort) $ bun run typecheck", darkColors.text},
			{"$ tsc --noEmit -p apps/web", darkColors.muted},
			{"✓ No type errors", darkColors.green},
			{"~/Projects/t3code (t3code/sidebar-sort) $ bun run test -- Sidebar", darkColors.text},
			{" ✓ Sidebar > sorts threads by updatedAt (4ms)", darkColors.green},
			{" ✓ Sidebar > collapses a project (2ms)", darkColors.green},
			{" Test Files  1 passed (1)", darkColors.muted},
		} {
			ui.Text(c, l.text).TextColor(l.col).NoWrap()
		}
		ui.Row(c).Children(func() {
			ui.Text(c, "~/Projects/t3code (t3code/sidebar-sort) $ ").TextColor(darkColors.text).NoWrap()
			caret := ui.Box(c).Size(7, 14)
			caret.Background(darkColors.text).Opacity(caret.Loop("blink", time.Second, blink))
		})
	})
}

func blink(t float32) float32 {
	if t < 0.5 {
		return 1
	}
	return 0
}

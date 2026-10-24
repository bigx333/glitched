package main

import (
	"fmt"
	"math/rand/v2"
	"slices"
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
		chat := ui.Column(c).Key("chat").Grow(1).Shrink(1).MinWidth(minChatWidth)
		a.chatWidth = chat.Bounds().W
		chat.Children(func() {
			// The header is glass, the window's vibrancy as the sidebar's;
			// the chat is below it, on the background, and doesn't scroll
			// under it.
			a.header(c, p, t)
			pane := a.contentCard(ui.Box(c).Grow(1))
			a.windBackground(c, pane)
			pane.Children(func() {
				if a.windDraw != nil && a.settings.wind {
					ui.Box(c).Key("wind").Absolute().Top(0).Left(0).Right(0).Bottom(0).PassThrough().Draw(a.windDraw)
				}
				a.ambient(c)
				a.timeline(c, t)
				// What scrolls fades out under the composer.
				ui.Box(c).Absolute().Bottom(0).Left(0).Right(0).Height(composerRoom - 40).PassThrough().
					Material(glass.ScrollEdge{Bottom: true, Background: palette.bg})
				// Over the edge, which would take its clicks.
				a.jumpToLatest(c, a.listFor(t))
				a.approvalKeys(c, t)
				a.composer(c, t)
			})
			if a.showTerminal {
				ui.Column(c).Key("terminal").Height(220).Transition(panel(0, 16)).Children(func() { a.terminal(c, p) })
			}
		})
		switch {
		case a.showDiff:
			ui.Column(c).Key("diff").Width(a.diffWidth()).Transition(panel(16, 0)).Children(func() { a.diffPanel(c, t) })
		case a.showPreview:
			ui.Column(c).Key("preview").Width(a.previewWidth()).Transition(panel(16, 0)).Children(func() { a.previewPanel(c) })
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
	// Drawn by a child, which the pane's rounded corner clips, as it
	// would not its own drawing.
	a.windDraw = func(p *ui.Painter, r ui.Rect) { a.wind.Draw(p, r, dark) }
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
	// Narrow, beside a panel, the header keeps to icons, and never draws
	// past the chat's edge.
	narrow := a.chatWidth < 700
	head := ui.Row(c).Height(max(bar.Height, headerHeight)).Shrink(0).
		Padding(0, 12, 0, left).Gap(8).AlignItems(ui.Center).ClipX()
	head.DragWindow().Children(func() {
		if !a.showSidebar {
			if iconButton(c, "show-sidebar", icPanelLeft, "Show sidebar  ⌘B", false).Clicked() {
				a.toggleSidebar(false)
			}
		}
		ui.Text(c, t.title).FontWeight(600).SingleLine().Shrink(1).MinWidth(80)
		if !narrow {
			pill(c, p.name).Shrink(0)
		}
		ui.Spacer(c)

		headerButton(c, "open").Shrink(0).Label("Open in").Tooltip("Open in").Children(func() {
			ui.Icon(c, icCode).FontSize(14)
			if !narrow {
				ui.Text(c, "Open").SingleLine()
			}
			ui.Icon(c, icChevronD).FontSize(11)
		}).Menu(func(m *ui.Menu) {
			m.Item("Cursor")
			m.Item("VS Code")
			m.Item("Zed")
			m.Separator()
			m.Item("Finder")
		})
		if headerButton(c, "commit").Shrink(0).Label("Commit & push").Tooltip("Commit & push").Children(func() {
			ui.Icon(c, icUpload).FontSize(14)
			if !narrow {
				ui.Text(c, "Commit & push").SingleLine()
			}
		}).Clicked() {
			c.AddToast(ui.Toast{Title: "Pushed to origin", Description: t.branch + " · 2 files, +23 −2"})
		}
		ui.Row(c).Padding(2).Gap(2).Radius(10).Shrink(0).Children(func() {
			if iconButton(c, "terminal", icTerminal, "Toggle terminal  ⌘J", a.showTerminal).Clicked() {
				a.showTerminal, a.panelsJump = !a.showTerminal, false
			}
			ui.Box(c).Children(func() {
				if iconButton(c, "diff", icDiff, "Toggle diff  ⌘D", a.showDiff).Clicked() {
					a.showDiff, a.panelsJump = !a.showDiff, false
					if a.showDiff {
						a.showPreview = false
					}
				}
				if n := len(t.changed()); n > 0 {
					ui.Textf(c, "%d", n).Absolute().Top(1).Right(1).FontSize(9).FontWeight(600).LineHeight(1.4).
						Padding(0, 4).Radius(999).Background(palette.accent).TextColor(ui.Hex("#ffffff")).PassThrough()
				}
			})
			if iconButton(c, "preview", icGlobe, "Toggle preview  ⇧⌘P", a.showPreview).Clicked() {
				a.togglePreview()
				a.panelsJump = false
			}
		})
	})
}

// headerButton is a button of the header's glass bar: a hairline pill,
// over the window's vibrancy rather than glass over the chat.
func headerButton(c *ui.Context, key any) ui.Element {
	return ghost(c, key, false).Padding(5, 11).Radius(999).Border(1, palette.border).TextColor(palette.text)
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

// listFor is the thread's list of entries, kept per thread so that each
// keeps where it was scrolled to.
func (a *app) listFor(t *thread) *ui.ListState {
	l := a.lists[t.id]
	if l == nil {
		l = &ui.ListState{}
		a.lists[t.id] = l
	}
	return l
}

// timeline lists the thread's entries, as MyGo's markdown-chat does: the
// list builds only those in view, starts at the end and follows it as a
// reply streams in, gliding, until the user scrolls up; scrolling back to
// the end follows it again. Each entry is in the middle of a row as wide
// as the pane, so that the wheel scrolls it wherever the pointer is.
func (a *app) timeline(c *ui.Context, t *thread) {
	l := a.listFor(t)
	l.Key = func(i int) any { return fmt.Sprintf("%s/%d", t.id, i) }
	l.FollowEnd = true
	l.FollowAnimation = &ui.Smooth
	// The frame shows the entries as they were at its start: an action on
	// one can cut the thread short while the rest are built.
	entries := t.entries
	ui.List(c.Key(t.id), l, len(entries), func(i int) {
		ui.Row(c).Justify(ui.Center).Children(func() {
			ui.Column(c).Grow(1).MaxWidth(contentWidth).Children(func() { a.entry(c, t, i, entries[i]) })
		})
	}).Fill().Padding(20, 24, composerRoom, markerColumnW+12).Gap(22).Justify(ui.End).
		ScrollbarInsets(0, 0, composerRoom-40, 0).Children(func() {
		if len(entries) == 0 {
			ui.Row(c).Justify(ui.Center).Children(func() { a.emptyState(c) })
		}
	})
	a.turnMarkers(c, t, l)
}

// jumpToLatest floats a pill over the composer while the user has
// scrolled up from the end, which takes them back.
func (a *app) jumpToLatest(c *ui.Context, l *ui.ListState) {
	if l.AtEnd() {
		return
	}
	// Just over the composer, compact as it is while scrolled up.
	ui.Row(c).Absolute().Left(0).Right(0).Bottom(80).Justify(ui.Center).PassThrough().Children(func() {
		b := a.glassButton(c, "jump").Padding(5, 12).Transition(riseIn)
		if b.Children(func() {
			ui.Icon(c, icArrowDown).FontSize(13)
			ui.Text(c, "Jump to latest").FontSize(12)
		}).Clicked() {
			l.ScrollToEnd()
		}
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
		row := ui.Row(c).Key(key).Transition(riseIn)
		row.Children(func() {
			ui.Spacer(c)
			ui.Column(c).AlignItems(ui.End).Gap(6).MaxWidthPercent(80).Children(func() {
				if len(e.attachments) > 0 {
					attachmentChips(c, e.attachments, nil)
				}
				if e.text != "" {
					ui.Text(c, e.text).Selectable().LineHeight(1.5).Padding(10, 14).Radius(16, 16, 4, 16).
						Background(palette.surface).Border(1, palette.border)
				}
			})
			a.messageActions(c, t, i, e, row.Hovered())
		})
	case entryAssistant:
		col := ui.Column(c).Key(key).LineHeight(1.55).Transition(riseIn)
		col.Children(func() {
			if e.streaming {
				markdown.Stream(c, e.text, &a.md)
			} else {
				markdown.View(c, e.text, &a.md)
			}
			a.messageActions(c, t, i, e, col.Hovered() && !e.streaming)
		})
	case entryWork:
		a.workLog(c, key, t, e, t.pausedFor(i))
	case entryChanges:
		a.changes(c, key, e)
	case entryApproval:
		a.approvalCard(c, key, t, e.approval)
	}
}

// workLog is the agent's steps of a turn, under a head that says how it
// went: still working, paused for an approval, or how long it took.
func (a *app) workLog(c *ui.Context, key string, t *thread, e entry, paused bool) {
	working := e.took == working
	open := !a.collapsed[key]
	ui.Column(c).Key(key).Radius(12).Border(1, palette.border).Background(palette.bg.Alpha(0.6)).Clip().
		Transition(riseIn).Children(func() {
		if ghost(c, "head", false).Radius(0).Padding(8, 12).Children(func() {
			switch {
			case paused:
				ui.Icon(c, icClock).FontSize(12).TextColor(palette.amber)
				ui.Text(c, "Paused for approval").FontSize(12).Grow(1)
			case working:
				statusMark(c, statusWorking)
				shimmerText(c, e.took, 12, palette.muted, palette.text)
			default:
				ui.Text(c, e.took).FontSize(12).Grow(1)
			}
			ui.Text(c, plural(len(e.work), "step")).FontSize(11).TextColor(palette.faint)
			chevron(c, open)
		}).Clicked() {
			a.collapsed[key] = open
		}
		if !open {
			return
		}
		ui.Column(c).Key("steps").Padding(6, 8, 10).Gap(2).BorderWidth(1, 0, 0, 0).BorderColor(palette.border).
			ClipY().Transition(fold).Children(func() {
			for i, w := range e.work {
				// Each step the agent takes unfolds below the last.
				a.step(c, t, i, w)
			}
		})
	})
}

// step is one of the agent's steps. A command that ran shows how it went,
// and what it printed when it failed. An edit of a file the diff shows
// opens it there.
func (a *app) step(c *ui.Context, t *thread, i int, w workItem) {
	icon := icFile
	switch w.kind {
	case "command":
		icon = icTerminal
	case "edit":
		icon = icPencil
	case "search":
		icon = icSearch
	}
	file := -1
	add, del := w.add, w.del
	if w.kind == "edit" {
		file = slices.IndexFunc(t.changed(), func(f fileChange) bool { return f.path == w.detail })
		if _, known := fileTexts[w.detail]; known {
			// The counts are the diff's own, where there is one.
			add, del = a.diffStats(w.detail)
		}
	}
	var row ui.Element
	if file >= 0 {
		row = ghost(c, i, false).Padding(2, 4).Radius(5).Gap(8).ClipY().Transition(fold)
	} else {
		row = ui.Row(c).Key(i).Padding(2, 4).Radius(5).Gap(8).AlignItems(ui.Center).ClipY().Transition(fold)
	}
	if row.Children(func() {
		ui.Icon(c, icon).FontSize(12).TextColor(palette.faint)
		ui.Text(c, w.label).FontSize(12).TextColor(palette.muted).SingleLine().Width(64)
		ui.Text(c, w.detail).FontSize(12).Font("monospace").SingleLine().Shrink(1).TextColor(palette.text)
		counts(c, add, del)
		ui.Spacer(c)
		switch {
		case w.kind == "command" && w.took != "":
			mark, color := "✓", palette.green
			if w.exit != 0 {
				mark, color = "✗", palette.red
			}
			ui.Textf(c, "%s %d", mark, w.exit).FontSize(11.5).Font("monospace").TextColor(color)
			ui.Textf(c, "· %s", w.took).FontSize(11.5).Font("monospace").TextColor(palette.faint)
		case file >= 0 && row.Hovered():
			ui.Text(c, "Open in diff").FontSize(11.5).TextColor(palette.muted)
			ui.Icon(c, icExternal).FontSize(11).TextColor(palette.muted)
		}
	}).Clicked() && file >= 0 {
		a.showDiff, a.showPreview, a.diffFile, a.panelsJump = true, false, file, false
	}
	if w.exit != 0 && w.output != "" {
		ui.Text(c, w.output).Selectable().FontSize(11.5).Font("monospace").LineHeight(1.5).TextColor(palette.muted).
			Margin(2, 0, 4, 24).Padding(6, 10).Radius(6).Background(palette.bg).Border(1, palette.border)
	}
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
			ui.Text(c, plural(len(e.files), "file")+" changed").FontSize(12).FontWeight(500)
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

// composerResize is how the composer grows and shrinks, compact while the
// user reads back up the thread.
var composerResize = ui.ElementTransition{Size: true, Duration: 250 * time.Millisecond, Ease: easeOut}

// composerCompact reports whether the composer is compact: away from the
// end of the thread, not clicked open, holding nothing.
func (a *app) composerCompact(t *thread) bool {
	if a.listFor(t).AtEnd() {
		a.composerOpened = false
		return false
	}
	return !a.composerOpened && strings.TrimSpace(a.draft) == "" && len(a.attachments) == 0
}

// compactWidth is the composer's width while compact.
const compactWidth = 520

func (a *app) composer(c *ui.Context, t *thread) {
	// While the user reads back up the thread, the composer steps out of
	// the way: narrower, one line, its controls folded away. It comes back
	// to its full size at the end of the thread, as it's clicked, and
	// whenever it holds a draft or attachments, so it never hides work.
	compact := a.composerCompact(t)
	width := float32(contentWidth)
	if compact {
		width = compactWidth
	}
	// The frame lets the pointer through, but for the composer in it, to
	// what is around it, as the pill taking the user to the end.
	ui.Column(c).Absolute().Bottom(0).Left(0).Right(0).AlignItems(ui.Center).Padding(0, 24, 14).PassThrough().Children(func() {
		ui.Column(c).Key("composer-frame").WidthPercent(100).MaxWidth(width).Gap(8).PassThrough().Transition(composerResize).Children(func() {
			card := ui.Column(c).Key("composer").Radius(20)
			if compact && card.Clicked() {
				a.composerOpened = true
			}
			if card.FocusWithin() {
				card.Shadow(0, 0, 0, 3, palette.accent.Alpha(0.22))
			}
			a.accessEffects(card)
			a.autocomplete(c, card)
			dragOver := a.dropZone(card)
			a.pane(card, glass.Glass{}).Shadow(0, 12, 32, 0, ui.RGBA(0, 0, 0, 0.28)).Clip().
				Transition(composerResize).Children(func() {
				workingGlow(c, card, t.status == statusWorking)
				if len(a.attachments) > 0 {
					ui.Column(c).Key("attachments").Padding(12, 12, 0).Children(func() {
						attachmentChips(c, a.attachments, func(i int) {
							a.attachments = append(a.attachments[:i:i], a.attachments[i+1:]...)
						})
					})
				}
				if compact {
					// A line to type a follow-up into, which a text area,
					// at least two lines high, can't be; the first key
					// typed brings the full composer back, its text with it.
					ui.TextInput(c.Key("draft-compact"), &a.draft).Placeholder("Ask a follow-up…").
						Background(ui.Transparent).Border(0, ui.Transparent).FocusRing(false).Padding(11, 18).
						OnPaste(a.onPaste)
					return
				}
				placeholder := "Ask anything, @tag files/folders, or use / to show available commands"
				if t.pending() != nil {
					placeholder = "Reply to steer, or answer the approval above"
				}
				ui.TextArea(c.Key("draft"), &a.draft).Placeholder(placeholder).
					Lines(2, 10).Background(ui.Transparent).Border(0, ui.Transparent).FocusRing(false).
					Padding(14, 16, 4).AutoFocus().OnPaste(a.onPaste).OnTextKey(a.suggestKey)
				ui.Row(c).Key("controls").Padding(6, 8, 8).Gap(2).AlignItems(ui.Center).ClipY().Transition(fold).Children(func() {
					a.composerControls(c)
					ui.Spacer(c)
					if iconButton(c, "attach", icPaperclip, "Attach files", false).Clicked() {
						a.pickFiles()
					}
					a.sendButton(c, t)
				})
				if dragOver {
					dropRing(c)
				}
			})
			if compact {
				return
			}
			ui.Row(c).Key("env-row").Gap(4).AlignItems(ui.Center).Padding(0, 6).ClipY().Transition(fold).Children(func() {
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
	empty := strings.TrimSpace(a.draft) == "" && len(a.attachments) == 0
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
			t.stopTurn()
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
	if text == "" && len(a.attachments) == 0 {
		return
	}
	if len(t.entries) == 0 && t.title == untitled {
		t.title = text
		if t.title == "" {
			t.title = a.attachments[0].name
		}
		if len(t.title) > 48 {
			t.title = t.title[:48] + "…"
		}
	}
	a.draft = ""
	a.listFor(t).ScrollToEnd()
	t.entries = append(t.entries, entry{kind: entryUser, text: text, attachments: a.attachments})
	a.attachments = nil
	a.run(t)
}

// run has the agent take its turn on the thread as it stands. The thread
// goes to Working while it does, out of the snoozed and settled ones. A
// message sent while an approval waits is the answer instead.
func (a *app) run(t *thread) {
	if ap := t.pending(); ap != nil {
		ap.decision = superseded
	}
	t.startTurn()
	a.play(t, scripts[replyCount%len(scripts)])
	replyCount++
}

// turnRunner is how a canned turn runs: start runs it, sleep waits
// between its steps, and update posts a change to the UI thread. The
// window's are a goroutine, time.Sleep and Window.Update. The tests'
// run the turn inline, on the UI thread, in no time.
type turnRunner struct {
	start  func(func())
	sleep  func(time.Duration)
	update func(func())
}

// inlineTurns plays a turn at once, where it's started.
var inlineTurns = &turnRunner{
	start:  func(fn func()) { fn() },
	sleep:  func(time.Duration) {},
	update: func(fn func()) { fn() },
}

// play runs a canned turn: the agent's steps arrive one by one, then its
// reply streams in. There is no agent. The turn is live until the thread
// moves on to another, or is stopped: after that it writes nothing, as
// the entries it knew may be cut.
func (a *app) play(t *thread, s script) {
	t.entries = append(t.entries, entry{kind: entryWork, took: working})
	work := len(t.entries) - 1
	id := t.turn.Add(1)
	live := func() bool { return t.turn.Load() == id }
	// post runs fn on the UI thread, while the turn is live there.
	post := func(fn func()) {
		a.turns.update(func() {
			if live() {
				fn()
			}
		})
	}
	a.turns.start(func() {
		started := time.Now()
		for _, step := range s.steps {
			a.turns.sleep(time.Duration(500+rand.IntN(500)) * time.Millisecond)
			if !live() {
				return
			}
			post(func() { t.entries[work].work = append(t.entries[work].work, step) })
		}
		a.turns.sleep(400 * time.Millisecond)
		reply := 0
		post(func() {
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
			a.turns.sleep(time.Duration(10+rand.IntN(25)) * time.Millisecond)
			if !live() {
				return
			}
			post(func() { t.entries[reply].text = chunk })
		}
		post(func() {
			t.entries[reply].streaming = false
			t.finish()
			a.notifyFinished(t)
		})
	})
}

func (a *app) terminal(c *ui.Context, p *project) {
	s, err := a.shellFor(p)
	title := "zsh"
	if s != nil {
		title = s.Title()
	}
	th := a.theme.terminal()
	ui.Column(c).Height(220).Background(th.Background).BorderWidth(1, 0, 0, 0).BorderColor(palette.border).Children(func() {
		ui.Row(c).Padding(4, 8, 4, 12).Gap(6).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(palette.border).Children(func() {
			ui.Icon(c, icTerminal).FontSize(12).TextColor(palette.muted)
			ui.Text(c, title).FontSize(11.5).TextColor(palette.muted).SingleLine().Shrink(1)
			ui.Spacer(c)
			if iconButton(c, "close-terminal", icX, "Close terminal", false).Padding(3).Clicked() {
				a.showTerminal = false
			}
		})
		a.terminalBody(c, s, err)
	})
}

// cannedTerminal is the drawer's output where no shell runs, as in tests,
// in the terminal's colors: its text, its dim text and its green.
func (a *app) cannedTerminal(c *ui.Context) {
	th := a.theme.terminal()
	text, dim, green := th.Foreground, th.Foreground.Alpha(0.6), th.Palette[2]
	ui.Scroll(c).Grow(1).Padding(8, 12).Gap(2).Font("monospace").FontSize(12).Children(func() {
		for _, l := range []struct {
			text string
			col  ui.Color
		}{
			{"~/Projects/t3code (t3code/sidebar-sort) $ bun run typecheck", text},
			{"$ tsc --noEmit -p apps/web", dim},
			{"✓ No type errors", green},
			{"~/Projects/t3code (t3code/sidebar-sort) $ bun run test -- Sidebar", text},
			{" ✓ Sidebar > sorts threads by updatedAt (4ms)", green},
			{" ✓ Sidebar > collapses a project (2ms)", green},
			{" Test Files  1 passed (1)", dim},
		} {
			ui.Text(c, l.text).TextColor(l.col).NoWrap()
		}
		ui.Row(c).Children(func() {
			ui.Text(c, "~/Projects/t3code (t3code/sidebar-sort) $ ").TextColor(text).NoWrap()
			caret := ui.Box(c).Size(7, 14)
			caret.Background(text).Opacity(caret.Loop("blink", time.Second, blink))
		})
	})
}

func blink(t float32) float32 {
	if t < 0.5 {
		return 1
	}
	return 0
}

// plural counts things: "1 step", "2 steps".
func plural(n int, what string) string {
	if n == 1 {
		return "1 " + what
	}
	return fmt.Sprintf("%d %ss", n, what)
}

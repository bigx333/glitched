package main

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/plugins/glass"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"

	"glitched/internal/api"
	"glitched/internal/client"
)

const (
	contentWidth = 800
	headerHeight = 52
	// composerRoom is the room left under the timeline for the composer
	// floating over it.
	composerRoom = 200
)

func (a *app) chatPage(c *ui.Context) {
	t := a.current()
	es := entriesOf(t)
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
			a.header(c, t)
			pane := a.contentCard(ui.Box(c).Grow(1))
			a.windBackground(c, pane)
			pane.Children(func() {
				if a.windDraw != nil && a.settings.wind {
					ui.Box(c).Key("wind").Absolute().Top(0).Left(0).Right(0).Bottom(0).PassThrough().Draw(a.windDraw)
				}
				a.ambient(c)
				a.timeline(c, t, es)
				// What scrolls fades out under the composer.
				ui.Box(c).Absolute().Bottom(0).Left(0).Right(0).Height(composerRoom - 40).PassThrough().
					Material(glass.ScrollEdge{Bottom: true, Background: palette.bg})
				// Over the edge, which would take its clicks.
				a.jumpToLatest(c, a.listFor(t.id()))
				a.composer(c, t)
			})
			// A server's workspace isn't here, for a shell or its changes.
			if a.showTerminal && t.local() {
				ui.Column(c).Key("terminal").Height(220).Transition(panel(0, 16)).Children(func() { a.terminal(c, t.View.Workspace) })
			}
		})
		switch {
		case a.showDiff && t.local():
			ui.Column(c).Key("diff").Width(a.diffWidth()).Transition(panel(16, 0)).Children(func() { a.diffPanel(c, es) })
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

func (a *app) header(c *ui.Context, t thread) {
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
		ui.Text(c, t.View.Title).FontWeight(600).SingleLine().Shrink(1).MinWidth(80)
		if !narrow {
			pill(c, filepath.Base(t.View.Workspace)).Shrink(0)
			if len(a.backend.Envs()) > 1 && t.env != nil {
				machinePill(c, t.env)
			}
		}
		ui.Spacer(c)
		if !t.local() {
			a.previewButton(c)
			return
		}

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
			c.AddToast(ui.Toast{Title: "Pushed to origin", Description: filepath.Base(t.View.Workspace) + " · 2 files, +23 −2"})
		}
		ui.Row(c).Padding(2).Gap(2).Radius(10).Shrink(0).Children(func() {
			if iconButton(c, "terminal", icTerminal, "Toggle terminal  ⌘J", a.showTerminal).Clicked() {
				a.showTerminal, a.panelsJump = !a.showTerminal, false
			}
			if iconButton(c, "diff", icDiff, "Toggle diff  ⌘D", a.showDiff).Clicked() {
				a.showDiff, a.panelsJump = !a.showDiff, false
				if a.showDiff {
					a.showPreview = false
				}
			}
			a.previewButton(c)
		})
	})
}

func (a *app) previewButton(c *ui.Context) {
	if iconButton(c, "preview", icGlobe, "Toggle preview  ⇧⌘P", a.showPreview).Clicked() {
		a.togglePreview()
		a.panelsJump = false
	}
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
func (a *app) listFor(id api.ID) *ui.ListState {
	l := a.lists[id]
	if l == nil {
		l = &ui.ListState{}
		a.lists[id] = l
	}
	return l
}

// timeline lists the thread's entries, as MyGo's markdown-chat does: the
// list builds only those in view, starts at the end and follows it as a
// reply streams in, gliding, until the user scrolls up; scrolling back to
// the end follows it again. Each entry is in the middle of a row as wide
// as the pane, so that the wheel scrolls it wherever the pointer is.
func (a *app) timeline(c *ui.Context, t thread, es []entry) {
	l := a.listFor(t.id())
	l.Key = func(i int) any { return es[i].key }
	l.FollowEnd = true
	l.FollowAnimation = &ui.Smooth
	ui.List(c.Key(t.id()), l, len(es), func(i int) {
		ui.Row(c).Justify(ui.Center).Children(func() {
			ui.Column(c).Grow(1).MaxWidth(contentWidth).Children(func() { a.entry(c, t, es[i]) })
		})
	}).Fill().Padding(20, 24, composerRoom, markerColumnW+12).Gap(22).Justify(ui.End).
		ScrollbarInsets(0, 0, composerRoom-40, 0).Children(func() {
		if len(es) == 0 {
			ui.Row(c).Justify(ui.Center).Children(func() { a.emptyState(c) })
		}
	})
	a.turnMarkers(c, es, l)
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

func (a *app) entry(c *ui.Context, t thread, e entry) {
	key := e.key
	switch e.kind {
	case entryUser:
		ui.Row(c).Key(key).Transition(riseIn).Children(func() {
			ui.Spacer(c)
			ui.Column(c).AlignItems(ui.End).Gap(6).MaxWidthPercent(80).Children(func() {
				ui.Text(c, e.text).Selectable().LineHeight(1.5).Padding(10, 14).Radius(16, 16, 4, 16).
					Background(palette.surface).Border(1, palette.border)
				if e.note != "" {
					a.messageNote(c, t, e)
				}
			})
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
	case entryRequest:
		a.requestCard(c, t, e)
	case entryFailed:
		ui.Row(c).Key(key).Gap(8).AlignItems(ui.Center).Padding(6, 6, 6, 14).Radius(12).
			Border(1, palette.red.Alpha(0.35)).Transition(riseIn).Children(func() {
			ui.Text(c, e.text).FontSize(12).TextColor(palette.red).Grow(1).Shrink(1)
			a.failedActions(c, t, e.failed)
		})
	}
}

// messageNote is under a message that hasn't simply been sent: queued,
// cancelled, or not sent.
func (a *app) messageNote(c *ui.Context, t thread, e entry) {
	ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
		col := palette.faint
		if e.failed.Err != nil {
			col = palette.red
		}
		ui.Text(c, e.note).FontSize(11.5).TextColor(col)
		if e.failed.Err != nil {
			a.failedActions(c, t, e.failed)
		}
	})
}

// failedActions are a failed command's buttons: Retry, unless sending it
// again can only fail the same way, and Dismiss.
func (a *app) failedActions(c *ui.Context, t thread, p client.Pending) {
	if !p.Terminal() && ghost(c, "retry", false).Padding(2, 8).Children(func() {
		ui.Icon(c, icRotate).FontSize(12)
		ui.Text(c, "Retry").FontSize(11.5)
	}).Clicked() {
		a.backend.Retry(t.env, t.id(), p.Command)
	}
	if ghost(c, "dismiss", false).Padding(2, 8).Children(func() {
		ui.Icon(c, icX).FontSize(12)
		ui.Text(c, "Dismiss").FontSize(11.5)
	}).Clicked() {
		a.backend.Dismiss(t.env, t.id(), p.Command)
	}
}

func (a *app) workLog(c *ui.Context, key string, e entry) {
	working := e.working
	open := !a.collapsed[key]
	ui.Column(c).Key(key).Radius(12).Border(1, palette.border).Background(palette.bg.Alpha(0.6)).Clip().
		Transition(riseIn).Children(func() {
		if ghost(c, "head", false).Radius(0).Padding(8, 12).Children(func() {
			if working {
				statusMark(c, statusWorking)
			}
			if working {
				shimmerText(c, e.took, 12, palette.muted, palette.text)
			} else {
				ui.Text(c, e.took).FontSize(12).Grow(1)
			}
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
			for _, w := range e.work {
				// Each step the agent takes unfolds below the last.
				ui.Row(c).Key(w.Item).Gap(8).AlignItems(ui.Center).ClipY().Transition(fold).Children(func() {
					icon := icFile
					switch w.Kind {
					case "command":
						icon = icTerminal
					case "edit":
						icon = icPencil
					case "search":
						icon = icSearch
					}
					ui.Icon(c, icon).FontSize(12).TextColor(palette.faint)
					ui.Text(c, w.Title).FontSize(12).SingleLine().Shrink(1)
					switch w.Status {
					case api.WorkStarted:
						statusMark(c, statusWorking)
					case api.WorkFailed:
						ui.Icon(c, icX).FontSize(12).TextColor(palette.red).Tooltip("Failed")
					}
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

// requestCard is what the agent asks before it goes on: Allow and Deny
// while it waits, then what was answered.
func (a *app) requestCard(c *ui.Context, t thread, e entry) {
	r := e.request
	ui.Column(c).Key(e.key).Gap(10).Padding(12, 14).Radius(12).Border(1, palette.amber.Alpha(0.45)).
		Background(palette.amber.Alpha(0.06)).Transition(riseIn).Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, icHand).FontSize(15).TextColor(palette.amber)
			ui.Text(c, asks(r.Body)).FontWeight(500).Grow(1).Shrink(1)
		})
		if r.State != api.RequestPending {
			ui.Text(c, answered(r)).FontSize(12).TextColor(palette.muted)
			return
		}
		sent, sending := responding(t, r.Request)
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			switch {
			case sending && sent.Err != nil:
				ui.Text(c, "Not sent: "+sent.Err.Error()).FontSize(12).TextColor(palette.red).Grow(1).Shrink(1)
				a.failedActions(c, t, sent)
				return
			case sending:
				ui.Text(c, "Sending…").FontSize(12).TextColor(palette.muted)
			}
			ui.Spacer(c)
			if headerButton(c, "deny").Disabled(sending).Children(func() { ui.Text(c, "Deny").FontSize(12.5) }).Clicked() {
				a.backend.Respond(t.env, t.id(), r.Request, false)
			}
			allow := ui.ButtonBase(c.Key("allow")).Padding(5, 14).Radius(999).Background(palette.accent).
				TextColor(ui.Hex("#ffffff")).Cursor(ui.CursorPointer).Disabled(sending)
			if allow.Children(func() { ui.Text(c, "Allow").FontSize(12.5).FontWeight(600) }).Clicked() {
				a.backend.Respond(t.env, t.id(), r.Request, true)
			}
		})
	})
}

// asks is what a request's body asks for, as {"operation":"edit",
// "path":"main.go"} asks to edit main.go, or the body itself.
func asks(body json.RawMessage) string {
	var b struct{ Operation, Path string }
	if json.Unmarshal(body, &b) == nil && b.Operation != "" {
		return strings.TrimSpace("Allow " + b.Operation + " " + b.Path + "?")
	}
	return "The agent asks: " + string(body)
}

func answered(r *client.RequestEntry) string {
	switch r.State {
	case api.RequestInvalidated, api.RequestExpired:
		if r.Reason != "" {
			return "No longer asked: " + r.Reason
		}
		return "No longer asked"
	}
	var b struct{ Allow bool }
	if json.Unmarshal(r.Answer, &b) == nil && b.Allow {
		return "Allowed"
	}
	return "Denied"
}

// responding is the answer to the request sent from here and not landed,
// if there is one.
func responding(t thread, request api.ID) (client.Pending, bool) {
	for _, p := range t.Pending {
		if r, ok := p.Body.(api.RequestRespond); ok && r.Request == request {
			return p, true
		}
	}
	return client.Pending{}, false
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

// composerResize is how the composer grows and shrinks, compact while the
// user reads back up the thread.
var composerResize = ui.ElementTransition{Size: true, Duration: 250 * time.Millisecond, Ease: easeOut}

// composerCompact reports whether the composer is compact: away from the
// end of the thread, not clicked open, holding nothing.
func (a *app) composerCompact(t thread) bool {
	if a.listFor(t.id()).AtEnd() {
		a.composerOpened = false
		return false
	}
	return !a.composerOpened && strings.TrimSpace(a.draft) == "" && len(a.attachments) == 0
}

// compactWidth is the composer's width while compact.
const compactWidth = 520

func (a *app) composer(c *ui.Context, t thread) {
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
			// Files go to the agent as their paths, which a server's
			// agent can't read.
			dragOver := t.local() && a.dropZone(card)
			a.pane(card, glass.Glass{}).Shadow(0, 12, 32, 0, ui.RGBA(0, 0, 0, 0.28)).Clip().
				Transition(composerResize).Children(func() {
				workingGlow(c, card, t.View.Working())
				if len(a.attachments) > 0 {
					ui.Column(c).Key("attachments").Padding(12, 12, 0).Children(func() {
						attachmentChips(c, a.attachments, t, func(i int) {
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
				ui.TextArea(c.Key("draft"), &a.draft).
					Placeholder("Ask anything, @tag files/folders, or use / to show available commands").
					Lines(2, 10).Background(ui.Transparent).Border(0, ui.Transparent).FocusRing(false).
					Padding(14, 16, 4).AutoFocus().OnPaste(a.onPaste).OnTextKey(a.suggestKey)
				ui.Row(c).Key("controls").Padding(6, 8, 8).Gap(2).AlignItems(ui.Center).ClipY().Transition(fold).Children(func() {
					a.composerControls(c)
					ui.Spacer(c)
					if t.local() && iconButton(c, "attach", icPaperclip, "Attach files", false).Clicked() {
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
				a.workspaceButton(c, t)
			})
		})
	})
}

// workspaceButton is where the thread runs: its workspace, and its machine
// when there are several. A draft's is a menu of where it could start.
func (a *app) workspaceButton(c *ui.Context, t thread) {
	several := len(a.backend.Envs()) > 1
	b := ghost(c, "workspace", false).Padding(3, 6).Label("Workspace").Children(func() {
		ui.Icon(c, icFolder).FontSize(12)
		ui.Text(c, filepath.Base(t.View.Workspace)).FontSize(11.5)
		if several && t.env != nil {
			ui.Text(c, "on "+t.env.Name).FontSize(11.5).TextColor(palette.faint)
		}
		if t.id().IsZero() {
			ui.Icon(c, icChevronD).FontSize(10)
		}
	})
	if !t.id().IsZero() {
		return
	}
	b.Menu(func(m *ui.Menu) {
		for _, e := range a.backend.Envs() {
			home := homeDir()
			if e.Kind == client.Remote {
				home = "~"
			}
			for _, d := range a.workspacesOn(e, home) {
				label := filepath.Base(d.workspace)
				if several {
					label += " on " + e.Name
				}
				if m.Item(label).Checked(t.env == e && t.View.Workspace == d.workspace).Chosen() {
					a.fresh = d
				}
			}
		}
	})
}

// workspacesOn are where a draft on e could start: the workspaces of e's
// threads, and home.
func (a *app) workspacesOn(e *client.Env, home string) []draft {
	ds := []draft{{e, home}}
	for _, p := range a.projects {
		if p.env == e && p.key.workspace != home {
			ds = append(ds, draft{e, p.key.workspace})
		}
	}
	return ds
}

// sendButton presses in as it's pressed; its color says whether there is
// anything to send. While the agent works, it stops it.
func (a *app) sendButton(c *ui.Context, t thread) {
	working := t.View.Working()
	empty := a.message(t) == ""
	label := "Send"
	if working {
		label = "Stop"
	}
	send := ui.ButtonBase(c.Key("send")).Label(label).Size(30, 30).Radius(15).Center().
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
			a.backend.Interrupt(t.env, t.id())
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

// send sends the draft in the thread, creating it first when it's the
// draft. A settled or snoozed thread comes back to the inbox with it.
func (a *app) send(t thread) {
	text := a.message(t)
	if text == "" || t.env == nil {
		return
	}
	id := t.id()
	if id.IsZero() {
		title := text
		if r := []rune(title); len(r) > 48 {
			title = string(r[:48]) + "…"
		}
		id = a.backend.Create(t.env, t.View.Workspace, title, a.modelRef(), accessOf[a.access])
		a.selected = id
		a.follow()
	} else if t.View.Disposition.State != api.Open {
		a.backend.SetDisposition(t.env, id, api.Disposition{})
	}
	a.backend.Send(t.env, id, text, api.AfterActive)
	a.draft = ""
	a.attachments = slices.DeleteFunc(a.attachments, func(at attachment) bool { return at.held(t) == "" })
	a.listFor(id).ScrollToEnd()
}

// message is the draft and what's attached to it that goes to t's agent:
// files as their paths, as the composer's @ puts them, and pasted text
// after.
func (a *app) message(t thread) string {
	parts := []string{strings.TrimSpace(a.draft)}
	for _, at := range a.attachments {
		if at.held(t) != "" {
			continue
		}
		switch {
		case at.path != "":
			parts = append(parts, "@"+at.path)
		case at.text != "":
			parts = append(parts, at.text)
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n\n"))
}

// modelRef is the model the pickers chose, for a new thread.
func (a *app) modelRef() api.ModelRef {
	m := modelByName(a.model)
	return api.ModelRef{Provider: strings.ToLower(m.provider), Model: m.name, Effort: strings.ToLower(a.effort), Fast: a.fast}
}

var accessOf = map[string]api.Access{accessAsk: api.AccessAsk, accessAuto: api.AccessAutoEdits, accessFull: api.AccessFull}

func (a *app) terminal(c *ui.Context, workspace string) {
	s, err := a.shellFor(workspace)
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

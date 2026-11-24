package main

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"glitched/internal/api"
	"glitched/internal/client"
	"glitched/internal/cliupdate"
	"glitched/internal/transport"
)

// cliKey is one provider's CLI on one machine: the env that reaches it,
// so a machine removed and added again, or two of one name, keep apart.
type cliKey struct {
	env      *client.Env
	provider string
}

// cliRun is how the user's last update of a CLI goes, or went: running,
// or waiting for its provider's turn there to end first; refused, busy,
// as a turn of it runs; or failed, and why. Output is what the updater
// printed.
type cliRun struct {
	running, waiting, busy bool
	failed                 string
	output                 string
	cancel                 func() // gives up waiting for the turn to end
}

// cliLists are what the remote machines' engines last said of their
// providers, the local engine's being models.served, and the updates the
// user ran.
type cliLists struct {
	served map[*client.Env][]transport.ProviderInfo
	// engine is the engine each machine's answer came from: one paired
	// again under its label is another, whose CLIs it doesn't know yet.
	engine map[*client.Env]api.ID
	asking map[*client.Env]bool
	next   map[*client.Env]time.Time
	// fresh are the machines whose next ask has each provider ask its
	// program, and look versions up again.
	fresh map[*client.Env]bool
	runs  map[cliKey]*cliRun
	// updated is when each CLI's last update came back: an answer asked
	// for before it says what was there before it.
	updated map[cliKey]time.Time
	open    bool // the sidebar's list of updates
}

func newCLILists() cliLists {
	return cliLists{served: map[*client.Env][]transport.ProviderInfo{}, engine: map[*client.Env]api.ID{}, asking: map[*client.Env]bool{}, next: map[*client.Env]time.Time{}, fresh: map[*client.Env]bool{}, runs: map[cliKey]*cliRun{}, updated: map[cliKey]time.Time{}}
}

// cliEntry is a provider's CLI on a machine, as its engine said.
type cliEntry struct {
	key  cliKey
	env  *client.Env
	info transport.ProviderInfo
}

// askCLIs asks every remote machine's engine what its providers are, once
// its answer is old, as askModels asks the local one.
func (a *app) askCLIs() {
	l := &a.clis
	l.prune(a.backend.Envs())
	for _, e := range a.backend.Envs() {
		if e.Kind != client.Remote || a.backend.ShownBy(e) != e || e.Status != client.Status(client.Online{}) {
			continue
		}
		fresh := l.fresh[e]
		if l.asking[e] || !fresh && time.Now().Before(l.next[e]) {
			continue
		}
		l.asking[e] = true
		delete(l.fresh, e)
		asked := time.Now()
		a.backend.Providers(e, fresh, a.settings.checkCLIs, func(served []transport.ProviderInfo, err error) {
			l.asking[e] = false
			if err != nil {
				l.next[e] = time.Now().Add(modelsRetry)
				time.AfterFunc(modelsRetry, func() { a.post(func() {}) })
				return
			}
			l.next[e] = time.Now().Add(modelsRecheck)
			old := l.served[e]
			if l.engine[e] != e.ID {
				old = nil
			}
			for i, p := range served {
				if j := slices.IndexFunc(old, func(o transport.ProviderInfo) bool { return o.Name == p.Name }); j >= 0 {
					served[i] = a.newer(cliKey{e, p.Name}, asked, old[j], p)
				}
			}
			l.served[e], l.engine[e] = served, e.ID
		})
	}
}

// prune forgets the machines no longer listed, and their updates, and
// what an engine that no longer answers for its machine said. A machine
// no engine has answered for yet keeps when to ask it again.
func (l *cliLists) prune(envs []*client.Env) {
	listed := func(e *client.Env) bool {
		id, answered := l.engine[e]
		return slices.Contains(envs, e) && (e.Kind == client.Local || !answered || id == e.ID)
	}
	maps.DeleteFunc(l.served, func(e *client.Env, _ []transport.ProviderInfo) bool { return !listed(e) })
	maps.DeleteFunc(l.next, func(e *client.Env, _ time.Time) bool { return !listed(e) })
	maps.DeleteFunc(l.fresh, func(e *client.Env, _ bool) bool { return !listed(e) })
	maps.DeleteFunc(l.runs, func(k cliKey, _ *cliRun) bool { return !listed(k.env) })
	maps.DeleteFunc(l.engine, func(e *client.Env, _ api.ID) bool { return !listed(e) })
	maps.DeleteFunc(l.updated, func(k cliKey, _ time.Time) bool { return !listed(k.env) })
}

// newer is what to keep of a provider an engine was asked of at asked:
// what it said before, when an update of it came back since, else what
// it says now, with keptUpdate's update.
func (a *app) newer(k cliKey, asked time.Time, old, now transport.ProviderInfo) transport.ProviderInfo {
	if old.Name != "" && a.clis.updated[k].After(asked) {
		return old
	}
	if r := a.clis.runs[k]; r != nil && r.running && now.Update == nil {
		now.Update = old.Update
	}
	return keptUpdate(old, now)
}

// keptUpdate is what an engine now says of a provider, but for an update
// it said of it before: an answer that doesn't know the version
// installed, as when its probe failed, or knows it but not the latest,
// as when offline, doesn't say the update is done.
func keptUpdate(old, now transport.ProviderInfo) transport.ProviderInfo {
	if old.Update == nil || now.State == transport.ProviderNotInstalled {
		return now
	}
	unknown := now.Info.Version == ""
	unchecked := now.Info.Version == old.Update.Installed && (now.Update == nil || now.Update.Latest == "")
	if unknown || unchecked {
		now.Update = old.Update
	}
	return now
}

// checkCLIs asks every engine again now: each provider its program, and
// each version its source.
func (a *app) checkCLIs() {
	for _, e := range a.backend.Envs() {
		if e.Kind == client.Remote {
			a.clis.fresh[e] = true
		}
	}
	a.checkProviders()
	a.askCLIs()
}

// cliEntries are the providers of every machine shown, the local one's
// first, in the order Settings › Providers lists them.
func (a *app) cliEntries() []cliEntry {
	var out []cliEntry
	for i, e := range a.backend.Envs() {
		if a.backend.ShownBy(e) != e {
			continue
		}
		served := a.clis.served[e]
		if i == 0 {
			served = nil
			for _, f := range a.providerFamilies() {
				if p, ok := a.models.served[f.provider]; ok {
					served = append(served, p)
				}
			}
		}
		for _, p := range served {
			out = append(out, cliEntry{cliKey{e, p.Name}, e, p})
		}
	}
	return out
}

// pendingCLIs are the CLIs with a newer version than their machine runs,
// while the check is on.
func (a *app) pendingCLIs() []cliEntry {
	if !a.settings.checkCLIs {
		return nil
	}
	var out []cliEntry
	for _, en := range a.cliEntries() {
		r := a.clis.runs[en.key]
		if u := en.info.Update; u != nil && u.Available || r != nil && r.running {
			out = append(out, en)
		}
	}
	return out
}

// updateCLI runs the CLI's updater on its machine, or, whenIdle, once no
// turn of its provider runs there, and keeps how it went. What the engine
// says of the provider after takes the place of what it said before.
func (a *app) updateCLI(en cliEntry, whenIdle bool) {
	r := a.clis.runs[en.key]
	if r == nil {
		r = &cliRun{}
		a.clis.runs[en.key] = r
	}
	if r.running {
		return
	}
	*r = cliRun{running: true, waiting: whenIdle}
	engine := en.env.ID
	r.cancel = a.runUpdate(en.env, en.key.provider, whenIdle, func(res transport.UpdateResult, err error) {
		if en.env.ID != engine {
			*r = cliRun{}
			return // another engine answers for the machine now
		}
		*r = cliRun{output: res.Output, busy: res.Busy}
		switch {
		case errors.Is(err, context.Canceled):
			return
		case err != nil:
			r.failed = err.Error()
		case !res.Busy && res.Error != "":
			r.failed = res.Error
		}
		// What a failed update's provider says may not know its versions
		// any more: the update it still needs stays.
		if r.failed != "" || r.busy || res.Provider.Name == "" {
			return
		}
		a.clis.updated[en.key] = time.Now()
		if envs := a.backend.Envs(); len(envs) > 0 && en.env == envs[0] {
			if a.models.served == nil {
				a.models.served = map[string]transport.ProviderInfo{}
			}
			a.models.served[res.Provider.Name] = keptUpdate(a.models.served[res.Provider.Name], res.Provider)
			return
		}
		for i, p := range a.clis.served[en.env] {
			if p.Name == res.Provider.Name {
				a.clis.served[en.env][i] = keptUpdate(p, res.Provider)
			}
		}
	})
}

// runUpdate is the engine's update of a provider, or the test's.
func (a *app) runUpdate(e *client.Env, name string, whenIdle bool, done func(transport.UpdateResult, error)) (cancel func()) {
	if a.updateRunner != nil {
		return a.updateRunner(e, name, whenIdle, done)
	}
	return a.backend.UpdateProvider(e, name, whenIdle, done)
}

// cliUpdate is a CLI's update, as a provider's row and the sidebar's list
// show it: what's newer and what's installed, the Update button or the
// command to run instead, and how the last update went. Title, when not
// "", is the row's heading in place of what the update itself would say,
// as the sidebar's list names the CLI and its machine instead.
func (a *app) cliUpdate(c *ui.Context, en cliEntry, title string) {
	if !a.cliUpdateShows(en) {
		return
	}
	u, r := en.info.Update, a.clis.runs[en.key]
	name := familyOf(en.key.provider).name
	waiting := r != nil && r.waiting
	running := r != nil && r.running
	failed := r != nil && r.failed != ""
	busy := r != nil && r.busy
	manual := u.Manual != "" && u.Available
	command := strings.Join(u.Command, " ")
	tip := ""
	if len(u.Command) > 0 {
		words := append([]string{displayPath(u.Command[0])}, u.Command[1:]...)
		tip = "Runs " + strings.Join(words, " ") + " on " + machineWords(en.env) + "."
	}
	named := title != ""
	if !named {
		title = "Updated to " + u.Installed
		switch {
		case u.Available || running:
			title = "Update to " + u.Latest
		case failed:
			title = "Version " + u.Installed
		}
	}
	ui.Column(c).Key("update:" + en.key.provider).Gap(10).Children(func() {
		ui.Row(c).GapX(16).GapY(10).AlignItems(ui.Center).Wrap().Children(func() {
			ui.Column(c).Grow(1).Shrink(1).Basis(200).Gap(2).Children(func() {
				ui.Text(c, title).Key("available").FontWeight(500)
				switch {
				case waiting:
					ui.Text(c, "Waiting for the turn to end…").FontSize(12).TextColor(palette.muted)
				case running:
					ui.Text(c, "Updating…").FontSize(12).TextColor(palette.muted)
				case failed:
					ui.Text(c, "The update failed: "+clip(r.failed, 300)).Key("failed").Selectable().FontSize(12).TextColor(palette.red)
				case busy:
					ui.Text(c, name+" has a turn running on "+machineWords(en.env)+", so it can't update now.").Key("busy").FontSize(12).TextColor(palette.amber)
				case manual:
					ui.Text(c, u.Manual+" Run this in Terminal on "+machineWords(en.env)+":").Key("manual").FontSize(12).TextColor(palette.muted)
				case u.Available:
					line := u.Installed + " is installed."
					if named {
						line = fmt.Sprintf("%s → %s", u.Installed, u.Latest)
					}
					ui.Text(c, line).FontSize(12).TextColor(palette.muted)
				case named:
					ui.Text(c, "Updated to "+u.Installed).FontSize(12).TextColor(palette.muted)
				}
			})
			if waiting || running || failed || busy || u.Available && !manual {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Shrink(0).Children(func() {
					switch {
					case waiting:
						ui.Spinner(c)
						ui.Button(c.Key("cancel"), "Cancel").OnClick(r.cancel)
					case running:
						ui.Spinner(c)
					case busy && !failed:
						b := ui.Button(c.Key("later"), "Update when it ends").Disabled(!online(en.env)).OnClick(func() { a.updateCLI(en, true) })
						a.tip(c, b, tip)
					default:
						b := ui.Button(c.Key("update"), "Update").Disabled(!online(en.env)).OnClick(func() { a.updateCLI(en, false) })
						a.tip(c, b, tip)
					}
				})
			}
		})
		if manual && command != "" {
			ui.Row(c).Radius(6).Background(palette.bg.Alpha(0.6)).Padding(8, 10).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, command).Key("command").Selectable().Font("monospace").FontSize(11.5).TextColor(palette.text).Grow(1).Shrink(1)
				ui.Button(c.Key("copy"), "Copy").OnClick(func() { c.WriteClipboard(command) })
			})
		}
		if r != nil && r.output != "" && !r.running {
			ui.Column(c).Radius(6).Background(palette.bg.Alpha(0.6)).Padding(8, 10).Clip().Children(func() {
				ui.Text(c, clip(r.output, 1200)).Key("output").Selectable().Font("monospace").FontSize(11.5).TextColor(palette.muted).MaxLines(10)
			})
		}
	})
}

// cliUpdateShows is whether cliUpdate has anything to say of en: an
// update, or how the last one went.
func (a *app) cliUpdateShows(en cliEntry) bool {
	u, r := en.info.Update, a.clis.runs[en.key]
	return u != nil && (u.Available || r != nil && (r.running || r.failed != "" || r.output != ""))
}

// cliUpdatesButton, at the foot of the sidebar, says a CLI on a machine
// of the user's has a newer version: one button however many have, with
// their count when more than one, and a tooltip naming each. It stays
// while any has, shows when one updates, failed or needs the user, and
// opens the list of them, each with its update, and Update all.
func (a *app) cliUpdatesButton(c *ui.Context) {
	a.askCLIs()
	pending := a.pendingCLIs()
	if len(pending) == 0 {
		a.clis.open = false
		return
	}
	running, failed, needed := false, false, false
	for _, en := range pending {
		r := a.clis.runs[en.key]
		running = running || r != nil && r.running
		failed = failed || r != nil && r.failed != ""
		needed = needed || r != nil && r.busy || en.info.Update != nil && en.info.Update.Manual != ""
	}
	tone, icon, label := palette.blue, icArrowDown, "Updates"
	switch {
	case failed:
		tone, icon = palette.red, icInfo
	case needed:
		tone, icon = palette.amber, icInfo
	}
	if running {
		label = "Updating"
	}
	b := ui.ButtonBase(c.Key("cli-updates")).Gap(5).AlignItems(ui.Center).Padding(4, 7).Radius(6).Shrink(0).
		TextColor(palette.text).Background(tone.Alpha(0.14)).Cursor(ui.CursorPointer).Transition(hoverFade).Label("Agent CLI updates")
	if a.clis.open || b.Hovered() {
		b.Background(tone.Alpha(0.22))
	}
	a.tip(c, b, a.cliUpdatesTip(pending))
	b.Children(func() {
		if running {
			ui.Spinner(c).Size(13, 13)
		} else {
			ui.Icon(c, icon).FontSize(14).TextColor(tone)
		}
		ui.Text(c, label).FontSize(12.5).FontWeight(550).SingleLine()
		if len(pending) > 1 {
			ui.Box(c).MinWidth(16).Height(16).Padding(0, 4).Radius(8).Background(tone.Alpha(0.22)).AlignItems(ui.Center).Justify(ui.Center).Children(func() {
				ui.Text(c, strconv.Itoa(len(pending))).FontSize(11).FontWeight(650)
			})
		}
	})
	if b.Clicked() {
		a.clis.open = !a.clis.open
	}
	a.popover(c, b, &a.clis.open, func(panel ui.Element) {
		panel.Padding(12).Gap(12).Width(380).Radius(10).Border(1, palette.border).Background(palette.surface).Shadow(0, 12, 32, 0, ui.RGBA(0, 0, 0, 0.3))
		ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
			ui.Text(c, "CLI updates").FontWeight(600).Grow(1)
			if len(pending) < 2 {
				return
			}
			var runnable []cliEntry
			for _, en := range pending {
				if r := a.clis.runs[en.key]; en.info.Update != nil && en.info.Update.Manual == "" && online(en.env) && (r == nil || !r.running) {
					runnable = append(runnable, en)
				}
			}
			ui.Button(c.Key("all"), "Update all").Disabled(len(runnable) == 0).OnClick(func() {
				for _, en := range runnable {
					a.updateCLI(en, false)
				}
			})
		})
		several := len(a.backend.Envs()) > 1
		for _, en := range pending {
			ui.Row(c).Key(fmt.Sprintf("cli:%p/%s", en.env, en.key.provider)).Gap(10).AlignItems(ui.Start).Children(func() {
				f := familyOf(en.key.provider)
				ui.Box(c).Margin(1, 0, 0, 0).Children(func() { modelTile(c, f.icon, f.color, 22) })
				title := f.name
				if several {
					title += " on " + machineWords(en.env)
				}
				ui.Column(c).Grow(1).Shrink(1).Children(func() {
					a.cliUpdate(c, en, title)
				})
			})
		}
	})
}

// cliUpdatesTip names each pending CLI as "Claude Code 2.1.0 → 2.2.0",
// under its machine when the user has more than one, and says of each
// that updates, waits, failed or needs the user.
func (a *app) cliUpdatesTip(pending []cliEntry) string {
	several := len(a.backend.Envs()) > 1
	var lines []string
	var machine *client.Env
	for _, en := range pending {
		if several && en.env != machine {
			if machine != nil {
				lines = append(lines, "")
			}
			m := en.env.Name
			switch {
			case en.env.Kind == client.Local:
				m = "This machine"
			case m == "":
				m = "A machine with no name"
			}
			lines = append(lines, m)
			machine = en.env
		}
		u := en.info.Update
		line := cliupdate.Name(en.key.provider)
		if u != nil {
			line += fmt.Sprintf(" %s → %s", u.Installed, u.Latest)
		}
		r := a.clis.runs[en.key]
		switch {
		case r != nil && r.waiting:
			line += " · waits for its turn to end"
		case r != nil && r.running:
			line += " · updating"
		case r != nil && r.failed != "":
			line += " · update failed"
		case r != nil && r.busy:
			line += " · a turn is running"
		case u.Manual != "":
			line += " · update it yourself"
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

// online is whether e's engine answers now, as an update needs.
func online(e *client.Env) bool { return e.Status == client.Status(client.Online{}) }

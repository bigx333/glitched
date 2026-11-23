package main

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"glitched/internal/api"
	"glitched/internal/client"
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
// show it: what's installed and what's newer, the Update button or the
// command to run instead, and how the last update went.
func (a *app) cliUpdate(c *ui.Context, en cliEntry) {
	if !a.cliUpdateShows(en) {
		return
	}
	u, r := en.info.Update, a.clis.runs[en.key]
	name := familyOf(en.key.provider).name
	ui.Column(c).Key("update:" + en.key.provider).Gap(8).Children(func() {
		if u.Available || r != nil && r.running {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Wrap().Children(func() {
				ui.Text(c, fmt.Sprintf("Update available: %s → %s", u.Installed, u.Latest)).Key("available").FontSize(12.5).FontWeight(550).TextColor(palette.blue).Grow(1)
				switch {
				case r != nil && r.waiting:
					ui.Spinner(c)
					ui.Text(c, "Waiting for the turn to end…").FontSize(12).TextColor(palette.muted)
					ui.Button(c.Key("cancel"), "Cancel").OnClick(r.cancel)
				case r != nil && r.running:
					ui.Spinner(c)
					ui.Text(c, "Updating…").FontSize(12).TextColor(palette.muted)
				case u.Manual != "":
				case r != nil && r.busy:
					ui.Button(c.Key("later"), "Update when it ends").Disabled(!online(en.env)).OnClick(func() { a.updateCLI(en, true) })
				default:
					ui.Button(c.Key("update"), "Update").Disabled(!online(en.env)).OnClick(func() { a.updateCLI(en, false) })
				}
			})
		}
		if r != nil && r.busy {
			ui.Text(c, name+" has a turn running on "+machineWords(en.env)+", so it can't update now.").Key("busy").FontSize(12).TextColor(palette.amber)
		}
		if r != nil && r.failed != "" {
			ui.Text(c, "The update failed: "+clip(r.failed, 300)).Key("failed").Selectable().FontSize(12).TextColor(palette.red)
		}
		manual := u.Manual != "" && u.Available
		if manual {
			ui.Text(c, u.Manual+" Run this in Terminal on "+machineWords(en.env)+":").Key("manual").FontSize(12).TextColor(palette.muted)
		}
		if command := strings.Join(u.Command, " "); command != "" && (manual || r != nil && r.running) {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, command).Key("command").Selectable().Font("monospace").FontSize(11.5).TextColor(palette.text).Grow(1).Shrink(1)
				if manual {
					ui.Button(c.Key("copy"), "Copy").OnClick(func() { c.WriteClipboard(command) })
				}
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
// of the user's has a newer version. It stays while any has, and opens
// the list of them, each with its update, and Update all.
func (a *app) cliUpdatesButton(c *ui.Context) {
	a.askCLIs()
	pending := a.pendingCLIs()
	if len(pending) == 0 {
		a.clis.open = false
		return
	}
	label := "Update " + familyOf(pending[0].key.provider).name
	if len(pending) > 1 {
		label = fmt.Sprintf("Updates for %d CLIs", len(pending))
	}
	failed, running := false, false
	for _, en := range pending {
		if r := a.clis.runs[en.key]; r != nil {
			failed = failed || r.failed != "" || r.busy
			running = running || r.running
		}
	}
	b := ghost(c, "cli-updates", a.clis.open).Padding(7, 8).Label(label)
	b.Children(func() {
		if running {
			ui.Spinner(c)
		} else {
			ui.Icon(c, icArrowDown).FontSize(15).TextColor(palette.blue)
		}
		ui.Text(c, label).Grow(1).SingleLine()
		dot := palette.blue
		if failed {
			dot = palette.red
		}
		ui.Box(c).Size(7, 7).Radius(4).Background(dot)
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
				if r := a.clis.runs[en.key]; en.info.Update.Manual == "" && online(en.env) && (r == nil || !r.running) {
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
			ui.Row(c).Key(fmt.Sprintf("cli:%p/%s", en.env, en.key.provider)).Gap(10).Children(func() {
				f := familyOf(en.key.provider)
				modelTile(c, f.icon, f.color, 22)
				ui.Column(c).Grow(1).Shrink(1).Gap(6).Children(func() {
					title := f.name
					if several {
						title += " on " + machineWords(en.env)
					}
					ui.Text(c, title).FontWeight(550).SingleLine()
					a.cliUpdate(c, en)
				})
			})
		}
	})
}

// online is whether e's engine answers now, as an update needs.
func online(e *client.Env) bool { return e.Status == client.Status(client.Online{}) }

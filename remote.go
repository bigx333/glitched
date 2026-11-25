package main

import (
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"glitched/internal/api"
	"glitched/internal/client"
	"glitched/internal/daemon"
)

// Remote children: an agent here may start a child on another machine's
// engine through a link that engine minted for this one. The machine that
// runs the child (B) turns Children from other machines on, mints the link
// and removes the machine it was minted for on its This machine page; the machine that delegates (A)
// adds B there with the link's token. On A the child is a placeholder in
// the parent's tree, which says where it runs and opens B's thread when
// this app has B paired; on B the thread says where it came from.

// minted is a link the This machine page minted, shown until dismissed:
// the engine keeps only its token's hash.
type minted struct {
	label, token string
	link         api.Link
	at           time.Time
}

// childLevels are the choices of Children from other machines, off first,
// as the composer names the access levels.
var childLevels = []string{"Off", accessAsk, accessAuto, accessFull}

// fullWarning is what choosing Full access for children from other machines
// warns, as glitched pair -children full does.
const fullWarning = "An agent at Full access runs as this machine's user with no prompts, so it can read every thread's credentials here."

// levelOf is the index in childLevels of a ceiling, nil for off.
func levelOf(ceiling *api.Access) int {
	if ceiling == nil {
		return 0
	}
	return int(*ceiling) + 1
}

// linksCard is B's side: whether other machines' agents may start
// children here and at most at what access, the links minted for them, and
// minting another.
func (a *app) linksCard(c *ui.Context, m client.Machine) {
	ceiling := m.Config.ChildrenFromOtherMachines
	card(c, "CHILDREN FROM OTHER MACHINES", func() {
		level := levelOf(ceiling)
		ui.Column(c).Key("children-from-others").Padding(14, 16).Gap(10).Children(func() {
			ui.Column(c).Gap(2).Children(func() {
				ui.Text(c, "Children from other machines").FontWeight(500)
				ui.Text(c, "The most access a child an agent on another machine starts here may run at, through a link minted below. Off removes every machine below at once and deletes its link, so on again starts with none.").FontSize(12).TextColor(palette.muted)
			})
			if ui.Segmented(c.Key("children-from-others"), &level, childLevels...).Label("Children from other machines").Changed() {
				var v *api.Access
				if level > 0 {
					l := api.Access(level - 1)
					v = &l
				}
				a.configure(func(cfg *daemon.Config) { cfg.ChildrenFromOtherMachines = v })
			}
		})
		if ceiling != nil && *ceiling == api.AccessFull {
			ui.Row(c).Key("full-warning").Padding(12, 16).Gap(10).AlignItems(ui.Start).Background(danger.Alpha(0.08)).Children(func() {
				ui.Icon(c, icShieldAlert).FontSize(14).TextColor(danger).Shrink(0)
				ui.Text(c, fullWarning).FontSize(12).TextColor(palette.text).Grow(1).Shrink(1)
			})
		}
		for _, t := range m.Tokens {
			if t.Link != nil {
				a.linkRow(c, t, ceiling)
			}
		}
		// A link whose machine was removed since it was minted takes its token
		// with it.
		if p := a.minted; p.token != "" && m.Read.After(p.at) && !slices.ContainsFunc(m.Tokens, func(t daemon.Token) bool { return t.Link != nil && t.Link.ID == p.link.ID }) {
			a.minted = minted{}
		}
		if a.minted.token != "" {
			a.mintedRow(c, m)
		}
		if ceiling != nil {
			a.mintRow(c, *ceiling)
		}
	})
}

// linkRow is a link minted here, with what it lets the other machine's
// agents start, and removing that machine.
func (a *app) linkRow(c *ui.Context, t daemon.Token, ceiling *api.Access) {
	l := t.Link
	desc := accessLabel(l.Access) + " at most"
	switch {
	case ceiling == nil:
		desc = "Starts nothing while children from other machines are off"
	case l.Access > *ceiling:
		desc += ", held to " + accessLabel(*ceiling) + " by the setting above"
	}
	if l.Working > 0 {
		desc += fmt.Sprintf(", %d at work at once", l.Working)
	}
	if l.All > 0 {
		desc += fmt.Sprintf(", %d in all", l.All)
	}
	desc += ". Minted " + ago(t.Created, c.Now()) + "."
	ui.Row(c).Key("link "+l.ID.String()).Padding(12, 12, 12, 16).Gap(12).AlignItems(ui.Center).ClipY().Transition(fold).Children(func() {
		ui.Column(c).Grow(1).Shrink(1).Gap(2).Children(func() {
			ui.Text(c, t.Label).FontWeight(500)
			ui.Text(c, desc).FontSize(12).TextColor(palette.muted)
		})
		ui.Button(c.Key("remove-link"), "Remove").Label("Remove " + t.Label).TextColor(palette.red).OnClick(func() {
			a.machineErr = ""
			if err := a.backend.RemoveToken(t.Label); err != nil {
				a.machineErr = err.Error()
			}
		})
	})
}

// mintRow mints a link: for which machine, at most at what access, which
// may be no more than ceiling, and how many children, 0 for this engine's
// child limits.
func (a *app) mintRow(c *ui.Context, ceiling api.Access) {
	if a.mint.access > int(ceiling) {
		a.mint.access = int(ceiling)
	}
	ui.Column(c).Key("mint").Padding(12, 12, 12, 16).Gap(10).Children(func() {
		ui.TextInput(c.Key("mint-label"), &a.mint.label).Placeholder("The other machine, such as laptop").Label("The other machine's label")
		ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "At most").FontSize(12).TextColor(palette.muted)
			ui.Segmented(c.Key("mint-access"), &a.mint.access, childLevels[1:int(ceiling)+2]...).Label("Most access")
		})
		ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "At work at once").FontSize(12).TextColor(palette.muted).SingleLine().Shrink(0)
			ui.NumberInput(c.Key("mint-working"), &a.mint.working, 0, 100, 1).Label("At work at once").Width(110)
			ui.Text(c, "In all").FontSize(12).TextColor(palette.muted).SingleLine().Shrink(0)
			ui.NumberInput(c.Key("mint-all"), &a.mint.all, 0, 1000, 1).Label("In all").Width(110)
		})
		ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "0 takes this machine's own child limits.").FontSize(12).TextColor(palette.faint).Grow(1).Shrink(1)
			ui.Button(c.Key("mint-link"), "Mint a link").Disabled(strings.TrimSpace(a.mint.label) == "").OnClick(func() {
				a.machineErr = ""
				label := strings.TrimSpace(a.mint.label)
				token, l, err := a.backend.PairLink(label, api.Link{Access: api.Access(a.mint.access), Working: int(a.mint.working), All: int(a.mint.all)})
				if err != nil {
					a.machineErr = err.Error()
					return
				}
				a.minted, a.mint.label = minted{label, token, l, time.Now()}, ""
			})
		})
	})
}

// servingAddr is where another machine reaches this engine: on the
// tailnet, or at a listener that isn't loopback; "" when it can't.
func servingAddr(m client.Machine) string {
	if m.Tailnet.Serving != "" {
		return m.Tailnet.Serving
	}
	for _, l := range m.Listeners {
		// Another machine can't reach this one's loopback.
		if ap, err := netip.ParseAddrPort(l.Addr); l.Accepting && err == nil && !ap.Addr().IsLoopback() {
			return l.Addr
		}
	}
	return ""
}

// mintedRow shows the link just minted, its token this once, and what to
// run on the machine it's for, as glitched pair -link prints.
func (a *app) mintedRow(c *ui.Context, m client.Machine) {
	p := a.minted
	addr := servingAddr(m)
	self := "<this machine>"
	if name, _, _ := strings.Cut(m.Tailnet.Name, "."); name != "" {
		self = name
	} else if m.Engine != nil && m.Engine.Name != "" {
		self = m.Engine.Name
	}
	line := ""
	if addr != "" {
		line = "glitched link -add " + shellWord(self) + " -server " + shellWord(addr) + " -token " + shellWord(p.token)
	}
	ui.Column(c).Key("minted").Padding(14, 16).Gap(10).Background(palette.green.Alpha(0.06)).Transition(fold).Children(func() {
		ui.Text(c, "Link for "+p.label+", at "+accessLabel(p.link.Access)+" at most. Its token, shown only this once").FontWeight(500)
		ui.Text(c, p.token).FontSize(12).Font("monospace").Selectable()
		hint := "On " + p.label + ", add this machine at " + addr + " with this token in Settings › This machine › Machines this one delegates to, or run:"
		if addr == "" {
			hint = "Turn on Serve on the tailnet for " + p.label + " to reach this machine. Then on " + p.label + ", add it with this token in Settings › This machine › Machines this one delegates to."
		}
		ui.Text(c, hint).FontSize(12).TextColor(palette.muted)
		if line != "" {
			ui.Text(c, line).Key("link-line").FontSize(12).Font("monospace").Selectable()
		}
		ui.Row(c).Gap(8).Children(func() {
			ui.Button(c.Key("copy-link-token"), "Copy token").OnClick(func() { a.copy(c, p.token) })
			if line != "" {
				ui.Button(c.Key("copy-link-line"), "Copy command").OnClick(func() { a.copy(c, line) })
			}
			ui.Button(c.Key("minted-done"), "Done").OnClick(func() { a.minted = minted{} })
		})
	})
}

// shellWord is s quoted for a shell, when it needs it.
func shellWord(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t'\"\\$`<>|&;()*?[]#~") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// delegatesCard is A's side: the machines whose links this engine holds,
// where its agents may start children, adding one by its token, and
// removing one.
func (a *app) delegatesCard(c *ui.Context, m client.Machine) {
	card(c, "MACHINES THIS ONE DELEGATES TO", func() {
		switch {
		case m.MachinesErr != nil:
			ui.Text(c, "Couldn't read the machines: "+m.MachinesErr.Error()).Key("machines-err").FontSize(12).TextColor(palette.red).Padding(14, 16)
		case len(m.Machines) == 0:
			ui.Text(c, "None yet. Add a machine with a link it minted for this one, and agents here may start children there.").Key("no-machines").FontSize(12).TextColor(palette.muted).Padding(14, 16)
		}
		for _, x := range m.Machines {
			ui.Row(c).Key("machine "+x.Label).Padding(12, 12, 12, 16).Gap(12).AlignItems(ui.Center).ClipY().Transition(fold).Children(func() {
				ui.Column(c).Grow(1).Shrink(1).Gap(2).Children(func() {
					ui.Text(c, x.Label).FontWeight(500)
					ui.Text(c, x.Server).FontSize(12).Font("monospace").TextColor(palette.muted).Selectable()
				})
				ui.Button(c.Key("remove-machine"), "Remove").Label("Remove " + x.Label).TextColor(palette.red).OnClick(func() {
					a.machineErr = ""
					if err := a.backend.RemoveMachine(x.Label); err != nil {
						a.machineErr = err.Error()
						return
					}
					a.removed = x.Label
				})
			})
		}
		if a.removed != "" {
			ui.Row(c).Key("removed").Padding(12, 12, 12, 16).Gap(12).AlignItems(ui.Center).Background(palette.amber.Alpha(0.08)).Transition(fold).Children(func() {
				ui.Text(c, "Removed "+a.removed+" here. Its link still works on "+a.removed+" until it removes this machine or turns off children from other machines there, in Settings › This machine.").FontSize(12).Grow(1).Shrink(1)
				ui.Button(c.Key("removed-done"), "Done").OnClick(func() { a.removed = "" })
			})
		}
		ui.Column(c).Key("add-machine").Padding(12, 12, 12, 16).Gap(10).Children(func() {
			ui.Row(c).Children(func() {
				ui.TextInput(c.Key("machine-label"), &a.delegate.label).Placeholder("Label, such as desk").Label("The machine's label").Grow(1)
			})
			ui.Row(c).Children(func() {
				ui.TextInput(c.Key("machine-addr"), &a.delegate.server).Placeholder("desk.tailnet.ts.net:7433").Label("The machine's address").Font("monospace").Grow(1)
			})
			ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
				ui.TextInput(c.Key("machine-token"), &a.delegate.token).Placeholder("The link's token").Label("The link's token").Password().Font("monospace").Grow(1)
				add := ui.Button(c.Key("add-machine"), "Add machine")
				ready := strings.TrimSpace(a.delegate.label) != "" && strings.TrimSpace(a.delegate.server) != "" && strings.TrimSpace(a.delegate.token) != ""
				add.Disabled(!ready || a.delegate.adding).OnClick(func() {
					d := a.delegate
					a.machineErr, a.delegate.adding = "", true
					a.backend.AddMachine(strings.TrimSpace(d.label), strings.TrimSpace(d.server), strings.TrimSpace(d.token), func(err error) {
						a.delegate.adding = false
						if err != nil {
							a.machineErr = err.Error()
							return
						}
						a.delegate = delegateForm{}
					})
				})
				if a.delegate.adding {
					ui.Spinner(c)
				}
			})
		})
	})
}

// delegateForm is the This machine page's form for adding a machine, and
// whether one is waiting for its engine to answer.
type delegateForm struct {
	label, server, token string
	adding               bool
}

// mintForm is the This machine page's form for minting a link.
type mintForm struct {
	label        string
	access       int
	working, all float64
}

// --- A remote child's placeholder, on A

// remote is a placeholder's placement, nil for any other thread.
func remote(t thread) *api.Placement { return t.View.Remote }

// remoteLabel is the label of the machine t, a placeholder, runs on: as
// the engine read it, as this machine's engine lists it, or as this app
// pairs that machine's engine.
func (a *app) remoteLabel(t thread) string {
	r := remote(t)
	if r.Machine != "" {
		return r.Machine
	}
	if t.env != nil && t.env.Kind != client.Remote {
		for _, m := range a.machineStatus().Machines {
			if m.Link == r.Link {
				return m.Label
			}
		}
	}
	if e := a.envOf(r.Engine); e != nil {
		return machineName(e)
	}
	return "another machine"
}

// machineStatus is this machine's engine as last read, which it has read
// again, in the background, when that is past inspectEvery: a label a
// link or a machine gives may be newer than the read.
func (a *app) machineStatus() client.Machine {
	m := a.backend.Machine()
	if time.Since(m.Read) >= inspectEvery {
		a.backend.Inspect()
	}
	return m
}

// envOf is the machine this app pairs whose engine is id, nil for none.
func (a *app) envOf(id api.ID) *client.Env {
	if id.IsZero() {
		return nil
	}
	for _, e := range a.backend.Envs() {
		if e.ID == id {
			return e
		}
	}
	return nil
}

// remoteThread is the thread a placeholder stands for, on its machine,
// when this app pairs that machine and lists the thread.
func (a *app) remoteThread(t thread) (thread, bool) {
	r := remote(t)
	if r == nil || r.Thread.IsZero() {
		return thread{}, false
	}
	e := a.envOf(r.Engine)
	if e == nil || e.Threads[r.Thread] == nil {
		return thread{}, false
	}
	return thread{e, e.Threads[r.Thread]}, true
}

// remoteState is what a placeholder's child does on its machine, in a few
// words, its color, and why it was lost, "" while it isn't.
func (a *app) remoteState(t thread) (string, ui.Color, string) {
	r, on := remote(t), a.remoteLabel(t)
	switch {
	case r.Lost != "":
		return "Lost on " + on, palette.red, r.Lost
	case r.Thread.IsZero() && t.View.Run == api.RunStarting:
		return "Creating on " + on, palette.muted, ""
	case t.View.Run == api.RunUncertain:
		return "Needs you on " + on, palette.amber, "Its run there was cut off, as by a restart of that machine's engine. Continue it or send it a message on " + on + "."
	case t.View.Run == api.RunWaiting:
		return "Needs you on " + on, palette.amber, ""
	case r.Held:
		return "Held on " + on, palette.amber, "Stopped there with messages or reports waiting. They start when someone resumes it on " + on + "; it can't be resumed here."
	case t.View.Working():
		return "Working on " + on, palette.blue, ""
	}
	return "Idle on " + on, palette.faint, ""
}

// remoteMark is a placeholder's state in its row, where another thread's
// row shows its age, with why it was lost as its tip.
func (a *app) remoteMark(c *ui.Context, t thread, size float32) {
	s, col, why := a.remoteState(t)
	m := ui.Text(c, s).Key("remote-state").FontSize(size).TextColor(col).SingleLine().Shrink(0)
	if why != "" {
		a.tip(c, m, why)
	}
}

// remoteNotice is a placeholder's timeline: where its child runs and what
// it does there, and that its thread opens here once this app pairs that
// machine.
func (a *app) remoteNotice(c *ui.Context, t thread) {
	s, col, why := a.remoteState(t)
	on := a.remoteLabel(t)
	ui.Column(c).Key("remote-notice").Center().Gap(8).Padding(140, 0, 0).Children(func() {
		ui.Text(c, s).FontWeight(600).TextColor(col)
		if why != "" {
			ui.Text(c, why).FontSize(12).TextColor(palette.muted).TextAlign(ui.Center)
		}
		if b, ok := a.remoteThread(t); ok {
			if ui.Button(c.Key("open-remote"), "Open on "+on).Clicked() {
				a.choose(b)
			}
			return
		}
		if a.envOf(remote(t).Engine) != nil {
			ui.Text(c, "Its thread opens here once "+on+" has made it.").FontSize(12).TextColor(palette.muted).TextAlign(ui.Center)
			return
		}
		ui.Text(c, "This child's thread is on "+on+". Pair "+on+" in Settings › Servers to open it here.").FontSize(12).TextColor(palette.muted).TextAlign(ui.Center)
	})
}

// --- A link's thread, on B

// originLabel is the label of the machine a link's thread came from: as
// this machine's tokens name the link, or as this app pairs that
// machine's engine.
func (a *app) originLabel(t thread) string {
	o := t.View.Origin
	if t.env != nil && t.env.Kind != client.Remote {
		for _, k := range a.machineStatus().Tokens {
			if k.Link != nil && k.Link.ID == o.Link {
				return k.Label
			}
		}
	}
	if e := a.envOf(o.Engine); e != nil {
		return machineName(e)
	}
	return "another machine"
}

// originCrumb says, before a link's thread's title, which thread on which
// machine started it, and opens that thread while this app pairs that
// machine.
func (a *app) originCrumb(c *ui.Context, t thread) {
	o, on := t.View.Origin, a.originLabel(t)
	// line is the title, cut short first, and the machine whole after it.
	line := func() ui.Element {
		return ui.Row(c).Key("origin-line").Shrink(1).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "from "+o.Title).FontSize(12.5).SingleLine().Shrink(1).MaxWidth(260)
			ui.Text(c, " on "+on).FontSize(12.5).SingleLine().Shrink(0)
		})
	}
	var parent thread
	found := false
	if e := a.envOf(o.Engine); e != nil && e.Threads[o.Thread] != nil {
		parent, found = thread{e, e.Threads[o.Thread]}, true
	}
	if !found {
		a.tip(c, ui.Row(c).Key("origin").Shrink(1).TextColor(palette.faint).Children(func() { line() }), "Started by an agent on "+on)
		return
	}
	b := a.tip(c, ghost(c, "origin", false).Padding(3, 6).Shrink(1).MinWidth(40).Label("Open "+o.Title), "Open "+o.Title+" on "+on)
	if b.Children(func() { line() }).Clicked() {
		a.choose(parent)
	}
}

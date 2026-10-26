package main

import (
	"time"

	"github.com/egoist/mygo/ui"

	"glitched/internal/client"
	"glitched/internal/daemon"
)

// pairing is a token the This machine page minted, shown until dismissed:
// the engine keeps only its hash.
type pairing struct{ label, token string }

// inspectEvery is how often the This machine page reads the engine's
// status while it shows.
const inspectEvery = 2 * time.Second

func (a *app) machine(c *ui.Context) {
	m := a.backend.Machine()
	if time.Since(m.Read) >= inspectEvery {
		a.backend.Inspect()
	}
	c.After(inspectEvery)
	if m.Read.IsZero() {
		ui.Row(c).Key("reading").Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Spinner(c)
			ui.Text(c, "Reading the engine's status…").TextColor(palette.muted)
		})
		return
	}
	if m.Err != nil {
		ui.Text(c, "Couldn't read the engine's state: "+m.Err.Error()).Key("err").TextColor(palette.red)
	}
	a.engineCard(c, m)
	a.tailnetCard(c, m)
	a.tokensCard(c, m)
	if a.machineErr != "" {
		ui.Text(c, a.machineErr).Key("action-err").FontSize(12).TextColor(palette.red)
	}
}

// statusRow is a setting whose title follows a dot of its state's color.
func statusRow(c *ui.Context, key string, col ui.Color, title, desc string, control func()) {
	ui.Row(c).Key(key).Padding(14, 16).Gap(12).AlignItems(ui.Center).Children(func() {
		ui.Box(c).Size(8, 8).Radius(4).Background(col).Shrink(0)
		ui.Column(c).Grow(1).Shrink(1).Gap(2).Children(func() {
			ui.Text(c, title).FontWeight(500)
			if desc != "" {
				ui.Text(c, desc).FontSize(12).TextColor(palette.muted)
			}
		})
		if control != nil {
			control()
		}
	})
}

func (a *app) engineCard(c *ui.Context, m client.Machine) {
	card(c, "ENGINE", func() {
		col, title, desc, online := palette.amber, "Starting…", "", false
		if envs := a.backend.Envs(); len(envs) > 0 {
			col, title = a.envStatus(envs[0])
			_, online = envs[0].Status.(client.Online)
		}
		switch e := m.Engine; {
		case e == nil || !online:
		case m.Embedded:
			desc = e.Name + " runs inside this app, and stops when it quits."
		default:
			desc = e.Name + " runs in another process, such as glitched serve, and this app attached to it."
		}
		statusRow(c, "engine", col, title, desc, func() {
			if m.Embedded {
				b := ui.Button(c.Key("restart"), "Restart").Disabled(a.localRuns() > 0).OnClick(func() { a.backend.Restart() })
				if a.localRuns() > 0 {
					b.Tooltip(stopFirst)
				}
			}
		})
		if e := m.Engine; e != nil {
			settingRow(c, "Host", "", func() {
				ui.Text(c, e.ID.String()).FontSize(12).Font("monospace").TextColor(palette.muted).Selectable()
			})
		}
		cfg := m.Config
		hub := "None: threads stay on this machine."
		switch {
		case cfg.HubServer != "":
			hub = "Uploads its log to the hub at " + cfg.HubServer + "."
		case cfg.HubDir != "":
			hub = "Uploads its log to the hub of the engine under " + cfg.HubDir + "."
		case cfg.Hub:
			hub = "This engine runs the hub, which keeps every machine's log."
		}
		settingRow(c, "Hub", hub, func() {})
		settingRow(c, "State directory", "", func() {
			ui.Text(c, cfg.Dir).FontSize(12).Font("monospace").TextColor(palette.muted).Selectable()
		})
	})
}

func (a *app) tailnetCard(c *ui.Context, m client.Machine) {
	card(c, "TAILNET", func() {
		ts := m.Tailscale
		if ts.IsValid() {
			statusRow(c, "tailscale", palette.green, "Tailscale is up", "This machine is "+ts.String()+" on the tailnet.", func() {
				ui.Button(c.Key("copy-ip"), "Copy").OnClick(func() { a.copy(c, ts.String()) })
			})
		} else {
			statusRow(c, "tailscale", palette.amber, "Tailscale isn't up",
				"Other machines reach this engine only once Tailscale runs here in kernel mode, with its own interface.", nil)
		}
		on := len(m.Config.Listen) > 0
		desc := "Lets other machines reach this engine with a token from below."
		busy := m.Embedded && a.localRuns() > 0
		switch {
		case busy:
			desc = stopFirst
		case !m.Embedded && m.Engine != nil:
			desc = "glitched serve applies a change here when it next starts."
		}
		settingRow(c, "Serve on the tailnet", desc, func() {
			sw := ui.Switch(c, &on).Label("Serve on the tailnet").Disabled(busy || !on && !ts.IsValid())
			if !sw.Changed() {
				return
			}
			var addrs []string
			if on {
				addrs = daemon.Serve(ts)
			}
			a.machineErr = ""
			if err := a.backend.SetListen(addrs); err != nil {
				a.machineErr = "Couldn't save engine.json: " + err.Error()
			}
		})
		for _, l := range m.Listeners {
			col, title := palette.green, "Accepting connections"
			why := ""
			if !l.Accepting {
				col, title = palette.red, "Not accepting connections"
				why = "Tailscale may not have assigned this address yet, or the engine isn't running."
			}
			statusRow(c, "listen "+l.Addr, col, title, why, func() {
				ui.Text(c, l.Addr).FontSize(12).Font("monospace").TextColor(palette.muted).Selectable()
			})
		}
	})
}

// stopFirst is why the engine can't restart while agents run here.
const stopFirst = "A change restarts the engine, which ends the turns its agents are running. Stop them first."

// localRuns counts this machine's threads whose agent is running.
func (a *app) localRuns() int {
	envs := a.backend.Envs()
	if len(envs) == 0 {
		return 0
	}
	n := 0
	for _, th := range envs[0].Threads {
		if th.View.Live() {
			n++
		}
	}
	return n
}

func (a *app) tokensCard(c *ui.Context, m client.Machine) {
	card(c, "TOKENS", func() {
		if len(m.Tokens) == 0 {
			ui.Text(c, "No tokens yet. A machine needs one to reach this engine.").Key("none").TextColor(palette.muted).Padding(16)
		}
		for _, t := range m.Tokens {
			ui.Row(c).Key("token "+t.Label).Padding(12, 12, 12, 16).Gap(12).AlignItems(ui.Center).ClipY().Transition(fold).Children(func() {
				ui.Column(c).Grow(1).Shrink(1).Gap(2).Children(func() {
					ui.Text(c, t.Label).FontWeight(500)
					ui.Text(c, "Paired "+ago(t.Created, c.Now())).FontSize(12).TextColor(palette.muted)
				})
				ui.Button(c.Key("revoke"), "Revoke").TextColor(palette.red).OnClick(func() {
					a.machineErr = ""
					if err := a.backend.Revoke(t.Label); err != nil {
						a.machineErr = err.Error()
					}
				})
			})
		}
		if a.paired.token != "" {
			a.pairedRow(c, m)
		}
		ui.Row(c).Key("pair").Padding(12, 12, 12, 16).Gap(12).AlignItems(ui.Center).Children(func() {
			ui.TextInput(c, &a.pairLabel).Placeholder("Label, such as laptop").Grow(1)
			ui.Button(c.Key("pair"), "Pair").Disabled(a.pairLabel == "").OnClick(func() {
				a.machineErr = ""
				token, err := a.backend.Pair(a.pairLabel)
				if err != nil {
					a.machineErr = err.Error()
					return
				}
				a.paired, a.pairLabel = pairing{a.pairLabel, token}, ""
			})
		})
	})
}

// pairedRow shows the token just minted, with what the other machine
// needs to reach this one.
func (a *app) pairedRow(c *ui.Context, m client.Machine) {
	p := a.paired
	addr := ""
	for _, l := range m.Listeners {
		if l.Accepting {
			addr = l.Addr
			break
		}
	}
	ui.Column(c).Key("paired").Padding(14, 16).Gap(10).Background(palette.green.Alpha(0.06)).Transition(fold).Children(func() {
		ui.Text(c, "Token for "+p.label+", shown only this once").FontWeight(500)
		ui.Text(c, p.token).FontSize(12).Font("monospace").Selectable()
		hint := "Turn on Serve on the tailnet for other machines to reach this one."
		if addr != "" {
			hint = "On the other machine, add a server at " + addr + " with this token in Settings › Servers."
		}
		ui.Text(c, hint).FontSize(12).TextColor(palette.muted)
		ui.Row(c).Gap(8).Children(func() {
			ui.Button(c.Key("copy-token"), "Copy token").OnClick(func() { a.copy(c, p.token) })
			if addr != "" {
				ui.Button(c.Key("copy-cmd"), "Copy CLI command").OnClick(func() {
					a.copy(c, "glitched run -server "+addr+" -token "+p.token+" hello")
				})
			}
			ui.Button(c.Key("done"), "Done").OnClick(func() { a.paired = pairing{} })
		})
	})
}

func (a *app) copy(c *ui.Context, text string) {
	c.WriteClipboard(text)
	c.AddToast(ui.Toast{Title: "Copied", Type: "info"})
}

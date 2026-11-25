package main

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"glitched/internal/api"
	"glitched/internal/client"
	"glitched/internal/daemon"
	eng "glitched/internal/engine"
	"glitched/internal/providerlog"
	"glitched/internal/service"
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
	a.agentsCard(c, m)
	a.linksCard(c, m)
	a.delegatesCard(c, m)
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
					a.tip(c, b, stopFirst)
				}
			}
		})
		if name := m.Tailnet.Name; name != "" {
			settingRow(c, "Host", "", func() {
				ui.Text(c, name).FontSize(12).Font("monospace").TextColor(palette.muted).Selectable()
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
		a.serviceRow(c)
	})
}

// svcSwitch is what the Background service switch is doing.
type svcSwitch int

const (
	svcIdle svcSwitch = iota
	svcInstalling
	svcRemoving
)

func (a *app) refreshService() {
	if a.svcLoading || a.svcDoing != svcIdle {
		return
	}
	a.svcLoading = true
	dir, sets := a.dir, a.svcSets
	go func() {
		st := service.Inspect(context.Background(), service.Options{Dir: dir})
		a.post(func() {
			a.svcLoading = false
			// A switch since the read began makes what it read stale.
			if a.svcSets != sets {
				return
			}
			a.svcStatus = st
			a.svcRead = time.Now()
		})
	}()
}

// setServiceInstalled installs or uninstalls the background service. The
// service waits for the engine lock, so once it is installed an engine
// embedded here hands the directory over to it.
func (a *app) setServiceInstalled(install bool) {
	if a.svcDoing != svcIdle {
		return
	}
	a.svcDoing, a.svcErr = svcRemoving, ""
	if install {
		a.svcDoing = svcInstalling
	}
	a.svcSets++
	dir := a.dir
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		// The service runs this app's own binary, which knows serve's
		// -wait-lock, not whichever glitched is first on PATH.
		opts := service.Options{Dir: dir}
		if exe, err := os.Executable(); err == nil {
			opts.Binary = exe
		}
		var st service.Status
		var err error
		if install {
			st, err = service.Install(ctx, opts)
		} else {
			st, err = service.Uninstall(ctx, opts)
		}
		a.post(func() {
			a.svcDoing = svcIdle
			a.svcStatus = st
			a.svcRead = time.Now()
			if err != nil {
				a.svcErr = err.Error()
			}
			if install && err == nil && a.backend.Machine().Embedded {
				a.backend.HandOver()
			}
			a.backend.Inspect()
		})
	}()
}

func (a *app) serviceRow(c *ui.Context) {
	if time.Since(a.svcRead) >= inspectEvery {
		a.refreshService()
	}
	st, read := a.svcStatus, !a.svcRead.IsZero()
	// Another state directory's service is that directory's to switch.
	on := st.Installed && !st.Foreign
	desc := "Keep the engine running as a user background service so agents survive closing Glitched."
	switch {
	case a.svcDoing == svcInstalling:
		on, desc = true, "Installing background service…"
	case a.svcDoing == svcRemoving:
		on, desc = false, "Removing background service…"
	case !read:
		desc = "Checking the background service…"
	case !st.Supported:
		desc = st.Detail
	case st.Foreign:
		desc = fmt.Sprintf("Installed for another state directory, %s. Manage it from there.", st.Dir)
	case st.Installed:
		desc = serviceHealth(st)
	}
	busy := a.localRuns() > 0
	settingRow(c, "Background service", desc, func() {
		sw := ui.Switch(c.Key("bg-service"), &on).Label("Enable background service").
			Disabled(a.svcDoing != svcIdle || busy || !read || !st.Supported || st.Foreign)
		if busy {
			a.tip(c, sw, stopFirst)
		}
		if sw.Changed() {
			a.setServiceInstalled(on)
		}
	})
	if a.svcErr != "" {
		ui.Text(c, a.svcErr).Key("bg-service-err").FontSize(12).TextColor(palette.red).Padding(0, 16, 12)
	}
}

// serviceHealth says whether the installed service runs.
func serviceHealth(st service.Status) string {
	switch {
	case st.Running && st.PID > 0:
		return fmt.Sprintf("Installed and running (PID %d).", st.PID)
	case st.Running:
		return "Installed and running."
	case st.Starting && st.LastExit != "":
		return "Installed, restarting: it last exited " + st.LastExit + "."
	case st.Starting:
		return "Installed and starting."
	case st.LastExit != "":
		return "Installed (stopped): it last exited " + st.LastExit + ". " + st.UnitPath
	}
	return "Installed (stopped). " + st.UnitPath
}

// limitRows are the Agents card's rows, in a.limits' order.
var limitRows = []struct {
	key, title, desc string
	hi               float64
	set              func(*eng.Limits, int)
}{
	{"limit-working", "At work at once", "Children of one thread that run together.", 100, func(l *eng.Limits, v int) { l.Working = v }},
	{"limit-all", "Children in all", "Each thread, not counting deleted ones.", 1000, func(l *eng.Limits, v int) { l.All = v }},
	{"limit-depth", "Depth", fmt.Sprintf("1 to %d below a thread of yours.", api.MaxDepth), api.MaxDepth, func(l *eng.Limits, v int) { l.Depth = v }},
}

// agentsCard caps the child threads agents start. The engine embedded
// here applies a change at once.
func (a *app) agentsCard(c *ui.Context, m client.Machine) {
	if lim := m.Config.Children.Effective(); a.limitsOf != lim {
		a.limitsOf = lim
		a.limits = [3]float64{float64(lim.Working), float64(lim.All), float64(lim.Depth)}
	}
	card(c, "AGENTS", func() {
		for i, r := range limitRows {
			settingRow(c, r.title, r.desc, func() {
				if ui.NumberInput(c.Key(r.key), &a.limits[i], 1, r.hi, 1).Label(r.title).Width(150).Changed() {
					v := int(a.limits[i])
					a.configure(func(cfg *daemon.Config) { r.set(&cfg.Children, v) })
				}
			})
		}
		compactOn := m.Config.CompactBeforeExpireEnabled()
		settingRow(c, "Compact before cache expires", "Compact idle threads over 200k tokens automatically: Claude before its prompt cache expires, and Gemini on Antigravity after 15 minutes idle.", func() {
			if ui.Switch(c.Key("compact-before-expire"), &compactOn).Label("Enable compact before cache expires").Changed() {
				v := compactOn
				a.configure(func(cfg *daemon.Config) { cfg.CompactBeforeExpire = &v })
			}
		})
		awakeOn := m.Config.KeepAwakeEnabled()
		settingRow(c, "Keep awake while agents work", "On a Mac, this machine doesn't sleep on its own while an agent on it works, and for a minute after. The display still sleeps.", func() {
			if ui.Switch(c.Key("keep-awake"), &awakeOn).Label("Enable keep awake while agents work").Changed() {
				v := awakeOn
				a.configure(func(cfg *daemon.Config) { cfg.KeepAwake = &v })
			}
		})
		logOn := m.Config.ProviderLog
		desc := "Keep the shape of what each thread's agent CLI reads and writes (which messages, fields and sizes, never their text), for Reveal provider log in the thread's menu. Up to 30 MB a thread and 512 MB in all, for 14 days."
		switch {
		case !providerlog.Supported:
			desc = "This system can't keep the provider log to its folder, so the engine keeps none."
		case !m.Embedded && m.Engine != nil && m.Engine.ProviderLog != logOn && m.Engine.ProviderLog:
			desc = "glitched serve keeps the provider log on."
		case !m.Embedded && m.Engine != nil && m.Engine.ProviderLog != logOn:
			desc = "glitched serve keeps the provider log off."
		}
		settingRow(c, "Provider log", desc, func() {
			if ui.Switch(c.Key("provider-log"), &logOn).Label("Enable provider log").Disabled(!providerlog.Supported).Changed() {
				v := logOn
				a.configure(func(cfg *daemon.Config) { cfg.ProviderLog = v })
			}
		})
		continueOn := m.Config.ContinueAfterRestart
		settingRow(c, "Continue agents after a restart", "When the engine starts again, it continues the threads of yours whose agent it cut off, two at a time, as Continue does. Not those that waited on you, were stopping, or are settled or snoozed.", func() {
			if ui.Switch(c.Key("continue-after-restart"), &continueOn).Label("Enable continue agents after a restart").Changed() {
				v := continueOn
				a.configure(func(cfg *daemon.Config) { cfg.ContinueAfterRestart = v })
			}
		})
		limitOn := m.Config.ContinueAfterLimit
		settingRow(c, "Continue agents after a usage limit", "When Claude or Codex stops a thread of yours at its usage limit and says when the limit resets, the engine continues the thread then, two at a time. The thread shows when, and you can cancel it there. Not threads that wait on you, or are settled or snoozed.", func() {
			if ui.Switch(c.Key("continue-after-limit"), &limitOn).Label("Enable continue agents after a usage limit").Changed() {
				v := limitOn
				a.configure(func(cfg *daemon.Config) { cfg.ContinueAfterLimit = v })
			}
		})
		syncOn := m.Config.SyncSkillsEnabled()
		settingRow(c, "Sync my skills to paired machines", "Threads on the machines this app reaches get your skills in ~/.glitched/skills, and theirs replace a machine's own of the same name. Turned off, each machine drops them the next time this app reaches it.", func() {
			if ui.Switch(c.Key("sync-skills"), &syncOn).Label("Enable sync my skills to paired machines").Changed() {
				v := syncOn
				a.configure(func(cfg *daemon.Config) { cfg.SyncSkills = &v })
			}
		})
		if !m.Embedded && m.Engine != nil {
			ui.Text(c, "glitched serve applies a change here when it next starts.").Key("limits-later").Padding(14, 16).FontSize(12).TextColor(palette.muted)
		}
	})
}

func (a *app) tailnetCard(c *ui.Context, m client.Machine) {
	card(c, "TAILNET", func() {
		tn := m.Tailnet
		col, title, desc := palette.amber, "Tailscale isn't installed", "Serving on the tailnet runs Glitched's own Tailscale node, which you sign in to here."
		switch {
		case tn.Mode == api.TailnetEmbedded && tn.AuthURL != "":
			col, title, desc = palette.amber, "Sign in to Tailscale", "Glitched's own node joins your tailnet once you sign in."
		case tn.Running():
			col, title = palette.green, "On the tailnet"
			desc = "As " + tn.Name
			if tn.Name == "" {
				desc = "As " + tn.IP.String()
			}
			if tn.User != "" {
				desc += ", signed in as " + tn.User
			}
			if tn.Tailnet != "" {
				desc += " to " + tn.Tailnet
			}
			desc += "."
		case tn.Mode != api.TailnetNone:
			title, desc = "Tailscale is "+strings.ToLower(tn.State), "Start Tailscale or sign in to it for other machines to reach this one."
		}
		via := map[api.TailnetMode]string{
			api.TailnetInstalled: "Through the Tailscale installed on this machine.",
			api.TailnetEmbedded:  "Through Glitched's own Tailscale node, in this engine.",
		}[tn.Mode]
		if via != "" {
			desc += " " + via
		}
		statusRow(c, "tailscale", col, title, desc, func() {
			switch {
			case tn.AuthURL != "":
				ui.Link(c, "Sign in", tn.AuthURL)
			case tn.Name != "":
				ui.Button(c.Key("copy-name"), "Copy name").OnClick(func() { a.copy(c, tn.Name) })
			}
		})

		on := m.Config.Tailnet
		desc = "Lets other machines reach this engine with a token from below."
		busy := m.Embedded && a.localRuns() > 0
		switch {
		case busy:
			desc = stopFirst
		case !m.Embedded && m.Engine != nil:
			desc = "glitched serve applies a change here when it next starts."
		case tn.Serving != "":
			desc = "Other machines reach this engine at " + tn.Serving + " with a token from below."
		case on:
			desc = "Waiting for Tailscale to be up before serving."
		}
		settingRow(c, "Serve on the tailnet", desc, func() {
			if ui.Switch(c, &on).Label("Serve on the tailnet").Disabled(busy).Changed() {
				a.configure(func(cfg *daemon.Config) { cfg.Tailnet = on })
			}
		})
		if tn.Mode != api.TailnetInstalled {
			a.controlRow(c, m, busy)
		}
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

// controlRow picks the control server Glitched's own node signs in to:
// Tailscale's, or one such as Headscale's.
func (a *app) controlRow(c *ui.Context, m client.Machine, busy bool) {
	current := m.Config.Control
	desc := "Tailscale's. Give a Headscale server's URL to use it instead."
	if current != "" {
		desc = current + ". Clear it to use Tailscale's."
	}
	settingRow(c, "Control server", desc, func() {
		ui.TextInput(c, &a.control).Placeholder("https://headscale.example.com").Font("monospace").Width(260)
		ui.Button(c.Key("use-control"), "Use").Disabled(busy || a.control == current).OnClick(func() {
			url := strings.TrimSpace(a.control)
			a.configure(func(cfg *daemon.Config) { cfg.Control = url })
		})
	})
}

func (a *app) configure(change func(*daemon.Config)) {
	a.machineErr = ""
	if err := a.backend.Configure(change); err != nil {
		a.machineErr = "Couldn't save engine.json: " + err.Error()
	}
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
		if !slices.ContainsFunc(m.Tokens, func(t daemon.Token) bool { return t.Link == nil }) {
			ui.Text(c, "No tokens yet. A machine needs one to reach this engine.").Key("none").TextColor(palette.muted).Padding(16)
		}
		for _, t := range m.Tokens {
			if t.Link != nil {
				continue
			}
			ui.Row(c).Key("token "+t.Label).Padding(12, 12, 12, 16).Gap(12).AlignItems(ui.Center).ClipY().Transition(fold).Children(func() {
				ui.Column(c).Grow(1).Shrink(1).Gap(2).Children(func() {
					ui.Text(c, t.Label).FontWeight(500)
					ui.Text(c, "Paired "+ago(t.Created, c.Now())).FontSize(12).TextColor(palette.muted)
				})
				ui.Button(c.Key("remove-token"), "Remove").Label("Remove " + t.Label).TextColor(palette.red).OnClick(func() {
					a.machineErr = ""
					if err := a.backend.RemoveToken(t.Label); err != nil {
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
	addr := servingAddr(m)
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

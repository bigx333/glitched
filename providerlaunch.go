package main

import (
	"regexp"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"glitched/internal/api"
	"glitched/internal/client"
	"glitched/internal/provider"
	"glitched/internal/transport"
)

// launchState is what Settings › Providers knows of how each machine's
// engine starts its providers' programs, and the edits not saved yet.
type launchState struct {
	machine string // the env's name whose settings the rows show; "" is this machine's
	read    map[api.ID]*launchRead
	drafts  map[launchKey]provider.Launch
	saving  map[launchKey]bool
	failed  map[launchKey]string        // why the last save didn't reach the engine
	refused map[launchKey]refusedLaunch // the last draft the engine wouldn't keep
}

// launchKey is one provider on one engine: a server re-paired to another
// engine shows that one's settings, and saves none of the old one's.
type launchKey struct {
	engine   api.ID
	provider string
}

// refusedLaunch is a draft the engine found wrong, and what was wrong.
type refusedLaunch struct {
	launch provider.Launch
	errs   provider.LaunchError
}

// launchRead is one engine's last answer. rev counts the saves since
// asked: an answer asked before a save is older than the save's.
type launchRead struct {
	asking bool
	at     time.Time
	rev    int
	infos  map[string]transport.LaunchInfo
	err    error
}

// launchesFresh is how long an engine's answer stands while the page shows.
const launchesFresh = 30 * time.Second

func (a *app) launchState() *launchState {
	if a.launches.read == nil {
		a.launches = launchState{read: map[api.ID]*launchRead{}, drafts: map[launchKey]provider.Launch{}, saving: map[launchKey]bool{}, failed: map[launchKey]string{}, refused: map[launchKey]refusedLaunch{}}
	}
	return &a.launches
}

func (s *launchState) invalidate(engine api.ID) {
	if r := s.read[engine]; r != nil {
		r.rev++
		r.at = time.Time{}
	}
}

// launchMachines are the engines a row's launch settings can be of: this
// machine's, then each server that is online.
func (a *app) launchMachines() []*client.Env {
	var out []*client.Env
	for _, e := range a.backend.Envs() {
		if _, online := e.Status.(client.Online); online || e.Kind == client.Local {
			out = append(out, e)
		}
	}
	return out
}

// machineName is e as the machine choice labels it, and machineWords as
// a sentence names it.
func machineName(e *client.Env) string {
	if e.Kind == client.Local {
		return "This machine"
	}
	return e.Name
}

func machineWords(e *client.Env) string {
	if e.Kind == client.Local {
		return "this machine"
	}
	return e.Name
}

// launchEnv is the machine the rows show, this one when the chosen one
// went offline.
func (a *app) launchEnv() *client.Env { return a.machineNamed(a.launchState().machine) }

// machineNamed is the online server called name, or this machine.
func (a *app) machineNamed(name string) *client.Env {
	envs := a.launchMachines()
	for _, e := range envs {
		if e.Kind != client.Local && e.Name == name {
			return e
		}
	}
	if len(envs) > 0 {
		return envs[0]
	}
	return nil
}

// machineChoice picks e among envs, and keeps the server's name in
// chosen, "" for this machine.
func machineChoice(c *ui.Context, envs []*client.Env, e *client.Env, chosen *string) {
	names := make([]string, len(envs))
	i := 0
	for j, m := range envs {
		names[j] = machineName(m)
		if m == e {
			i = j
		}
	}
	if ui.Segmented(c.Key("machine"), &i, names...).Label("Machine").Changed() {
		*chosen = map[bool]string{true: envs[i].Name}[envs[i].Kind != client.Local]
	}
}

// launchRead is e's engine's answer, asked again once it's old. An
// engine that hasn't answered the env yet is still being asked.
func (a *app) askLaunches(e *client.Env) *launchRead {
	s := a.launchState()
	if e.ID.IsZero() {
		return &launchRead{}
	}
	r := s.read[e.ID]
	if r == nil {
		r = &launchRead{}
		s.read[e.ID] = r
	}
	if r.asking || time.Since(r.at) < launchesFresh {
		return r
	}
	r.asking = true
	rev := r.rev
	a.backend.Launches(e, e.ID, func(infos []transport.LaunchInfo, err error) {
		r.asking = false
		if r.rev != rev {
			return // a save answered since; the next frame asks again
		}
		r.at, r.err = time.Now(), err
		if err == nil {
			r.infos = map[string]transport.LaunchInfo{}
			for _, info := range infos {
				r.infos[info.Provider] = info
			}
		}
	})
	return r
}

// providerLaunch is where a provider's program is and how it starts, on
// the machine the page shows: the settings that machine's engine keeps.
func (a *app) providerLaunch(c *ui.Context, r providerRow) {
	s := a.launchState()
	envs := a.launchMachines()
	e := a.launchEnv()
	if e == nil {
		return
	}
	read := a.askLaunches(e)
	c.After(launchesFresh)
	info, ok := read.infos[r.family.provider]
	inset(c, "LAUNCH", func() {
		if len(envs) > 1 {
			optionRow(c, "Machine", "Each machine's engine keeps its own launch settings. These are the chosen one's.", func() {
				machineChoice(c, envs, e, &s.machine)
			})
		}
		switch {
		case !ok && read.err != nil:
			ui.Text(c, machineWords(e)+" didn't say how it starts "+r.family.name+": "+read.err.Error()).Key("launch-err").FontSize(12).TextColor(palette.red).Padding(12, 16)
			return
		case !ok && read.at.IsZero():
			ui.Text(c, "Asking "+machineWords(e)+"…").Key("launch-asking").FontSize(12).TextColor(palette.muted).Padding(12, 16)
			return
		case !ok:
			ui.Text(c, capitalized(machineWords(e))+" doesn't run "+r.family.name+".").Key("launch-none").FontSize(12).TextColor(palette.muted).Padding(12, 16)
			return
		}
		a.launchFields(c, e, launchKey{e.ID, r.family.provider}, r.family.name, machineWords(e), info)
	})
}

func (a *app) launchFields(c *ui.Context, e *client.Env, k launchKey, name, machine string, info transport.LaunchInfo) {
	s := a.launchState()
	draft, editing := s.drafts[k]
	if !editing {
		draft = info.Launch
	}
	was := draft
	errs := info.Errors
	if draft != info.Launch {
		errs = provider.LaunchError{}
		if rf, ok := s.refused[k]; ok && rf.launch == draft {
			errs = rf.errs
		}
	}
	errOf := func(msg string) {
		if msg != "" {
			ui.Text(c, msg).Selectable().FontSize(12).TextColor(palette.red)
		}
	}
	ui.Column(c).Padding(12, 16).Gap(14).Children(func() {
		pathDesc := "Empty finds " + name + " on " + machine + "'s PATH, then in ~/.local/bin."
		if info.Launch.Path == "" {
			if info.Path != "" {
				pathDesc = "Empty finds it on PATH: " + displayPath(info.Path) + "."
			} else {
				pathDesc = "Empty finds it on PATH, where " + machine + " has none."
			}
		}
		ui.Field(c, "Program", func() {
			ui.TextInput(c.Key("path"), &draft.Path).Placeholder(cmpOr(displayPath(info.Path), "/usr/local/bin/"+name)).Font("monospace")
		}).Description(pathDesc)
		errOf(errs.Path)
		if info.HomeVar != "" {
			ui.Field(c, info.HomeVar, func() {
				ui.TextInput(c.Key("home"), &draft.Home).Placeholder(info.DefaultHome).Font("monospace")
			}).Description(name + "'s own folder of settings, sign-in and sessions, set as " + info.HomeVar + " for every launch. Empty is " + info.DefaultHome + ".")
			errOf(errs.Home)
			if draft.Home != info.Launch.Home {
				ui.Text(c, "Threads that ran in the old folder won't find their sessions in this one: each starts a new conversation without its earlier context.").Key("home-warn").FontSize(12).TextColor(palette.amber)
			}
		}
		ui.Field(c, "Launch arguments", func() {
			ui.TextInput(c.Key("args"), &draft.Args).Placeholder("--flag value").Font("monospace")
		}).Description("Added to every launch, in shell quoting. Glitched sets these itself: " + strings.Join(info.Reserved, ", ") + ".")
		errOf(errs.Args)
		if len(info.Command) > 0 {
			ui.Column(c).Gap(4).Children(func() {
				ui.Text(c, "A new thread starts").FontSize(12).FontWeight(500).TextColor(palette.muted)
				ui.Text(c, shellLine(info.Env, info.Command)).Key("command").Selectable().Font("monospace").FontSize(11.5).TextColor(palette.text)
			})
		}
		ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
			note := "Saved settings apply to the next launch on " + machine + ". Running turns keep how they started."
			if f := s.failed[k]; f != "" {
				ui.Text(c, f).FontSize(12).TextColor(palette.red).Grow(1).Shrink(1)
			} else {
				ui.Text(c, note).FontSize(12).TextColor(palette.faint).Grow(1).Shrink(1)
			}
			changed := draft != info.Launch
			if changed {
				ui.Button(c.Key("revert"), "Revert").Disabled(s.saving[k]).OnClick(func() {
					delete(s.drafts, k)
					delete(s.failed, k)
					delete(s.refused, k)
				})
			}
			label := "Save"
			if s.saving[k] {
				label = "Checking…"
			}
			ui.Button(c.Key("save"), label).Disabled(!changed || s.saving[k]).OnClick(func() { a.saveLaunch(e, k, s.drafts[k]) })
		})
	})
	if draft != was {
		s.drafts[k] = draft
	}
}

// saveLaunch has k's engine, e's, check l and keep it when nothing is
// wrong, then asks every provider of this machine again, as one may run
// now. What is typed meanwhile stays a draft.
func (a *app) saveLaunch(e *client.Env, k launchKey, l provider.Launch) {
	s := a.launchState()
	r := s.read[k.engine]
	if r == nil {
		r = &launchRead{}
		s.read[k.engine] = r
	}
	r.rev++
	s.saving[k] = true
	delete(s.failed, k)
	a.backend.SetLaunch(e, k.engine, k.provider, l, func(info transport.LaunchInfo, err error) {
		s.saving[k] = false
		if err != nil {
			s.failed[k] = "Couldn't save: " + err.Error()
			return
		}
		if !info.Saved {
			s.refused[k] = refusedLaunch{l, info.Errors}
			return
		}
		r.rev++
		if r.infos == nil {
			r.infos = map[string]transport.LaunchInfo{}
		}
		r.infos[k.provider] = info
		delete(s.refused, k)
		if s.drafts[k] == l {
			delete(s.drafts, k)
		}
		if e.Kind == client.Local {
			a.checkProviders()
		}
	})
}

func capitalized(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

var plainWord = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./~-]+$`)

// shellLine is env and argv as a shell would take them.
func shellLine(env, argv []string) string {
	quote := func(w string) string {
		if plainWord.MatchString(w) {
			return w
		}
		return "'" + strings.ReplaceAll(w, "'", `'\''`) + "'"
	}
	var words []string
	for _, kv := range env {
		name, value, _ := strings.Cut(kv, "=")
		words = append(words, name+"="+quote(value))
	}
	for _, w := range argv {
		words = append(words, quote(w))
	}
	return strings.Join(words, " ")
}

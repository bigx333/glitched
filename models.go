package main

import (
	"cmp"
	"context"
	"encoding/json"
	"log"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"glitched/internal/client"
	"glitched/internal/jsonfile"
	"glitched/internal/provider"
	"glitched/internal/transport"
)

// modelKey names a model end to end: the provider that runs it, as a
// thread's api.ModelRef names it, and its id there. A model's name is only
// what the picker shows of it.
type modelKey struct {
	Provider string `json:"provider"`
	ID       string `json:"id"`
}

// String is k as a row of the picker is keyed: no provider's name holds a
// colon.
func (k modelKey) String() string { return k.Provider + ":" + k.ID }

// UnmarshalJSON reads a key, or a name Glitched gave a model before it
// named models by key, as composer.json held them.
func (k *modelKey) UnmarshalJSON(b []byte) error {
	var name string
	if json.Unmarshal(b, &name) == nil {
		*k = modelKey{}
		if m, ok := knownNamed(name); ok {
			*k = m.key
		}
		return nil
	}
	type plain modelKey
	return json.Unmarshal(b, (*plain)(k))
}

// modelLists are the models each provider's engine last reported, as
// models.json keeps them across restarts. A provider offers what its
// engine said, though that's none, as of a provider it has no adapter
// for; one its engine never answered for offers none yet.
type modelLists struct {
	reported map[string][]provider.Model // by provider
	asking   bool
	next     time.Time // when to ask again
}

// modelsRecheck is how long the engine's answer stands: a model the
// account gains since shows after it, or after a restart. modelsRetry is
// how soon an engine that didn't answer is asked again; what it said
// before stands meanwhile.
const (
	modelsRecheck = 10 * time.Minute
	modelsRetry   = 15 * time.Second
)

func (a *app) modelsPath() string { return filepath.Join(a.dir, "models.json") }

func (a *app) loadModels() {
	a.models = modelLists{reported: map[string][]provider.Model{}}
	if err := jsonfile.Load(a.modelsPath(), &a.models.reported); err != nil {
		log.Printf("glitched: models: %v", err)
	}
	if a.models.reported == nil {
		a.models.reported = map[string][]provider.Model{} // a file of null
	}
}

// askModels asks this machine's engine, off the UI thread, for the models
// of every provider it serves, once its answer is old. The picker shows
// what it has meanwhile, and what changes as the answer comes. A provider
// that couldn't say keeps what it said before.
func (a *app) askModels() {
	l := &a.models
	if envs := a.backend.Envs(); l.asking || time.Now().Before(l.next) || len(envs) == 0 || envs[0].Status != client.Status(client.Online{}) {
		return // an engine coming online builds the picker again
	}
	l.asking = true
	engine := a.serviceClient(a.dir)
	go func() {
		// Claude Code's answer takes several seconds, and up to its own
		// timeout when claude hangs.
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		served, err := engine.Providers(ctx)
		cancel()
		a.post(func() {
			l.asking = false
			retry := func() {
				l.next = time.Now().Add(modelsRetry)
				time.AfterFunc(modelsRetry, func() { a.post(func() {}) })
			}
			if err != nil {
				retry()
				return
			}
			l.next = time.Now().Add(modelsRecheck)
			for _, f := range a.providerFamilies() {
				if !slices.ContainsFunc(served, func(p transport.ProviderInfo) bool { return p.Name == f.provider }) {
					l.reported[f.provider] = []provider.Model{}
				}
			}
			for _, p := range served {
				if p.Error != "" {
					log.Printf("glitched: models: %s: %s", p.Name, p.Error)
					retry()
					continue
				}
				l.reported[p.Name] = append([]provider.Model{}, p.Info.Models...)
			}
			if err := jsonfile.Write(a.modelsPath(), l.reported); err != nil {
				log.Printf("glitched: models: %v", err)
			}
			// A default the user left as it was takes what the engine
			// now offers: the engine's first model where none was chosen,
			// an effort the model runs at.
			if a.choice() == a.savedChoice && !a.effortHeld {
				a.show(a.starting(a.composerFile))
				a.savedChoice, a.triedChoice = a.choice(), a.choice()
			}
		})
	}()
}

// providerFamilies are the families of Glitched's table, then those of
// any other provider an engine reported, by its key.
func (a *app) providerFamilies() []family {
	out := slices.Clone(families)
	var others []string
	for p := range a.models.reported {
		if !slices.ContainsFunc(families, func(f family) bool { return f.provider == p }) {
			others = append(others, p)
		}
	}
	slices.Sort(others)
	for _, p := range others {
		out = append(out, familyOf(p))
	}
	return out
}

// offeredModels are the models the engines offer, family by family in
// providerFamilies' order, each in its engine's order: as Glitched knows
// it, else as its engine words it. Each has a name of its own, as the
// settings choose the default by it: a name another took is its key.
func (a *app) offeredModels() []modelInfo {
	var out []modelInfo
	taken := map[string]bool{}
	for _, f := range a.providerFamilies() {
		for _, r := range a.models.reported[f.provider] {
			key := modelKey{f.provider, r.ID}
			if slices.ContainsFunc(out, func(m modelInfo) bool { return m.key == key }) {
				continue
			}
			m, known := knownModel(key)
			if !known {
				short := cmp.Or(r.Name, r.ID)
				m = modelInfo{key: key, name: f.name + " " + short, short: short, desc: r.Description, color: f.color, icon: f.icon}
			}
			if taken[m.name] {
				m.name = key.String()
			}
			taken[m.name] = true
			m.efforts = nil
			for _, e := range r.Efforts {
				m.efforts = append(m.efforts, effortOf(e))
			}
			out = append(out, m)
		}
	}
	return out
}

// familyModels are the models offered of family f.
func (a *app) familyModels(f family) []modelInfo {
	return slices.DeleteFunc(a.offeredModels(), func(m modelInfo) bool { return m.key.Provider != f.provider })
}

func knownModel(k modelKey) (modelInfo, bool) {
	i := slices.IndexFunc(knownModels, func(m modelInfo) bool { return m.key == k })
	if i < 0 {
		return modelInfo{}, false
	}
	return knownModels[i], true
}

func knownNamed(name string) (modelInfo, bool) {
	i := slices.IndexFunc(knownModels, func(m modelInfo) bool { return m.name == name })
	if i < 0 {
		return modelInfo{}, false
	}
	return knownModels[i], true
}

// modelNamed is the offered model named name: the settings' default model
// is chosen by name.
func (a *app) modelNamed(name string) (modelInfo, bool) {
	i := slices.IndexFunc(a.offeredModels(), func(m modelInfo) bool { return m.name == name })
	if i < 0 {
		return modelInfo{}, false
	}
	return a.offeredModels()[i], true
}

// modelNames is every offered model's name, and the default's where none
// offers it any more, for the settings' choice.
func (a *app) modelNames() []string {
	var names []string
	for _, m := range a.offeredModels() {
		names = append(names, m.name)
	}
	if m, offered := a.modelOf(a.model); !offered {
		names = append(names, m.name)
	}
	return names
}

// modelOf is the model k names, and whether its engine offers it; else
// the one Glitched knew by k; else one shown by its id alone, of k's
// family.
func (a *app) modelOf(k modelKey) (modelInfo, bool) {
	if i := slices.IndexFunc(a.offeredModels(), func(m modelInfo) bool { return m.key == k }); i >= 0 {
		return a.offeredModels()[i], true
	}
	if m, ok := knownModel(k); ok {
		return m, false
	}
	f := familyOf(k.Provider)
	name := cmp.Or(k.ID, "Default model")
	return modelInfo{key: k, name: name, short: name, color: f.color, icon: f.icon}, false
}

// keyOfThread is the key of the model a thread of provider names: by its
// engine's id, or by the name Glitched gave it before it sent ids.
func keyOfThread(provider, model string) modelKey {
	if m, ok := knownNamed(model); ok && m.key.Provider == provider {
		return m.key
	}
	return modelKey{provider, model}
}

// effortInfo is a level of effort as the picker shows it: its id, which a
// thread's api.ModelRef names it by, its label and color, and how much of
// the model button's glow it lights.
type effortInfo struct {
	id, label string
	color     ui.Color
	glow      float32
}

// knownEfforts are how the picker shows the efforts Glitched knows.
var knownEfforts = []effortInfo{
	{"low", "Low", ui.Hex("#60a5fa"), 0},
	{"medium", "Medium", ui.Hex("#a78bfa"), 0},
	{"high", "High", ui.Hex("#f472b6"), 0.55},
	{"xhigh", "Extra high", ui.Hex("#fb7185"), 0.8},
	{"max", "Max", ui.Hex("#f87171"), 1},
}

// defaultEfforts are a model's whose engine doesn't say.
var defaultEfforts = []string{"low", "medium", "high", "max"}

// effortOf is e as the picker shows it: as Glitched knows it, else in its
// engine's word, in the slider's middle color.
func effortOf(e provider.Effort) effortInfo {
	if i := slices.IndexFunc(knownEfforts, func(k effortInfo) bool { return k.id == e.ID }); i >= 0 {
		return knownEfforts[i]
	}
	return effortInfo{id: e.ID, label: cmp.Or(e.Label, e.ID), color: knownEfforts[1].color}
}

// effortsOf are the efforts m runs at, least first.
func effortsOf(m modelInfo) []effortInfo {
	if len(m.efforts) > 0 {
		return m.efforts
	}
	var out []effortInfo
	for _, id := range defaultEfforts {
		out = append(out, effortOf(provider.Effort{ID: id}))
	}
	return out
}

// effortFor is the effort m runs at for want: want, or the effort want
// names by its label, as composer.json and threads held them before; else
// Medium, else the least m runs at. The picker shows it, and sends it.
func effortFor(m modelInfo, want string) string {
	es := effortsOf(m)
	for _, id := range []string{want, strings.ToLower(want), "medium"} {
		if slices.ContainsFunc(es, func(e effortInfo) bool { return e.id == id }) {
			return id
		}
	}
	return es[0].id
}

// chooseModel chooses the model k, at the effort chosen where k runs at
// it, else at the one it shows (effortFor).
func (a *app) chooseModel(k modelKey) {
	a.model = k
	m, _ := a.modelOf(k)
	a.effort = effortFor(m, a.effort)
}

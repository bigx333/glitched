package main

import (
	"cmp"
	"context"
	"log"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"glitched/internal/client"
	"glitched/internal/jsonfile"
	"glitched/internal/provider"
)

// modelLists are the models each family's engine last reported, as
// models.json keeps them across restarts. A family whose engine never
// answered offers the ones Glitched knows of it (knownModels); one that
// answered offers what it said, though that's none.
type modelLists struct {
	reported map[string][]provider.Model // by provider, as a thread's api.ModelRef names it
	asking   map[string]bool
	next     map[string]time.Time // when to ask again
}

// modelsRecheck is how long a provider's answer stands: a model the
// account gains since shows after it, or after a restart. modelsRetry is
// how soon one that didn't answer, as an engine still starting, is asked
// again; what it said before stands meanwhile.
const (
	modelsRecheck = 10 * time.Minute
	modelsRetry   = 15 * time.Second
)

func (a *app) modelsPath() string { return filepath.Join(a.dir, "models.json") }

func (a *app) loadModels() {
	a.models = modelLists{reported: map[string][]provider.Model{}, asking: map[string]bool{}, next: map[string]time.Time{}}
	if err := jsonfile.Load(a.modelsPath(), &a.models.reported); err != nil {
		log.Printf("glitched: models: %v", err)
	}
}

// askModels asks this machine's engine, off the UI thread, for the models
// of each family's provider whose answer is old. The picker shows what it
// has meanwhile, and what changes as the answer comes.
func (a *app) askModels() {
	if envs := a.backend.Envs(); len(envs) == 0 || envs[0].Status != client.Status(client.Online{}) {
		return // asked once it is, which builds the picker again
	}
	l := &a.models
	for _, f := range families {
		p := strings.ToLower(f.name)
		if l.asking[p] || time.Now().Before(l.next[p]) {
			continue
		}
		l.asking[p] = true
		engine := a.serviceClient(a.dir)
		go func() {
			// Claude Code's answer takes several seconds, and up to its
			// own timeout when claude hangs.
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			info, err := engine.ProviderInfo(ctx, p)
			cancel()
			a.post(func() {
				l.asking[p] = false
				if err != nil {
					l.next[p] = time.Now().Add(modelsRetry)
					time.AfterFunc(modelsRetry, func() { a.post(func() {}) })
					return
				}
				l.next[p] = time.Now().Add(modelsRecheck)
				l.reported[p] = append([]provider.Model{}, info.Models...)
				if err := jsonfile.Write(a.modelsPath(), l.reported); err != nil {
					log.Printf("glitched: models: %v", err)
				}
			})
		}()
	}
}

// familyModels are the models the picker offers of family f, in its
// engine's order: each as Glitched knows it, else as its engine words it.
func (a *app) familyModels(f family) []modelInfo {
	reported, ok := a.models.reported[strings.ToLower(f.name)]
	var out []modelInfo
	if !ok {
		for _, m := range knownModels {
			if m.provider == f.name {
				out = append(out, m)
			}
		}
		return out
	}
	taken := map[string]bool{}
	for _, r := range reported {
		m, known := knownModel(r.ID)
		if !known {
			short := cmp.Or(r.Name, r.ID)
			m = modelInfo{id: r.ID, name: f.name + " " + short, short: short, desc: r.Description, provider: f.name, color: f.color, icon: f.icon}
		}
		if taken[m.name] {
			m.name = r.ID
		}
		taken[m.name] = true
		out = append(out, m)
	}
	return out
}

func knownModel(id string) (modelInfo, bool) {
	for _, m := range knownModels {
		if m.id == id {
			return m, true
		}
	}
	return modelInfo{}, false
}

// offeredModels are every family's, in the families' order.
func (a *app) offeredModels() []modelInfo {
	var out []modelInfo
	for _, f := range families {
		out = append(out, a.familyModels(f)...)
	}
	return out
}

// modelNames is every offered model's name, for the settings' choice.
func (a *app) modelNames() []string {
	var names []string
	for _, m := range a.offeredModels() {
		names = append(names, m.name)
	}
	return names
}

// defaultModels are what the settings' default model chooses from: the
// offered models, and the default where none offers it any more.
func (a *app) defaultModels() []string {
	names := a.modelNames()
	if !slices.Contains(names, a.model) {
		names = append(names, a.model)
	}
	return names
}

// modelByName is the offered model named name, and whether it's offered;
// else the one Glitched knew by that name; else one of family by that
// name alone, which goes to its engine as it is.
func (a *app) modelByName(name, family string) (modelInfo, bool) {
	for _, m := range a.offeredModels() {
		if m.name == name {
			return m, true
		}
	}
	for _, m := range knownModels {
		if m.name == name {
			return m, false
		}
	}
	f := familyNamed(family)
	return modelInfo{id: name, name: name, short: name, provider: f.name, color: f.color, icon: f.icon}, false
}

// modelOfThread is the name the picker gives the model a thread of family
// names: by its engine's id, or by the name Glitched gave it before it
// sent ids.
func (a *app) modelOfThread(family, model string) string {
	for _, m := range append(a.familyModels(familyNamed(family)), knownModels...) {
		if m.id == model {
			return m.name
		}
	}
	return model
}

// familyNamed is the family named name, else the first.
func familyNamed(name string) family {
	for _, f := range families {
		if f.name == name {
			return f
		}
	}
	return families[0]
}

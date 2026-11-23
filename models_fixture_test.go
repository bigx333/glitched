package main

import (
	"context"
	"errors"

	"glitched/internal/provider"
	"glitched/internal/provider/acp"
	"glitched/internal/provider/claude"
	"glitched/internal/provider/codex"
)

// reporting is an adapter whose provider reports models, as a family's
// engine does.
type reporting struct {
	provider.Adapter
	models []provider.Model
}

func (r reporting) Describe(ctx context.Context) (provider.Info, error) {
	info, err := r.Adapter.Describe(ctx)
	info.Models = r.models
	return info, err
}

func (r reporting) Commands(ctx context.Context, workspace string) ([]provider.Command, error) {
	if c, ok := r.Adapter.(provider.Commander); ok {
		return c.Commands(ctx, workspace)
	}
	return nil, nil
}

// undescribed is an adapter whose provider can't say what it runs.
type undescribed struct{ provider.Adapter }

func (undescribed) Describe(context.Context) (provider.Info, error) {
	return provider.Info{}, errors.New("no answer")
}

// todaysModels has each family's provider in m report the models the
// picker listed before it asked engines, by the ids Glitched knows them
// by: the fixture on which the picker looks as it did. A provider that
// reports its own already, or a real adapter, is left as it is.
func todaysModels(m provider.Mux) {
	for _, f := range families {
		p := f.provider
		switch m[p].(type) {
		case nil, reporting, shifting, undescribed, *claude.Adapter, *acp.Adapter, *codex.Adapter, provider.Placed:
			continue
		}
		m[p] = reporting{m[p], knownOf(p)}
	}
}

// knownOf are the models the picker listed of provider p before it asked
// engines.
func knownOf(p string) []provider.Model {
	var models []provider.Model
	for _, k := range knownModels {
		if k.key.Provider == p {
			models = append(models, provider.Model{ID: k.key.ID, Name: k.short})
		}
	}
	return models
}

// keyNamed is the key of the model Glitched knows by name.
func keyNamed(name string) modelKey {
	m, _ := knownNamed(name)
	return m.key
}

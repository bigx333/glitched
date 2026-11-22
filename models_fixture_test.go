package main

import (
	"context"
	"strings"

	"glitched/internal/provider"
	"glitched/internal/provider/claude"
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

// todaysModels has each family's provider in m report the models the
// picker listed before it asked engines, by the ids Glitched knows them
// by: the fixture on which the picker looks as it did. A provider that
// reports its own already, or the real claude, is left as it is.
func todaysModels(m provider.Mux) {
	for _, f := range families {
		p := strings.ToLower(f.name)
		switch m[p].(type) {
		case nil, reporting, *claude.Adapter:
			continue
		}
		var models []provider.Model
		for _, k := range knownModels {
			if k.provider == f.name {
				models = append(models, provider.Model{ID: k.id, Name: k.short})
			}
		}
		m[p] = reporting{m[p], models}
	}
}

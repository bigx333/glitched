package provider

import (
	"context"
	"errors"
	"fmt"
)

// ErrNoAdapter is what a Mux returns for a provider it has no adapter for.
var ErrNoAdapter = errors.New("provider: no adapter")

// Mux is the adapters a host serves, by the provider name a thread's
// api.ModelRef carries. One adapter may serve several names.
type Mux map[string]Adapter

func (m Mux) adapter(name string) (Adapter, error) {
	if a, ok := m[name]; ok {
		return a, nil
	}
	return nil, fmt.Errorf("%w for %q on this engine", ErrNoAdapter, name)
}

func (m Mux) Describe(ctx context.Context, name string) (Info, error) {
	a, err := m.adapter(name)
	if err != nil {
		return Info{}, err
	}
	return a.Describe(ctx)
}

func (m Mux) Open(ctx context.Context, spec SessionSpec) (Agent, error) {
	a, err := m.adapter(spec.Model.Provider)
	if err != nil {
		return nil, err
	}
	return a.Open(ctx, spec)
}

// Reconcile answers Lost for a provider with no adapter: no agent of it can
// be found, and an error would only be retried.
func (m Mux) Reconcile(ctx context.Context, id RuntimeIdentity) (Recovery, error) {
	a, err := m.adapter(id.Provider)
	if err != nil {
		return Lost{Reason: err.Error()}, nil
	}
	return a.Reconcile(ctx, id)
}

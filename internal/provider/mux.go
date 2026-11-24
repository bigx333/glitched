package provider

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
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
	return nil, fmt.Errorf("%w for %q on this engine, which has %s", ErrNoAdapter, name, strings.Join(m.Names(), ", "))
}

// Names are the providers m serves, sorted.
func (m Mux) Names() []string {
	return slices.Sorted(maps.Keys(m))
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

// Reconcile fails for a provider with no adapter rather than answer Lost: its
// agent may still run, and a later engine that has the adapter can close it.
func (m Mux) Reconcile(ctx context.Context, id RuntimeIdentity) (Recovery, error) {
	a, err := m.adapter(id.Provider)
	if err != nil {
		return nil, err
	}
	return a.Reconcile(ctx, id)
}

// Rewind rewinds a conversation of name's, or answers a portable Rewind
// for an adapter that can't.
func (m Mux) Rewind(ctx context.Context, name string, spec RewindSpec) (Rewind, error) {
	a, err := m.adapter(name)
	if err != nil {
		return Rewind{}, err
	}
	if r, ok := a.(Rewinder); ok {
		return r.Rewind(ctx, spec)
	}
	return Rewind{}, nil
}

// Commands are the slash commands of name's sessions in workspace, or
// none for an adapter that can't list them.
func (m Mux) Commands(ctx context.Context, name, workspace string) ([]Command, error) {
	a, err := m.adapter(name)
	if err != nil {
		return nil, err
	}
	if c, ok := a.(Commander); ok {
		return c.Commands(ctx, workspace)
	}
	return nil, nil
}

// DefaultModel is the model a session of name's in workspace runs where
// it names none, "" where its adapter can't say (Defaulter).
func (m Mux) DefaultModel(ctx context.Context, name, workspace string) (string, error) {
	a, err := m.adapter(name)
	if err != nil {
		return "", err
	}
	if d, ok := a.(Defaulter); ok {
		return d.DefaultModel(ctx, workspace)
	}
	return "", nil
}

// Ask asks name's adapter for one answer within spec's timeout,
// ErrUnsupported from one that can't.
func (m Mux) Ask(ctx context.Context, name string, spec AskSpec) (AskResult, error) {
	a, err := m.adapter(name)
	if err != nil {
		return AskResult{}, err
	}
	q, ok := a.(Asker)
	if !ok {
		return AskResult{}, fmt.Errorf("%w: %s can't answer a prompt on its own", ErrUnsupported, name)
	}
	if spec.Timeout <= 0 {
		return AskResult{}, fmt.Errorf("an ask's timeout is %s; it needs time to run", spec.Timeout)
	}
	ctx, cancel := context.WithTimeout(ctx, spec.Timeout)
	defer cancel()
	return q.Ask(ctx, spec)
}

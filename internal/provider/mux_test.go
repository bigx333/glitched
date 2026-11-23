package provider_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/provider"
	"glitched/internal/provider/fake"
)

type named struct {
	*fake.Adapter
	name string
}

func (n named) Describe(context.Context) (provider.Info, error) {
	return provider.Info{Name: n.name}, nil
}

func TestMuxAsksOnlyAnAdapterThatAnswers(t *testing.T) {
	m := provider.Mux{"fake": fake.New(nil)}
	if _, err := m.Ask(context.Background(), "fake", provider.AskSpec{Prompt: "p"}); !errors.Is(err, provider.ErrUnsupported) {
		t.Errorf("Ask of an adapter with no Ask: %v, want ErrUnsupported", err)
	}
	if _, err := m.Ask(context.Background(), "nope", provider.AskSpec{Prompt: "p"}); !errors.Is(err, provider.ErrNoAdapter) {
		t.Errorf("Ask of an unknown provider: %v, want ErrNoAdapter", err)
	}
}

type deadlined struct {
	*fake.Adapter
	left time.Duration
}

func (d *deadlined) Ask(ctx context.Context, _ provider.AskSpec) (provider.AskResult, error) {
	if at, ok := ctx.Deadline(); ok {
		d.left = time.Until(at)
	}
	return provider.AskResult{}, nil
}

// An ask's timeout bounds it in the engine, where the caller's deadline
// doesn't reach.
func TestMuxBoundsAnAskByItsTimeout(t *testing.T) {
	d := &deadlined{Adapter: fake.New(nil)}
	m := provider.Mux{"d": d}
	if _, err := m.Ask(context.Background(), "d", provider.AskSpec{Prompt: "p", Timeout: 7 * time.Second}); err != nil {
		t.Fatal(err)
	}
	if d.left > 7*time.Second || d.left < 6*time.Second {
		t.Errorf("the ask may take %s, want 7s", d.left)
	}
}

func TestMuxRoutesByProviderName(t *testing.T) {
	ctx := context.Background()
	m := provider.Mux{"a": named{fake.New(nil), "A"}, "b": named{fake.New(nil), "B"}}
	for name, want := range map[string]string{"a": "A", "b": "B"} {
		if info, err := m.Describe(ctx, name); err != nil || info.Name != want {
			t.Errorf("Describe(%q) = %+v, %v; want %s's adapter", name, info, err, want)
		}
	}

	if _, err := m.Describe(ctx, "nope"); !errors.Is(err, provider.ErrNoAdapter) {
		t.Errorf("Describe of an unknown provider: %v, want ErrNoAdapter", err)
	}
	spec := provider.SessionSpec{Thread: api.NewID(), Run: api.NewID(), Model: api.ModelRef{Provider: "nope"}}
	if _, err := m.Open(ctx, spec); !errors.Is(err, provider.ErrNoAdapter) {
		t.Errorf("Open on an unknown provider: %v, want ErrNoAdapter", err)
	}
	if _, err := m.Reconcile(ctx, provider.RuntimeIdentity{Thread: spec.Thread, Run: spec.Run, Provider: "nope"}); !errors.Is(err, provider.ErrNoAdapter) {
		t.Errorf("Reconcile on an unknown provider: %v, want ErrNoAdapter, since its agent may still run", err)
	}
}

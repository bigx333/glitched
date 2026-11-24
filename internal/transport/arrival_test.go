package transport

import (
	"context"
	"testing"
	"time"

	"glitched/internal/api"
)

// arrivalEndpoint is an engine that answers each skills call with nothing, and
// says when each arrived.
type arrivalEndpoint struct {
	*fakeEndpoint
	got chan uint64
}

func (a arrivalEndpoint) Skills(ctx context.Context, _ api.SkillQuery) (api.SkillList, error) {
	a.got <- api.Arrival(ctx)
	return api.SkillList{}, nil
}

func (a arrivalEndpoint) SyncSkills(ctx context.Context, _ api.SkillSync) (api.SkillsSynced, error) {
	a.got <- api.Arrival(ctx)
	return api.SkillsSynced{}, nil
}

func (a arrivalEndpoint) PutSkill(ctx context.Context, _ api.SkillPut) error {
	a.got <- api.Arrival(ctx)
	return nil
}

func (a arrivalEndpoint) DropSkills(ctx context.Context, _ api.SkillDrop) error {
	a.got <- api.Arrival(ctx)
	return nil
}

// Each call a server reads arrives after every one it read before, on its
// socket or another: a drop sent after a sync, though the sync's handler
// runs later, says it came later.
func TestACallArrivesAfterTheCallsReadBeforeIt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	fake := &fakeEndpoint{env: api.Environment{ID: api.NewID(), Log: api.NewID()}}
	e := arrivalEndpoint{fake, make(chan uint64, 3)}
	d := &dialer{Loopback: start(t, "unix", &Server{Endpoint: fake, Skills: e})}
	first, second := NewEndpointClient(d, fake.env.ID), NewEndpointClient(d, fake.env.ID)
	defer first.Close()
	defer second.Close()
	calls := []func() error{
		func() error { _, err := first.SyncSkills(ctx, api.SkillSync{}); return err },
		func() error { return second.DropSkills(ctx, api.SkillDrop{}) },
		func() error { return first.PutSkill(ctx, api.SkillPut{}) },
	}
	var last uint64
	for i, call := range calls {
		if err := call(); err != nil {
			t.Fatal(err)
		}
		if n := <-e.got; n <= last {
			t.Fatalf("call %d arrived at %d, after %d", i, n, last)
		} else {
			last = n
		}
	}
}

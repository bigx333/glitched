package runtime

import (
	"context"
	"fmt"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/engine"
	"glitched/internal/store"
)

// oldMachine is a machine from before links took answers: it refuses one
// as an intent a link may not send, and takes everything else.
type oldMachine struct {
	api.Endpoint
	env  api.Environment
	sent []api.Command
}

func (m *oldMachine) Info(context.Context) (api.Environment, error) { return m.env, nil }
func (m *oldMachine) Close() error                                  { return nil }
func (m *oldMachine) ChildReply(context.Context, api.ChildReplyQuery) (api.ChildReply, error) {
	return api.ChildReply{}, api.ErrNotFound
}
func (m *oldMachine) Submit(_ context.Context, c api.Command) (api.Receipt, error) {
	if _, ok := c.Body.(api.RequestRespond); ok {
		return api.Receipt{}, fmt.Errorf("%w: a link may not send %s", api.ErrNotAllowed, api.IntentType(c.Body))
	}
	m.sent = append(m.sent, c)
	return api.Receipt{Command: c.ID}, nil
}

// A machine that takes no answer from a link loses the answer, never the
// child: the messages after it still go there.
func TestAnAnswerAnOldMachineRefusesLosesNotTheChild(t *testing.T) {
	at := api.Remote{Engine: api.NewID(), Link: api.NewID()}
	m := &oldMachine{env: api.Environment{ID: at.Engine, Link: at.Link}}
	h := Host{ctx: context.Background(), backoff: func(int) time.Duration { return 0 }, reaching: map[api.ID]context.CancelFunc{},
		dial: func(api.Remote) (string, Machine, error) { return "b", m, nil }}
	e := store.Effect{Thread: api.NewID()}
	answer := api.Command{ID: api.NewID(), Target: api.NewID(), Body: api.RequestRespond{Request: api.NewID(), Answer: []byte(`{"answers":{}}`)}}
	if o, ok := h.remoteCommand(e, "message", at, nil, answer, nil); !ok || o != nil {
		lost, _ := o.(engine.RemoteLost)
		t.Fatalf("an answer the machine may not take: observed %+v (lost %q), ok %v; want nothing observed", o, lost.Reason, ok)
	}
	msg := api.Command{ID: api.NewID(), Target: answer.Target, Body: api.MessageDispatch{Text: "after the answer", Mode: api.AfterActive}}
	if o, ok := h.remoteCommand(e, "message", at, nil, msg, nil); !ok || o != nil || len(m.sent) != 1 || m.sent[0].ID != msg.ID {
		t.Fatalf("the message after the refused answer: observed %+v, ok %v, machine took %d; want it taken", o, ok, len(m.sent))
	}
}

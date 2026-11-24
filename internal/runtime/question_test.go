package runtime_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"glitched/internal/api"
	"glitched/internal/provider/fake"
)

// Eight clients answer one question request at once: seven with answers
// that fit, one with an approval's {"allow":true}. One fitting answer
// wins, the other six lose the race, and the misfit is refused whenever it
// lands. The agent gets the winner's answer, once.
func TestQuestionRaceHasOneWinner(t *testing.T) {
	path := dbPath(t)
	n := start(t, path, fake.New(fake.Asking()))
	thread := newThread(t, n.h)
	submit(t, n.h, thread, dispatch("ask", api.StartNow))
	req := until(t, n.h, thread, "the questions", waiting).Requests[0]
	if req.Kind != api.KindQuestion {
		t.Fatalf("the request is a %q, want a question", req.Kind)
	}

	const clients = 8
	answers := make([]json.RawMessage, clients)
	for i := range clients - 1 {
		answers[i] = json.RawMessage(fmt.Sprintf(`{"answers":[{"other":"store %d"},{"picked":["Unit"]}]}`, i))
	}
	answers[clients-1] = json.RawMessage(`{"allow":true}`)
	errs := make([]error, clients)
	var wg sync.WaitGroup
	gate := make(chan struct{})
	for i := range clients {
		wg.Go(func() {
			<-gate
			_, errs[i] = n.h.Submit(ctx, api.Command{ID: api.NewID(), Target: thread, Body: api.RequestRespond{
				Request: req.ID, Generation: req.Generation, Answer: answers[i],
			}})
		})
	}
	close(gate)
	wg.Wait()
	winner := -1
	for i, err := range errs[:clients-1] {
		switch {
		case err == nil && winner < 0:
			winner = i
		case err == nil:
			t.Fatalf("clients %d and %d both won", winner, i)
		case !errors.Is(err, api.ErrAlreadyAnswered):
			t.Fatalf("client %d got %v, want ErrAlreadyAnswered", i, err)
		}
	}
	if winner < 0 {
		t.Fatal("no client won")
	}
	if rej := (*api.RejectedError)(nil); !errors.As(errs[clients-1], &rej) {
		t.Fatalf(`{"allow":true} got %v, want rejected`, errs[clients-1])
	}
	until(t, n.h, thread, "turn end", idle)
	untilEffectsDone(t, path, "request.deliver")
	evs := events(t, n.h, thread)
	if count(evs, "request.answered") != 1 || count(evs, "request.delivered") != 1 {
		t.Fatalf("answered %d, delivered %d", count(evs, "request.answered"), count(evs, "request.delivered"))
	}
	if text, want := transcript(t, evs), "Two things first. got: "+string(answers[winner])+" Thanks."; text != want {
		t.Fatalf("the agent said %q, want %q", text, want)
	}
	assertReplay(t, n.st)
}

// A child blocked on questions reports its first question to its parent.
func TestABlockedChildReportsItsFirstQuestion(t *testing.T) {
	ad := fake.New(fake.Asking())
	n := start(t, dbPath(t), ad)
	parent := newThread(t, n.h)
	c, err := newChild(t, n.h, ad, parent, api.AccessAsk)
	if err != nil {
		t.Fatal(err)
	}
	submit(t, n.h, c, dispatch("ask", api.StartNow))
	until(t, n.h, parent, "the report", func(th api.Thread) bool { return !th.Reported.IsZero() })
	var got []string
	for _, e := range events(t, n.h, parent) {
		if b, err := e.Decode(); err == nil {
			if r, ok := b.(api.ChildReported); ok {
				got = append(got, r.Outcome)
			}
		}
	}
	if want := "Asks: Which store should the cache use?"; len(got) != 1 || got[0] != want {
		t.Fatalf("the parent's reports say %q, want %q", strings.Join(got, " | "), want)
	}
}

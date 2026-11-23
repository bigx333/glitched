package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"

	"glitched/internal/provider"
)

const (
	colours = `{"type":"string","title":"Colour?","oneOf":[{"const":"1","title":"Blue"},{"const":"2","title":"Red"}]}`
	words   = `{"type":"string","title":"Other"}`
)

// elicitation is an agy-shaped form of the session S with properties.
func elicitation(t *testing.T, schema string) acpsdk.CreateElicitationRequest {
	t.Helper()
	var r acpsdk.CreateElicitationRequest
	raw := fmt.Sprintf(`{"mode":"form","message":"The agent has a question.","sessionId":"S","requestedSchema":{"type":"object",%s}}`, schema)
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		t.Fatal(err)
	}
	return r
}

// briefly is a context that ends before a card that opened could hang the
// test: an answer it waits on never comes.
func briefly(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// A form Glitched's card could answer in a way the form refuses is
// cancelled before any card opens: the card always offers the user's own
// words, never requires an answer, and keeps no limits of the form's.
func TestAFormTheCardCantAnswerIsCancelled(t *testing.T) {
	for name, schema := range map[string]string{
		"options only":        `"properties":{"q1":` + colours + `}`,
		"a required field":    `"properties":{"q1":` + colours + `,"q1_other":` + words + `},"required":["q1"]`,
		"a field not asked":   `"properties":{"q1":` + colours + `,"q1_other":` + words + `,"confirmed":{"type":"boolean"}}`,
		"a limit on picks":    `"properties":{"q1":{"type":"array","maxItems":1,"items":{"anyOf":[{"const":"1","title":"A"},{"const":"2","title":"B"}]}},"q1_other":` + words + `}`,
		"a limit on words":    `"properties":{"q1":` + colours + `,"q1_other":{"type":"string","maxLength":2}}`,
		"a type of its own":   `"properties":{"q1":{"type":"_calendar","enum":["A","B"]},"q1_other":` + words + `}`,
		"words that also ask": `"properties":{"q1":` + colours + `,"q1_other":{"type":"string","enum":["X","Y"]}}`,
	} {
		t.Run(name, func(t *testing.T) {
			p := &proc{session: "S"}
			g := newAgent(provider.SessionSpec{}, p, nil)
			g.started = true
			r, err := p.CreateElicitation(briefly(t), elicitation(t, schema))
			if err != nil || r.Cancel == nil || len(g.out) != 0 {
				t.Fatalf("the agent got %+v, %v, and the run saw %+v; want it cancelled with no card", r, err, g.out)
			}
		})
	}
}

// The card asks a form's questions in the order of their numbers.
func TestQuestionsKeepTheirNumbersOrder(t *testing.T) {
	fm, _, err := parseForm(*elicitation(t, `"properties":{"q10":`+strings.Replace(colours, "Colour?", "Ten?", 1)+`,"q10_other":`+words+`,"q2":`+strings.Replace(colours, "Colour?", "Two?", 1)+`,"q2_other":`+words+`}`).Form)
	if err != nil {
		t.Fatal(err)
	}
	if got := []string{fm.q.Questions[0].Text, fm.q.Questions[1].Text}; !slices.Equal(got, []string{"Two?", "Ten?"}) {
		t.Fatalf("the card asks %q", got)
	}
}

// A question isn't asked before the run's prompt goes out.
func TestNoQuestionBeforeTheRunStarts(t *testing.T) {
	p := &proc{session: "S"}
	g := newAgent(provider.SessionSpec{}, p, nil)
	r, err := p.CreateElicitation(briefly(t), elicitation(t, `"properties":{"q1":`+colours+`,"q1_other":`+words+`}`))
	if err != nil || r.Cancel == nil || len(g.out) != 0 {
		t.Fatalf("the agent got %+v, %v, and the run saw %+v; want it cancelled with no card", r, err, g.out)
	}
}

// An answer given as the agent stops waiting is either the one the agent
// gets, or refused as withdrawn: never taken and lost.
func TestAnAnswerAsTheAgentStopsWaitingIsSentOrRefused(t *testing.T) {
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(1))
	synctest.Test(t, func(t *testing.T) {
		p := &proc{session: "S"}
		g := newAgent(provider.SessionSpec{}, p, nil)
		g.started = true
		ctx, cancel := context.WithCancel(t.Context())
		got := make(chan acpsdk.CreateElicitationResponse, 1)
		go func() {
			r, _ := p.CreateElicitation(ctx, elicitation(t, `"properties":{"q1":`+colours+`,"q1_other":`+words+`}`))
			got <- r
		}()
		synctest.Wait()
		opened := g.out[0].(provider.RequestOpened)
		cancel()
		err := g.respond(provider.Respond{Request: opened.ID, Answer: json.RawMessage(`{"answers":[{"picked":["Red"]}]}`)})
		b, _ := json.Marshal(<-got)
		switch {
		case err == nil && string(b) != `{"action":"accept","content":{"q1":"2"}}`:
			t.Fatalf("the answer was taken, but the agent got %s", b)
		case err != nil && (!errors.Is(err, provider.ErrWithdrawn) || string(b) != `{"action":"cancel"}`):
			t.Fatalf("the answer was refused with %v, and the agent got %s", err, b)
		}
	})
}

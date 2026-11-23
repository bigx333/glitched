package acp

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"glitched/internal/api"
	"glitched/internal/provider"
)

// asked opens a run on scenario, sends a prompt and returns the question
// the agent asks.
func asked(t *testing.T, scenario string) (provider.Agent, provider.RequestOpened, api.Questions, string) {
	t.Helper()
	a, log := fake(t, scenario)
	t.Cleanup(func() {
		if t.Failed() {
			b, _ := os.ReadFile(log)
			t.Log(string(b))
		}
	})
	g := open(t, a, spec(t))
	do(t, g, provider.Start{Text: "Ask me."})
	for {
		if r, ok := next(t, g).(provider.RequestOpened); ok {
			if r.Kind != api.KindQuestion {
				t.Fatalf("opened a %s, want a question", r.Kind)
			}
			q, err := api.ParseQuestions(r.Body)
			if err != nil {
				t.Fatalf("the question's body %s: %v", r.Body, err)
			}
			return g, r, q, log
		}
	}
}

// reply answers the question and returns what the agent says it got.
func reply(t *testing.T, g provider.Agent, r provider.RequestOpened, a api.Answers) string {
	t.Helper()
	b, _ := json.Marshal(a)
	do(t, g, provider.Respond{Request: r.ID, Answer: b})
	events := turn(t, g)
	closeRun(t, g)
	return text(events)
}

// agy asks its ask_question tool's question with a form: it reaches
// Glitched's own card, all five options kept in order, the recommended one
// marked as agy marks it, and the turn waits for the answer, which goes
// back as the option's value.
func TestAnAgentsQuestionReachesTheCard(t *testing.T) {
	g, r, q, log := asked(t, "question")
	want := []string{"(Recommended) Blue", "Green", "Red", "Yellow", "Purple"}
	var got []string
	for _, o := range q.Questions[0].Options {
		got = append(got, o.Label)
	}
	if len(q.Questions) != 1 || q.Questions[0].Text != "What is your favourite colour?" || q.Questions[0].Multi || strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("the card asks %+v", q)
	}
	if init := find(t, wire(t, log), "initialize"); len(init) != 1 || !strings.Contains(string(init[0].Params), `"elicitation":{"form":{}}`) {
		t.Fatalf("Glitched told the agent it takes no forms: %s", init[0].Params)
	}
	said := reply(t, g, r, api.Answers{Answers: []api.Answer{{Picked: []string{"Red"}}}})
	if said != `Answer: {"action":"accept","content":{"q1":"3"}}` {
		t.Fatalf("the agent got %s", said)
	}
}

// The user's own words go in the field agy gives them.
func TestAQuestionTakesTheUsersOwnWords(t *testing.T) {
	g, r, _, _ := asked(t, "question")
	said := reply(t, g, r, api.Answers{Answers: []api.Answer{{Other: "dark red"}}})
	if said != `Answer: {"action":"accept","content":{"q1_other":"dark red"}}` {
		t.Fatalf("the agent got %s", said)
	}
}

// A question dismissed on the card is declined.
func TestADismissedQuestionIsDeclined(t *testing.T) {
	g, r, _, _ := asked(t, "question")
	if said := reply(t, g, r, api.Answers{}); said != `Answer: {"action":"decline"}` {
		t.Fatalf("the agent got %s", said)
	}
}

// A question of more than one answer takes each pick's value.
func TestAMultiSelectQuestionSendsEachPick(t *testing.T) {
	g, r, q, _ := asked(t, "question-multi")
	if !q.Questions[0].Multi || len(q.Questions[0].Options) != 3 {
		t.Fatalf("the card asks %+v", q)
	}
	said := reply(t, g, r, api.Answers{Answers: []api.Answer{{Picked: []string{"Unit", "GUI"}}}})
	if said != `Answer: {"action":"accept","content":{"q1":["1","3"]}}` {
		t.Fatalf("the agent got %s", said)
	}
}

// Each of agy's questions takes its own answer, in its own fields.
func TestEachQuestionsAnswerGoesInItsOwnFields(t *testing.T) {
	for _, c := range []struct {
		answers []api.Answer
		want    string
	}{
		{[]api.Answer{{Other: "teal"}, {Picked: []string{"(Recommended) Apple", "Orange"}}}, `{"q1_other":"teal","q2":["1","3"]}`},
		{[]api.Answer{{Picked: []string{"Red"}}, {Picked: []string{"Banana"}, Other: "figs"}}, `{"q1":"2","q2":["2"],"q2_other":"figs"}`},
	} {
		g, r, q, _ := asked(t, "question-two")
		if len(q.Questions) != 2 || q.Questions[0].Text != "What is your favourite colour?" || q.Questions[0].Multi || q.Questions[1].Text != "Which fruits do you like?" || !q.Questions[1].Multi {
			t.Fatalf("the card asks %+v", q)
		}
		if said := reply(t, g, r, api.Answers{Answers: c.answers}); said != `Answer: {"action":"accept","content":`+c.want+"}" {
			t.Errorf("for %+v the agent got %s", c.answers, said)
		}
	}
}

// A question the turn stops waiting on goes back cancelled, and its card
// says it was withdrawn.
func TestAStoppedTurnCancelsItsQuestion(t *testing.T) {
	g, r, _, log := asked(t, "question")
	do(t, g, provider.Interrupt{})
	events := turn(t, g)
	closeRun(t, g)
	withdrawn := false
	for _, e := range events {
		if w, ok := e.(provider.RequestWithdrawn); ok && w.ID == r.ID {
			withdrawn = true
		}
	}
	if !withdrawn {
		t.Fatalf("the question stayed open after the stop: %+v", events)
	}
	if got := find(t, wire(t, log), "elicitation/create answer"); len(got) != 1 || !strings.Contains(string(got[0].Params), `"cancel"`) {
		t.Fatalf("the agent got %v", got)
	}
}

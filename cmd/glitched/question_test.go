package main

import (
	"bufio"
	"context"
	"reflect"
	"strings"
	"testing"

	"glitched/internal/api"
	"glitched/internal/provider/fake"
)

var storeAndTests = api.Questions{Questions: []api.Question{
	{Text: "Which store should the cache use?", Header: "Store", Options: []api.Option{
		{Label: "SQLite", About: "One file next to the log"}, {Label: "Memory", About: "Lost on restart"}}},
	{Text: "Which suites gate the merge?", Header: "Tests", Multi: true, Options: []api.Option{
		{Label: "Unit"}, {Label: "Race"}, {Label: "GUI"}}},
}}

// Each question is asked with its numbered options, and a line answers
// it: numbers pick, other words are the user's own, and an empty line
// dismisses them all.
func TestRunAsksEachQuestion(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want api.Answers
		says string
	}{
		{"1\n1,2\n", api.Answers{Answers: []api.Answer{{Picked: []string{"SQLite"}}, {Picked: []string{"Unit", "Race"}}}}, "answered"},
		{"Postgres\n3, Fuzz\n", api.Answers{Answers: []api.Answer{{Other: "Postgres"}, {Picked: []string{"GUI"}, Other: "Fuzz"}}}, "answered"},
		{"1,2\n2\n", api.Answers{Answers: []api.Answer{{Other: "1,2"}, {Picked: []string{"Race"}}}}, "answered"},
		{"1\n\n", api.Answers{}, "dismissed"},
	} {
		var out strings.Builder
		got := askQuestions(bufio.NewReader(strings.NewReader(tc.in)), &out, storeAndTests)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%q answered %+v, want %+v", tc.in, got, tc.want)
		}
		if err := got.Fit(storeAndTests); err != nil {
			t.Errorf("%q answered %+v, which the engine refuses: %v", tc.in, got, err)
		}
		for _, s := range []string{"? Store: Which store should the cache use?", "1) SQLite   One file next to the log", "2) Memory", tc.says} {
			if !strings.Contains(out.String(), s) {
				t.Errorf("%q: no %q in\n%s", tc.in, s, out.String())
			}
		}
	}
}

// A line that answers nothing, only commas, asks the question again
// rather than send what the engine refuses, which would end run.
func TestRunAsksAgainOnALineThatAnswersNothing(t *testing.T) {
	for _, in := range []string{",\n1\n2\n", "1\n , ,\n2\n"} {
		var out strings.Builder
		got := askQuestions(bufio.NewReader(strings.NewReader(in)), &out, storeAndTests)
		if want := (api.Answers{Answers: []api.Answer{{Picked: []string{"SQLite"}}, {Picked: []string{"Race"}}}}); !reflect.DeepEqual(got, want) {
			t.Errorf("%q answered %+v, want %+v", in, got, want)
		}
		if !strings.Contains(out.String(), "That answers nothing.") {
			t.Errorf("%q: the prompt didn't ask again:\n%s", in, out.String())
		}
	}
}

// With -yes, run dismisses the questions, and the turn goes on.
func TestRunYesDismissesQuestions(t *testing.T) {
	dir, ep := engine(t, fake.New(fake.Asking()))
	thread := newThread(t, ep)
	if err := <-runAsync(dir, thread, "now"); err != nil {
		t.Fatalf("run -yes: %v", err)
	}
	snap, err := ep.Read(context.Background(), api.Query{Thread: thread})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range snap.Events {
		if b, err := e.Decode(); err == nil {
			if a, ok := b.(api.RequestAnswer); ok {
				if as, err := api.ParseAnswers(a.Answer); err != nil || len(as.Answers) != 0 {
					t.Fatalf("run -yes answered %s, want a dismissal", a.Answer)
				}
				return
			}
		}
	}
	t.Fatal("run -yes answered nothing")
}

// The question prompt prints an agent's questions in printable ASCII,
// with every other character as an escape, and says so, as the [y/N]
// prompt does: a bidi control or an invisible mark could make one option
// read as another.
func TestRunQuestionPromptPrintsOnlyASCII(t *testing.T) {
	q := api.Questions{Questions: []api.Question{{
		Text: "Which ‮etaled‬ store?", Header: "St​ore",
		Options: []api.Option{{Label: "SQLite️", About: "א one file"}, {Label: "\x1b[2KMemory"}},
	}}}
	var out strings.Builder
	askQuestions(bufio.NewReader(strings.NewReader("1\n")), &out, q)
	printed := out.String()
	for i, c := range []byte(printed) {
		if (c < ' ' || c > '~') && c != '\n' {
			t.Errorf("the prompt prints byte %#x as it is, at %d of %q", c, i, printed)
		}
	}
	for _, want := range []string{
		`St\u{200B}ore: Which \u{202E}etaled\u{202C} store?`, `1) SQLite\u{FE0F}   \u{5D0} one file`, `2) \u{1B}[2KMemory`,
		"! " + escapes,
	} {
		if !strings.Contains(printed, want) {
			t.Errorf("the prompt prints %q, want %q in it", printed, want)
		}
	}
}

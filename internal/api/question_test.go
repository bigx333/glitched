package api

import (
	"encoding/json"
	"strings"
	"testing"
)

var storeAndTests = Questions{Questions: []Question{
	{Text: "Which store?", Header: "Store", Options: []Option{{Label: "SQLite"}, {Label: "Memory"}}},
	{Text: "Which suites?", Header: "Tests", Options: []Option{{Label: "Unit"}, {Label: "Race"}, {Label: "GUI"}}, Multi: true},
}}

func TestParseQuestionsRefusesWhatNoCardCanAsk(t *testing.T) {
	b, _ := json.Marshal(storeAndTests)
	if q, err := ParseQuestions(b); err != nil || len(q.Questions) != 2 || !q.Questions[1].Multi {
		t.Fatalf("parse %s: %+v %v", b, q, err)
	}
	opts := `"options":[{"label":"a"},{"label":"b"}]`
	five := strings.Repeat(`{"text":"q",`+opts+`},`, 5)
	for _, body := range []string{
		`{"allow":true}`,
		`{"questions":[]}`,
		`{"questions":[` + five[:len(five)-1] + `]}`,
		`{"questions":[{"text":" ",` + opts + `}]}`,
		`{"questions":[{"text":"q","options":[{"label":"a"}]}]}`,
		`{"questions":[{"text":"q","options":[{"label":"a"},{"label":"b"},{"label":"c"},{"label":"d"},{"label":"e"}]}]}`,
		`{"questions":[{"text":"q","options":[{"label":"a"},{"label":"a"}]}]}`,
		`{"questions":[{"text":"q","options":[{"label":"a"},{"label":""}]}]}`,
		`[1]`,
	} {
		if _, err := ParseQuestions(json.RawMessage(body)); err == nil {
			t.Errorf("parsed %s", body)
		}
	}
}

func TestAnswersFit(t *testing.T) {
	for _, tc := range []struct {
		raw string
		ok  bool
	}{
		{`{"answers":[]}`, true}, // a dismissal
		{`{}`, true},
		{`{"answers":null}`, true}, // the GUI's dismissal, as Go marshals it
		{`null`, false},            // no answer at all, not a dismissal
		{`{"answers":[{"picked":["SQLite"]},{"picked":["Unit","Race"]}]}`, true},
		{`{"answers":[{"other":"Postgres"},{"picked":["GUI"],"other":"Fuzz"}]}`, true},
		{`{"answers":[{"picked":["SQLite"]}]}`, false},                                        // one answer to two questions
		{`{"answers":[{"picked":["SQLite","Memory"]},{"picked":["Unit"]}]}`, false},           // two picks, one choice
		{`{"answers":[{"picked":["SQLite"],"other":"Postgres"},{"picked":["Unit"]}]}`, false}, // a pick and Other, one choice
		{`{"answers":[{"picked":["Postgres"]},{"picked":["Unit"]}]}`, false},                  // no such option
		{`{"answers":[{"other":"  "},{"picked":["Unit"]}]}`, false},                           // blank Other
		{`{"answers":[{"picked":["SQLite"]},{"picked":["Unit","Unit"]}]}`, false},             // a pick twice
		{`{"answers":[{"picked":["SQLite"]},{}]}`, false},
	} {
		a, err := ParseAnswers(json.RawMessage(tc.raw))
		if err == nil {
			err = a.Fit(storeAndTests)
		}
		if (err == nil) != tc.ok {
			t.Errorf("%s: %v, want fit %v", tc.raw, err, tc.ok)
		}
	}
	if _, err := ParseAnswers(json.RawMessage(`{"allow":true}`)); err == nil {
		t.Error(`{"allow":true} read as answers, which would dismiss the questions`)
	}
}

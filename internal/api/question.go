package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// The kinds of request an agent makes: an approval of what it wants to do,
// or questions for the user to answer.
const (
	KindApproval = "approval"
	KindQuestion = "question"
)

// Questions is a question request's body: 1 to 4 questions, which the
// user answers together.
type Questions struct {
	Questions []Question `json:"questions"`
}

type Question struct {
	Text    string   `json:"text"`             // the full question
	Header  string   `json:"header,omitempty"` // a short label for it
	Options []Option `json:"options"`          // 2 to 10
	Multi   bool     `json:"multi,omitempty"`  // more than one option may be picked
}

type Option struct {
	Label string `json:"label"`
	About string `json:"about,omitempty"`
}

// Answers answers a question request: Answers[i] answers Questions[i].
// No answers is a dismissal: the user declined to answer.
type Answers struct {
	Answers []Answer `json:"answers"`
}

// Answer is the labels of the options picked, the user's own words, or
// both when the question is Multi.
type Answer struct {
	Picked []string `json:"picked,omitempty"`
	Other  string   `json:"other,omitempty"`
}

// ParseQuestions reads a question request's body, and refuses one that
// asks no question, more than 4, or a question without text or with
// fewer than 2 or more than 10 options, or two options of one label.
// Claude Code offers 2 to 4; agy offers more.
func ParseQuestions(body json.RawMessage) (Questions, error) {
	var q Questions
	if err := json.Unmarshal(body, &q); err != nil {
		return Questions{}, fmt.Errorf("questions: %w", err)
	}
	if n := len(q.Questions); n < 1 || n > 4 {
		return Questions{}, fmt.Errorf("questions: %d questions, want 1 to 4", n)
	}
	for i, x := range q.Questions {
		if strings.TrimSpace(x.Text) == "" {
			return Questions{}, fmt.Errorf("questions: question %d has no text", i+1)
		}
		if n := len(x.Options); n < 2 || n > 10 {
			return Questions{}, fmt.Errorf("questions: question %d has %d options, want 2 to 10", i+1, n)
		}
		for j, o := range x.Options {
			if strings.TrimSpace(o.Label) == "" || slices.ContainsFunc(x.Options[:j], func(p Option) bool { return p.Label == o.Label }) {
				return Questions{}, fmt.Errorf("questions: question %d has a blank or repeated option", i+1)
			}
		}
	}
	return q, nil
}

// ParseAnswers reads an answer to a question request, and refuses
// anything else, such as an approval's {"allow":true}, or null, which
// would read as no answers and so dismiss them.
func ParseAnswers(raw json.RawMessage) (Answers, error) {
	var a *Answers
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&a); err != nil {
		return Answers{}, fmt.Errorf("answers: %w", err)
	}
	if a == nil {
		return Answers{}, errors.New("answers: null")
	}
	return *a, nil
}

// Fit is nil when a answers q: no answers, a dismissal, or one per
// question, each with a pick or Other text, its picks labels of the
// question's options, and one choice only when the question is not Multi.
func (a Answers) Fit(q Questions) error {
	if len(a.Answers) == 0 {
		return nil
	}
	if len(a.Answers) != len(q.Questions) {
		return fmt.Errorf("%d answers to %d questions", len(a.Answers), len(q.Questions))
	}
	for i, x := range a.Answers {
		qn := q.Questions[i]
		n := len(x.Picked)
		if strings.TrimSpace(x.Other) != "" {
			n++
		}
		switch {
		case n == 0:
			return fmt.Errorf("question %d has no answer", i+1)
		case n > 1 && !qn.Multi:
			return fmt.Errorf("question %d takes one answer", i+1)
		}
		for j, l := range x.Picked {
			if !slices.ContainsFunc(qn.Options, func(o Option) bool { return o.Label == l }) || slices.Contains(x.Picked[:j], l) {
				return fmt.Errorf("question %d has no option %q to pick, or picks it twice", i+1, l)
			}
		}
	}
	return nil
}

package acp

import (
	"cmp"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	acpsdk "github.com/coder/acp-go-sdk"

	"glitched/internal/api"
)

// form is a question an agent asks with an elicitation form, as Glitched's
// card shows it and as the agent takes the answers back. agy 1.5.14 asks
// each question of its ask_question tool as a field qN, of one string from
// a oneOf whose titles are its options, a recommended one's title starting
// "(Recommended)", and the user's own words in an optional field qN_other.
type form struct {
	q      api.Questions
	fields []field // fields[i] asks q.Questions[i]
}

type field struct {
	key, other string            // other takes the user's own words
	values     map[string]string // an option's label to the value it sends
	multi      bool
}

// parseForm reads an elicitation form as questions: one for each field
// that offers options, in the order of their names (q1, q2, ... q10), with
// the field named for it with "_other" as its words. It refuses a form
// whose every answer on Glitched's card the form wouldn't take: one that
// requires a field, limits what a field takes, offers no words for a
// question, or has a field the card doesn't ask.
func parseForm(f acpsdk.CreateElicitationForm) (*form, json.RawMessage, error) {
	props := f.RequestedSchema.Properties
	if len(f.RequestedSchema.Required) > 0 {
		return nil, nil, fmt.Errorf("acp: the form requires %v, which the card may leave empty", f.RequestedSchema.Required)
	}
	keys := slices.Collect(maps.Keys(props))
	slices.SortFunc(keys, func(a, b string) int { return cmp.Or(cmp.Compare(len(a), len(b)), strings.Compare(a, b)) })
	fm := &form{}
	for _, k := range keys {
		if base, ok := strings.CutSuffix(k, "_other"); ok {
			if _, _, asks := fieldQuestion(props[base]); asks {
				continue
			}
		}
		q, values, ok := fieldQuestion(props[k])
		if !ok {
			return nil, nil, fmt.Errorf("acp: the card can't ask the form's field %q", k)
		}
		if !takesWords(props[k+"_other"]) {
			return nil, nil, fmt.Errorf("acp: the form takes no words of the user's for %q, which the card offers", k)
		}
		q.Text = cmp.Or(q.Text, f.Message)
		fm.q.Questions = append(fm.q.Questions, q)
		fm.fields = append(fm.fields, field{key: k, other: k + "_other", values: values, multi: q.Multi})
	}
	body, err := json.Marshal(fm.q)
	if err != nil {
		return nil, nil, err
	}
	if _, err := api.ParseQuestions(body); err != nil {
		return nil, nil, err
	}
	return fm, body, nil
}

// fieldQuestion is the question a form field asks and the value each of its
// options sends: a string of one of its options, or an array of them,
// that takes any of its options and no more limits.
func fieldQuestion(p acpsdk.ElicitationPropertySchema) (q api.Question, values map[string]string, ok bool) {
	values = map[string]string{}
	add := func(label, value string) {
		q.Options = append(q.Options, api.Option{Label: label})
		values[label] = value
	}
	var title, about *string
	switch {
	case p.String != nil:
		s := p.String
		if s.Format != nil || s.MinLength != nil || s.MaxLength != nil || s.Pattern != nil {
			return q, nil, false
		}
		for _, v := range s.Enum {
			add(v, v)
		}
		for _, o := range s.OneOf {
			add(cmp.Or(o.Title, o.Const), o.Const)
		}
		title, about = s.Title, s.Description
	case p.Array != nil:
		a := p.Array
		if a.MinItems != nil || a.MaxItems != nil {
			return q, nil, false
		}
		switch {
		case a.Items.String != nil:
			for _, v := range a.Items.String.Enum {
				add(v, v)
			}
		case a.Items.Titled != nil:
			for _, o := range a.Items.Titled.AnyOf {
				add(cmp.Or(o.Title, o.Const), o.Const)
			}
		}
		q.Multi, title, about = true, a.Title, a.Description
	}
	if len(q.Options) == 0 {
		return q, nil, false
	}
	q.Text = cmp.Or(deref(title), deref(about))
	return q, values, true
}

// takesWords is whether a form field takes whatever the user writes.
func takesWords(p acpsdk.ElicitationPropertySchema) bool {
	s := p.String
	return s != nil && len(s.Enum) == 0 && len(s.OneOf) == 0 && s.Format == nil && s.MinLength == nil && s.MaxLength == nil && s.Pattern == nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// answer is the agent's reply to answers: each pick as the value its
// field sends, the user's words in the field that takes them, or declined
// when the user dismissed the questions.
func (fm *form) answer(raw json.RawMessage) (acpsdk.CreateElicitationResponse, error) {
	a, err := api.ParseAnswers(raw)
	if err == nil {
		err = a.Fit(fm.q)
	}
	if err != nil {
		return acpsdk.CreateElicitationResponse{}, fmt.Errorf("acp: an answer the agent can't take: %w", err)
	}
	if len(a.Answers) == 0 {
		return acpsdk.CreateElicitationResponse{Decline: &acpsdk.CreateElicitationDecline{Action: "decline"}}, nil
	}
	content := map[string]acpsdk.ElicitationContentValue{}
	for i, an := range a.Answers {
		fd := fm.fields[i]
		var picked []string
		for _, l := range an.Picked {
			picked = append(picked, fd.values[l])
		}
		switch {
		case fd.multi && len(picked) > 0:
			content[fd.key] = acpsdk.NewElicitationContentValueStringArray(picked...)
		case len(picked) > 0:
			content[fd.key] = acpsdk.NewElicitationContentValueString(picked[0])
		}
		if an.Other != "" {
			content[fd.other] = acpsdk.NewElicitationContentValueString(an.Other)
		}
	}
	return acpsdk.CreateElicitationResponse{Accept: &acpsdk.CreateElicitationAccept{Action: "accept", Content: content}}, nil
}

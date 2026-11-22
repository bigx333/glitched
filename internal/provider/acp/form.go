package acp

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
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
	key, other string            // other is "" when the form takes no words
	values     map[string]string // an option's label to the value it sends
	multi      bool
}

// offer is what a form field offers to pick from: JSON Schema's enum, or
// a oneOf or anyOf of titled constants, on the field or on its items.
type offer struct {
	Enum  []string `json:"enum"`
	OneOf []choice `json:"oneOf"`
	AnyOf []choice `json:"anyOf"`
}

type choice struct{ Const, Title string }

func (c offer) options() (labels []string, values map[string]string) {
	values = map[string]string{}
	add := func(label, value string) {
		labels = append(labels, label)
		values[label] = value
	}
	for _, v := range c.Enum {
		add(v, v)
	}
	for _, o := range append(c.OneOf, c.AnyOf...) {
		add(cmp.Or(o.Title, o.Const), o.Const)
	}
	return labels, values
}

// parseForm reads an elicitation form as questions: one for each field
// that offers options, in the order of their names (q1, q2, ... q10), with
// the field named for it with "_other" as its words. It refuses a form
// Glitched's card can't show.
func parseForm(f acpsdk.CreateElicitationForm) (*form, json.RawMessage, error) {
	raw, err := json.Marshal(f.RequestedSchema)
	if err != nil {
		return nil, nil, err
	}
	var schema struct {
		Properties map[string]struct {
			offer
			Type        string `json:"type"`
			Title       string `json:"title"`
			Description string `json:"description"`
			Items       offer  `json:"items"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		return nil, nil, err
	}
	keys := make([]string, 0, len(schema.Properties))
	for k := range schema.Properties {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b string) int { return cmp.Or(cmp.Compare(len(a), len(b)), strings.Compare(a, b)) })
	fm := &form{}
	for _, k := range keys {
		p := schema.Properties[k]
		c := p.offer
		if p.Type == "array" {
			c = p.Items
		}
		labels, values := c.options()
		if len(labels) == 0 {
			continue
		}
		fd := field{key: k, values: values, multi: p.Type == "array"}
		if o, ok := schema.Properties[k+"_other"]; ok && o.Type == "string" {
			fd.other = k + "_other"
		}
		q := api.Question{Text: cmp.Or(p.Title, p.Description, f.Message), Multi: fd.multi}
		for _, l := range labels {
			q.Options = append(q.Options, api.Option{Label: l})
		}
		fm.q.Questions = append(fm.q.Questions, q)
		fm.fields = append(fm.fields, fd)
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
		if an.Other == "" {
			continue
		}
		if fd.other == "" {
			return acpsdk.CreateElicitationResponse{}, errors.New("acp: the agent takes only its own options for this question")
		}
		content[fd.other] = acpsdk.NewElicitationContentValueString(an.Other)
	}
	return acpsdk.CreateElicitationResponse{Accept: &acpsdk.CreateElicitationAccept{Action: "accept", Content: content}}, nil
}

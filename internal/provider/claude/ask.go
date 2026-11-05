package claude

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"glitched/internal/api"
	"glitched/internal/provider"
)

// askTool is Claude Code's tool for asking the user questions. It reaches
// the run as a can_use_tool at every permission mode, bypassPermissions
// included, with "requires_user_interaction": true. Claude Code 2.1.317
// sends its input as:
//
//	{"questions":[{"question":"Which color?","header":"Color","multiSelect":false,
//	  "options":[{"label":"Red","description":"…"},{"label":"Blue","description":"…"}]}]}
const askTool = "AskUserQuestion"

// dismissedReason is what Claude Code hears when the user dismisses the
// questions. Claude Code gives it to the model as an error tool_result.
const dismissedReason = "The user dismissed the questions without answering."

// claudeQuestions is AskUserQuestion's input as Claude Code sends it.
// Kind is unset, or "choice", for a question with options; Claude Code
// can also ask for free "text" or a "number", which the card can't show.
type claudeQuestions struct {
	Questions []struct {
		Question    string `json:"question"`
		Header      string `json:"header"`
		Kind        string `json:"kind"`
		MultiSelect bool   `json:"multiSelect"`
		Options     []struct {
			Label       string `json:"label"`
			Description string `json:"description"`
		} `json:"options"`
	} `json:"questions"`
}

// question is the request an AskUserQuestion becomes, or the reason the
// card can't show it: more than 4 questions or options, fewer than 2
// options, a question that is not a choice, or a blank or repeated text
// or label.
func question(id string, input json.RawMessage) (provider.RequestOpened, *prompt, error) {
	var in claudeQuestions
	if err := json.Unmarshal(input, &in); err != nil {
		return provider.RequestOpened{}, nil, err
	}
	var q api.Questions
	for i, x := range in.Questions {
		if x.Kind != "" && x.Kind != "choice" {
			return provider.RequestOpened{}, nil, fmt.Errorf("question %d asks for %s, not a choice", i+1, x.Kind)
		}
		if slices.ContainsFunc(q.Questions, func(p api.Question) bool { return p.Text == x.Question }) {
			return provider.RequestOpened{}, nil, fmt.Errorf("question %d repeats an earlier question", i+1)
		}
		n := api.Question{Text: x.Question, Header: x.Header, Multi: x.MultiSelect}
		for _, o := range x.Options {
			n.Options = append(n.Options, api.Option{Label: o.Label, About: o.Description})
		}
		q.Questions = append(q.Questions, n)
	}
	body, _ := json.Marshal(q)
	if _, err := api.ParseQuestions(body); err != nil {
		return provider.RequestOpened{}, nil, err
	}
	return provider.RequestOpened{ID: id, Kind: api.KindQuestion, Body: body},
		&prompt{kind: api.KindQuestion, input: input, questions: q}, nil
}

// misfit is the deny that answers questions the card can't show, so the
// turn goes on without them.
func misfit(id string, why error) map[string]any {
	return decided(id, map[string]any{"behavior": "deny", "message": "glitched cannot show these questions (" + why.Error() +
		"). Ask 1 to 4 questions, each with 2 to 4 options of distinct labels, or ask in plain text."})
}

// answered is the decision that carries the user's answers to Claude
// Code: the tool's own input with "answers" added, which maps each
// question's text to its option label or the user's own words. A Multi
// question's answer is a list, which Claude Code joins with commas. No
// answers is a dismissal, which is a deny.
//
//	{"behavior":"allow","updatedInput":{"questions":[…as sent…],
//	  "answers":{"Which color?":"Teal","Which sizes?":["S","M"]}}}
//	{"behavior":"deny","message":"The user dismissed the questions without answering."}
//
// Claude Code 2.1.317 then tells the model, as the tool's result:
//
//	The user answered: "Which color?"="Teal", "Which sizes?"="S,M". …
//	The user dismissed the questions without answering. (is_error)
func (p *prompt) answered(answer json.RawMessage) (map[string]any, error) {
	a, err := api.ParseAnswers(answer)
	if err != nil {
		return nil, err
	}
	if len(a.Answers) == 0 {
		return map[string]any{"behavior": "deny", "message": dismissedReason}, nil
	}
	if err := a.Fit(p.questions); err != nil {
		return nil, err
	}
	answers := map[string]any{}
	for i, x := range p.questions.Questions {
		got := a.Answers[i].Picked
		if o := a.Answers[i].Other; strings.TrimSpace(o) != "" {
			got = append(slices.Clip(got), o)
		}
		if x.Multi {
			answers[x.Text] = got
		} else {
			answers[x.Text] = got[0]
		}
	}
	var input map[string]any
	if err := json.Unmarshal(p.input, &input); err != nil || input == nil {
		return nil, errors.New("claude: AskUserQuestion's input is no object")
	}
	input["answers"] = answers
	return map[string]any{"behavior": "allow", "updatedInput": input}, nil
}

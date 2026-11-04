package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"glitched/internal/api"
	"glitched/internal/engine"
	"glitched/internal/store"
)

// outcomeLimit is how long a report's outcome may be, in characters: a line
// or two of the parent's timeline.
const outcomeLimit = 280

// report posts a child's report in its parent's log, with the completion of
// the effect that asked for it. A parent that was deleted, or never reached
// this engine, takes nothing, and the effect is done.
func (h *Host) report(e store.Effect, b engine.ReportToParent) {
	var outcome string
	if !retry(h.ctx, "read report", func(ctx context.Context) (err error) {
		outcome, err = h.outcome(ctx, e.Thread, b)
		return err
	}) {
		retry(h.drain, "release effect", func(ctx context.Context) error { return h.store.Release(ctx, e.ID) })
		return
	}
	o := engine.Reported{Report: api.ChildReported{Child: b.Child, Run: b.Run, Kind: b.Kind, Title: b.Title, Outcome: outcome}}
	retry(h.drain, "commit report", func(ctx context.Context) error {
		err := h.observe(ctx, b.Parent, o, e.ID)
		if errors.Is(err, api.ErrNotFound) {
			return h.store.Complete(ctx, e.ID)
		}
		return err
	})
}

// outcome is what came of the child's run, in a line or two: the request
// it waits on, why it failed, or the last of what it said.
func (h *Host) outcome(ctx context.Context, child api.ID, b engine.ReportToParent) (string, error) {
	switch {
	case b.Kind == api.ReportBlocked:
		return askLine(b.Ask), nil
	case b.Kind == api.ReportFailed && b.Error != "":
		return clip(oneLine(b.Error), outcomeLimit), nil
	}
	text, err := h.store.RunText(ctx, child, b.Run)
	if err != nil {
		return "", err
	}
	return summary(text), nil
}

// summary is the last paragraph of a reply, on one line, cut to
// outcomeLimit: where an agent says what it did.
func summary(s string) string {
	paras := strings.Split(strings.TrimSpace(s), "\n\n")
	for i := len(paras) - 1; i >= 0; i-- {
		if p := oneLine(paras[i]); p != "" {
			return clip(p, outcomeLimit)
		}
	}
	return ""
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// clip is s cut to at most n characters, an ellipsis ending one that was
// cut, on a character's boundary.
func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

// askLine is what a blocked child waits on, from its request's body: a
// command to run, an operation on a path, or its first question.
func askLine(body json.RawMessage) string {
	if q, err := api.ParseQuestions(body); err == nil {
		return clip(oneLine("Asks: "+q.Questions[0].Text), outcomeLimit)
	}
	var b struct{ Operation, Path, Command string }
	if json.Unmarshal(body, &b) == nil {
		switch {
		case b.Command != "":
			return clip("Wants to run "+oneLine(b.Command), outcomeLimit)
		case b.Operation != "" && b.Path != "":
			return clip(oneLine("Wants to "+b.Operation+" "+b.Path), outcomeLimit)
		case b.Operation != "":
			return clip(oneLine("Wants to "+b.Operation), outcomeLimit)
		}
	}
	return "Waits on your answer"
}

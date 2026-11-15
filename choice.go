package main

import (
	"log"
	"path/filepath"
	"slices"
	"strings"

	"glitched/internal/api"
	"glitched/internal/jsonfile"
)

// choice is what the composer's pickers show: the model, its effort, fast
// mode and the access, by the names the pickers give them. In a new
// thread's draft it's the default a new thread takes; in a thread, that
// thread's own.
type choice struct {
	Model  string `json:"model"`
	Effort string `json:"effort"`
	Fast   bool   `json:"fast,omitempty"`
	Access string `json:"access"`
}

// choice is what the pickers show now.
func (a *app) choice() choice {
	return choice{Model: a.model, Effort: a.effort, Fast: a.fast, Access: a.access}
}

func (a *app) show(ch choice) {
	a.model, a.effort, a.fast, a.access = ch.Model, ch.Effort, ch.Fast, ch.Access
}

func (a *app) composerPath() string { return filepath.Join(a.dir, "composer.json") }

// loadComposer takes the default a new thread took when the app last
// saved it: composer.json in the state directory, beside layout.json. A
// name the pickers no longer offer, as a model since gone, keeps the
// app's own default for it.
func (a *app) loadComposer() {
	var ch choice
	if err := jsonfile.Load(a.composerPath(), &ch); err != nil {
		log.Printf("glitched: composer: %v", err)
		return
	}
	if slices.Contains(modelNames(), ch.Model) {
		a.model = ch.Model
	}
	if slices.Contains(efforts, ch.Effort) {
		a.effort = ch.Effort
	}
	a.fast = ch.Fast
	if _, ok := accessOf[ch.Access]; ok {
		a.access = ch.Access
	}
	a.savedChoice = a.choice()
}

// saveComposer keeps the default for the next start, once it changed and
// the effort's thumb is let go. A write that fails is tried again at the
// next change, not each frame.
func (a *app) saveComposer() {
	ch := a.choice()
	if ch == a.savedChoice || a.effortHeld {
		return
	}
	a.savedChoice = ch
	if err := jsonfile.Write(a.composerPath(), ch); err != nil {
		log.Printf("glitched: composer: %v", err)
	}
}

// configured is the thread's model and access, as the last change sent
// from here makes them once it lands.
func configured(t thread) (api.ModelRef, api.Access) {
	m, acc := t.View.Model, t.View.Access
	for _, p := range t.Pending {
		if c, ok := p.Body.(api.ThreadConfigure); ok && p.Err == nil {
			m, acc = c.Model, c.Access
		}
	}
	return m, acc
}

// threadChoice is the thread's model and access as the pickers name them.
// A model or effort the pickers don't list shows by its own name.
func threadChoice(t thread) choice {
	m, acc := configured(t)
	ch := choice{Model: m.Model, Effort: m.Effort, Fast: m.Fast, Access: accessAuto}
	if ch.Model == "" {
		ch.Model = "Default model"
	}
	for _, e := range efforts {
		if strings.EqualFold(e, m.Effort) {
			ch.Effort = e
		}
	}
	for name, v := range accessOf {
		if v == acc {
			ch.Access = name
		}
	}
	return ch
}

// familyOf is the picker's family of the thread's provider, or noFamily
// for one the picker doesn't list: it offers none of its models.
func familyOf(provider string) string {
	for _, f := range families {
		if strings.EqualFold(f.name, provider) {
			return f.name
		}
	}
	return noFamily
}

const noFamily = "-"

// threadControls builds the pickers on the thread's own model and access,
// and sends what they change to the engine, for the thread's next turn.
// The default for a new thread stays as it was: the pickers show it again
// in a draft. A thread keeps its provider, so the picker offers only its
// family's models.
func (a *app) threadControls(t thread, build func()) {
	accessAt := a.accessAt
	defer func() {
		if a.accessAt != accessAt {
			a.accessFor = t.id()
		}
	}()
	if t.id().IsZero() {
		build()
		a.saveComposer()
		return
	}
	draft := a.choice()
	defer a.show(draft)
	before := threadChoice(t)
	a.show(before)
	m, level := configured(t)
	a.pickerLock = familyOf(m.Provider)
	family := a.pickerFamily
	defer func() { a.pickerLock, a.pickerFamily = "", family }()
	build()
	after := a.choice()
	if after == before || t.env == nil {
		return
	}
	if after.Model != before.Model {
		m.Model = after.Model
	}
	if after.Effort != before.Effort {
		m.Effort = strings.ToLower(after.Effort)
	}
	m.Fast = after.Fast
	if after.Access != before.Access {
		level = accessOf[after.Access]
	}
	a.backend.ConfigureThread(t.env, t.id(), m, level)
}

// shownAccess is the access level the composer shows for t, for its card's
// effect, or "" when the level last changed elsewhere: the effect is that
// thread's or the draft's.
func (a *app) shownAccess(t thread) string {
	if a.accessFor != t.id() {
		return ""
	}
	if t.id().IsZero() {
		return a.access
	}
	return threadChoice(t).Access
}

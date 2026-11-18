package main

import (
	"encoding/json"
	"errors"
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
// thread's own. composer.json holds the default's fields the user chose:
// one never chosen is the app's own.
type choice struct {
	Model  string `json:"model,omitempty"`
	Effort string `json:"effort,omitempty"`
	Fast   bool   `json:"fast,omitempty"`
	Access string `json:"access,omitempty"`
}

// ownChoice is the default a new thread takes where the user chose none.
var ownChoice = choice{Model: "Claude Opus 5.5", Effort: "High", Access: accessAuto}

// starting is the default the app starts with from file, composer.json's
// contents: the app's own, with each of file's names the pickers offer. A
// name they no longer offer, as a model since gone, keeps the app's own.
func starting(file choice) choice {
	ch := ownChoice
	if slices.Contains(modelNames(), file.Model) {
		ch.Model = file.Model
	}
	if slices.Contains(efforts, file.Effort) {
		ch.Effort = file.Effort
	}
	ch.Fast = file.Fast
	if _, ok := accessOf[file.Access]; ok {
		ch.Access = file.Access
	}
	return ch
}

// choice is what the pickers show now.
func (a *app) choice() choice {
	return choice{Model: a.model, Effort: a.effort, Fast: a.fast, Access: a.access}
}

func (a *app) show(ch choice) {
	a.model, a.effort, a.fast, a.access = ch.Model, ch.Effort, ch.Fast, ch.Access
}

func (a *app) composerPath() string { return filepath.Join(a.dir, "composer.json") }

// loadComposer takes the default a new thread took when an app last
// saved it: composer.json in the state directory, beside layout.json.
func (a *app) loadComposer() {
	var file choice
	if err := jsonfile.Load(a.composerPath(), &file); err != nil {
		log.Printf("glitched: composer: %v", err)
		file = choice{}
	}
	a.show(starting(file))
	a.savedChoice, a.triedChoice = a.choice(), a.choice()
}

// saveComposer keeps the default for the next start, once it changed and
// the effort's thumb is let go. Another window on the state directory
// saves its own: under the file's lock, only what changed here goes onto
// what the file holds, and the composer then shows the default a start
// would take from it, the other window's choices with this one's. A save
// that fails is tried again at the next change, not each frame.
func (a *app) saveComposer() {
	ch := a.choice()
	if ch == a.triedChoice || a.effortHeld {
		return
	}
	a.triedChoice = ch
	had := a.savedChoice
	if ch == had {
		return
	}
	file, err := jsonfile.Edit(a.composerPath(), func(f *choice) error {
		if ch.Model != had.Model {
			f.Model = ch.Model
		}
		if ch.Effort != had.Effort {
			f.Effort = ch.Effort
		}
		if ch.Fast != had.Fast {
			f.Fast = ch.Fast
		}
		if ch.Access != had.Access {
			f.Access = ch.Access
		}
		return nil
	})
	syntax, kind := (*json.SyntaxError)(nil), (*json.UnmarshalTypeError)(nil)
	if errors.As(err, &syntax) || errors.As(err, &kind) {
		// A file that doesn't parse holds no default to keep.
		file, err = ch, jsonfile.Write(a.composerPath(), ch)
	}
	if err != nil {
		log.Printf("glitched: composer: %v", err)
		return
	}
	a.show(starting(file))
	a.savedChoice, a.triedChoice = a.choice(), a.choice()
}

// configured is the thread's model and access, as the changes sent from
// here make them once they land: what each gives, in place of what the
// view has.
func configured(t thread) (api.ModelRef, api.Access) {
	m, acc := t.View.Model, t.View.Access
	for _, p := range t.Pending {
		if c, ok := p.Body.(api.ThreadConfigure); ok && p.Err == nil {
			m, acc = c.Onto(m, acc)
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
// It sends only what changed: what the pickers left as it showed stays as
// the engine has it, though another client changed it since the view this
// window holds. The default for a new thread stays as it was: the pickers
// show it again in a draft. A thread keeps its provider, so the picker
// offers only its family's models.
func (a *app) threadControls(t thread, build func()) {
	a.pickersIn(t.id())
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
	a.pickerLock = familyOf(t.View.Model.Provider)
	family := a.pickerFamily
	defer func() { a.pickerLock, a.pickerFamily = "", family }()
	build()
	after := a.choice()
	if after == before || t.env == nil {
		return
	}
	var c api.ThreadConfigure
	if after.Model != before.Model {
		c.Model = new(after.Model)
	}
	if after.Effort != before.Effort {
		c.Effort = new(strings.ToLower(after.Effort))
	}
	if after.Fast != before.Fast {
		c.Fast = new(after.Fast)
	}
	if after.Access != before.Access {
		c.Access = new(accessOf[after.Access])
	}
	a.backend.ConfigureThread(t.env, t.id(), c)
}

// pickersIn ties the pickers to the composer of the thread id, zero for
// the draft's. Their popovers are of the composer they opened in, and
// close as another's builds them: closed, a popover builds nothing, which
// lets go of what was pressed in it, and ends the thumb's hold (view). A
// thumb dragged in one thread would else choose the effort of the thread
// that comes under it, or the default's as ⌘N brings the draft, and a
// hold on Full access would grant it there. A hold the draft loses so
// ends here, and saves the default its thumb chose: the draft's composer
// that would save it once let go is no longer built.
func (a *app) pickersIn(id api.ID) {
	if a.pickersFor != id {
		if a.effortHeld && a.pickersFor.IsZero() {
			a.effortHeld = false
			a.saveComposer()
		}
		a.pickersFor = id
		a.pickerOpen, a.accessOpen = false, false
	}
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

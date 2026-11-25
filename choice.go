package main

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"glitched/internal/api"
	"glitched/internal/jsonfile"
)

// choice is what the composer's pickers show: the model by its key, its
// effort by its id, speed and the access by the name its picker gives it. In a new
// thread's draft it's the default a new thread takes; in a thread, that
// thread's own. composer.json holds the default's fields the user chose:
// one never chosen is the app's own. Follow is that the default's model
// is its provider's program's own default, which composer.json holds as
// the provider without an id, and none chosen as Claude Code's: the model
// is the one its engine last reported the program runs where given none,
// which moves with what the engine reports.
type choice struct {
	Model     modelKey `json:"model,omitzero"`
	Effort    string   `json:"effort,omitempty"`
	Fast      bool     `json:"fast,omitempty"`
	Ultrafast bool     `json:"ultrafast,omitempty"`
	Access    string   `json:"access,omitempty"`
	Follow    bool     `json:"-"`
}

// boolsOfSpeed is s in ModelRef's compatible form: ultrafast is fast to
// a reader that doesn't know it.
func boolsOfSpeed(s api.Speed) (fast, ultrafast bool) {
	return s != api.SpeedStandard, s == api.SpeedUltrafast
}

// ownChoice is the default a new thread takes where the user chose none
// and Claude Code's engine never said its own.
var ownChoice = choice{Model: modelKey{"claude", "claude-opus-5-5"}, Effort: "high", Access: accessAuto}

// starting is the default the app starts with from file, composer.json's
// contents: the model file chose, though its engine no longer offers it
// (goneModel), as the user chose it. A provider's own default is the
// model its engine reports its program runs where given none; else the
// app's own, where it's that provider's and its engine offers it; else
// its first model, else none, which the program chooses. With none
// chosen, it's Claude Code's own default; else the app's own where its
// engine offers it, else the first model the engines offer. The effort is
// file's, else, for a provider's own default, the one its program runs it
// at, else the app's own, as the model runs at it (effortFor).
func (a *app) starting(file choice) choice {
	ch := ownChoice
	ch.Follow = file.Model.ID == ""
	p := cmp.Or(file.Model.Provider, ownChoice.Model.Provider)
	def, known := a.cliDefault(p)
	_, ownOffered := a.modelOf(ownChoice.Model)
	switch {
	case !ch.Follow:
		ch.Model = file.Model
	case known:
		ch.Model = def.key
	case p == ownChoice.Model.Provider && ownOffered:
	case file.Model.Provider != "":
		ch.Model = file.Model
		if ms := a.familyModels(familyOf(p)); len(ms) > 0 {
			ch.Model = ms[0].key
		}
	default:
		if ms := a.offeredModels(); len(ms) > 0 {
			ch.Model = ms[0].key
		}
	}
	m, _ := a.modelOf(ch.Model)
	ch.Effort = effortFor(m, ownChoice.Effort)
	if ch.Follow && known {
		ch.Effort = effortFor(m, cmp.Or(def.defaultEffort, ownChoice.Effort))
	}
	if e := effortFor(m, file.Effort); strings.EqualFold(e, file.Effort) {
		ch.Effort = e
	}
	// Speed is a preference, not a capability decision: keep it as chosen
	// while a catalog is uncertain or moves, and narrow it only where it is
	// shown or sent (speedFor).
	ch.Fast, ch.Ultrafast = file.Fast, file.Ultrafast
	if _, ok := accessOf[file.Access]; ok {
		ch.Access = file.Access
	}
	return ch
}

// choice is what the pickers show now.
func (a *app) choice() choice {
	return choice{Model: a.model, Effort: a.effort, Fast: a.fast, Ultrafast: a.ultrafast, Access: a.access, Follow: a.followsCLI}
}

func (a *app) show(ch choice) {
	a.model, a.effort, a.fast, a.ultrafast, a.access, a.followsCLI = ch.Model, ch.Effort, ch.Fast, ch.Ultrafast, ch.Access, ch.Follow
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
	a.composerFile = file
	a.show(a.starting(file))
	a.savedChoice, a.triedChoice = a.choice(), a.choice()
}

// reconcile has a default the user left as it was take what the engines
// now offer as the answer comes, though no draft shows: their first model
// where none was chosen, a program's own default where it's followed, an
// effort the model runs at. An edit not yet saved, or the effort's thumb
// held, leaves it to saveComposer, as that edit is saved.
func (a *app) reconcile() {
	if !a.models.changed || a.effortHeld || a.choice() != a.savedChoice {
		return
	}
	a.models.changed = false
	a.show(a.starting(a.composerFile))
	a.savedChoice, a.triedChoice = a.choice(), a.choice()
}

// saveComposer keeps the default for the next start, once it changed and
// the effort's thumb is let go. Another window on the state directory
// saves its own: under the file's lock, only what changed here goes onto
// what the file holds, and the composer then shows the default a start
// would take from it, the other window's choices with this one's. A save
// that fails is tried again at the next change, not each frame.
func (a *app) saveComposer() {
	if a.effortHeld {
		return
	}
	if a.models.changed {
		// A default the user left as it was takes what the engines now
		// offer: their first model where none was chosen, an effort the
		// model runs at.
		a.models.changed = false
		if a.choice() == a.savedChoice {
			a.show(a.starting(a.composerFile))
			a.savedChoice, a.triedChoice = a.choice(), a.choice()
		}
	}
	ch := a.choice()
	if ch == a.triedChoice {
		return
	}
	a.triedChoice = ch
	had := a.savedChoice
	if ch == had {
		return
	}
	file, err := jsonfile.Edit(a.composerPath(), func(f *choice) error {
		switch {
		case ch.Model != had.Model || !ch.Follow && had.Follow:
			// The effort goes with the model: one the file holds that
			// the last model didn't run at mustn't come back with this.
			f.Model, f.Effort = ch.Model, ch.Effort
		case ch.Effort != had.Effort:
			f.Effort = ch.Effort
		}
		if ch.Fast != had.Fast || ch.Ultrafast != had.Ultrafast {
			f.Fast, f.Ultrafast = ch.Fast, ch.Ultrafast
		}
		if ch.Access != had.Access {
			f.Access = ch.Access
		}
		return nil
	})
	syntax, kind := (*json.SyntaxError)(nil), (*json.UnmarshalTypeError)(nil)
	if errors.As(err, &syntax) || errors.As(err, &kind) {
		// A file that doesn't parse holds no default to keep: it holds
		// this one, a provider's own default as the provider alone.
		file = ch
		if ch.Follow {
			file.Model = modelKey{Provider: ch.Model.Provider}
			if ch.Effort == a.starting(choice{Model: file.Model}).Effort {
				file.Effort = ""
			}
		}
		err = jsonfile.Write(a.composerPath(), file)
	}
	if err != nil {
		log.Printf("glitched: composer: %v", err)
		return
	}
	a.composerFile = file
	a.show(a.starting(file))
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

// threadModel is the model t runs on m: the one m names, else, where t
// leaves it to its provider's program, the one that program reported it
// ran, whose efforts are the ones t runs at.
func threadModel(t thread, m api.ModelRef) modelKey {
	id := m.Model
	if id == "" && t.View.Confirmed.Model != nil {
		id = t.View.Confirmed.Model.Model
	}
	return keyOfThread(m.Provider, id)
}

// threadChoice is the thread's model and access as the pickers name them.
// A model or effort the pickers don't list shows by its own name.
func (a *app) threadChoice(t thread) choice {
	m, acc := configured(t)
	fast, ultrafast := boolsOfSpeed(a.speedFor(threadModel(t, m), m.Fast, m.Ultrafast))
	ch := choice{Model: threadModel(t, m), Effort: m.Effort, Fast: fast, Ultrafast: ultrafast, Access: accessAuto, Follow: m.Model == ""}
	for name, v := range accessOf {
		if v == acc {
			ch.Access = name
		}
	}
	return ch
}

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
		a.draftDefaults(t)
		build()
		a.saveComposer()
		return
	}
	draft := a.choice()
	defer a.show(draft)
	before := a.threadChoice(t)
	a.show(before)
	a.pickerLock = t.View.Model.Provider
	family := a.pickerFamily
	defer func() { a.pickerLock, a.pickerFamily = "", family }()
	build()
	after := a.choice()
	// The raw choice keeps a speed preference across models, but a thread
	// is configured at what its model can run: moving off Ultrafast's model
	// is a downgrade even though the preference's flags did not change.
	after.Fast, after.Ultrafast = boolsOfSpeed(a.speedFor(after.Model, after.Fast, after.Ultrafast))
	if after == before || t.env == nil {
		return
	}
	var c api.ThreadConfigure
	if after.Model != before.Model || before.Follow && !after.Follow {
		// A model goes with its effort: the engine keeps an effort not
		// sent, which another client may have changed since. One the
		// thread ran as its program's own is the user's once picked.
		m, _ := a.modelOf(after.Model)
		c.Model, c.Effort = new(after.Model.ID), new(effortFor(m, after.Effort))
	}
	if after.Effort != before.Effort {
		c.Effort = new(after.Effort)
	}
	if after.Fast != before.Fast || after.Ultrafast != before.Ultrafast {
		c.Fast, c.Ultrafast = new(after.Fast), new(after.Ultrafast)
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
	return a.threadChoice(t).Access
}

// modelRecords is models.list: the models the picker offers, each by its
// provider and id, which name it, its name, family and what it's for as
// the picker shows them, and the efforts it runs at; and every model's
// efforts.
func (a *app) modelRecords() any {
	type record struct {
		Provider    string   `json:"provider"`
		ID          string   `json:"id"`
		Name        string   `json:"name"`
		Family      string   `json:"family"`
		Description string   `json:"description"`
		Efforts     []string `json:"efforts"`
	}
	out := struct {
		Models  []record `json:"models"`
		Efforts []string `json:"efforts"`
	}{Models: []record{}, Efforts: []string{}}
	for _, m := range a.offeredModels() {
		r := record{m.key.Provider, m.key.ID, m.name, familyOf(m.key.Provider).name, m.desc, []string{}}
		for _, e := range effortsOf(m) {
			r.Efforts = append(r.Efforts, e.id)
			if !slices.Contains(out.Efforts, e.id) {
				out.Efforts = append(out.Efforts, e.id)
			}
		}
		out.Models = append(out.Models, r)
	}
	return out
}

// configureEffect is threads.configure: the model, effort and speed of
// the thread's next turn, as its picker sets them, and only those. The
// access is the access picker's alone, so a configure that names it, or
// anything else, is refused whole.
func (a *app) configureEffect(args json.RawMessage) (any, error) {
	var in struct {
		Type      string // the call's own, threads.configure
		Thread    string
		Model     *string
		Effort    *string
		Fast      *bool
		Ultrafast *bool
	}
	d := json.NewDecoder(bytes.NewReader(args))
	d.DisallowUnknownFields()
	if err := d.Decode(&in); err != nil {
		return nil, fmt.Errorf("threads.configure sets a model, an effort and a speed, and nothing else: %v", err)
	}
	if in.Model == nil && in.Effort == nil && in.Fast == nil && in.Ultrafast == nil {
		return nil, errors.New("threads.configure needs a model, an effort or a speed")
	}
	t, err := a.pluginFind(args)
	if err != nil {
		return nil, err
	}
	c := api.ThreadConfigure{Fast: in.Fast, Ultrafast: in.Ultrafast}
	now, _ := configured(t)
	// A thread keeps its provider: its session is that provider's.
	m, _ := a.modelOf(threadModel(t, now))
	if in.Model != nil {
		var offered bool
		if m, offered = a.modelOf(modelKey{now.Provider, *in.Model}); !offered {
			return nil, fmt.Errorf("%q is no model of the thread's provider, %s", *in.Model, now.Provider)
		}
		c.Model = in.Model
	}
	if in.Effort == nil && in.Model != nil {
		// The new model takes the effort the thread runs at, where it
		// runs at it, else the one the picker would show; sent with it,
		// as the engine keeps an effort not sent, which another client
		// may have changed since.
		e := effortFor(m, now.Effort)
		c.Effort = &e
	}
	if in.Effort != nil {
		var ids []string
		for _, e := range effortsOf(m) {
			ids = append(ids, e.id)
		}
		if !slices.Contains(ids, *in.Effort) {
			return nil, fmt.Errorf("%s's effort is one of %s", m.name, strings.Join(ids, ", "))
		}
		c.Effort = in.Effort
	}
	if in.Model != nil || in.Fast != nil || in.Ultrafast != nil {
		candidate := now
		if c.Model != nil {
			candidate.Model = *c.Model
		}
		candidate, _ = (api.ThreadConfigure{Fast: in.Fast, Ultrafast: in.Ultrafast}).Onto(candidate, api.AccessAsk)
		if a.speedFor(threadModel(t, candidate), candidate.Fast, candidate.Ultrafast) != candidate.Speed() {
			cm, _ := a.modelOf(threadModel(t, candidate))
			return nil, fmt.Errorf("%s doesn't offer %s mode", cm.name, candidate.Speed())
		}
	}
	return nil, a.sendConfigure(t, c)
}

// sendConfigure sends c to t's engine for its next turn, and answers once
// it's sent; one that can't be, as to an engine that's gone, says why.
func (a *app) sendConfigure(t thread, c api.ThreadConfigure) error {
	cmd := a.backend.ConfigureThread(t.env, t.id(), c)
	if now, ok := a.find(t.id()); ok {
		for _, p := range now.Pending {
			if p.Command == cmd && p.Err != nil {
				return p.Err
			}
		}
	}
	return nil
}

// setAccessEffect is threads.setAccess: a thread's access, or with none
// the default for new threads the draft's picker sets, to one of the
// levels the access picker offers, and nothing else.
func (a *app) setAccessEffect(args json.RawMessage) (any, error) {
	var in struct {
		Type   string // the call's own, threads.setAccess
		Thread string
		Access string
	}
	d := json.NewDecoder(bytes.NewReader(args))
	d.DisallowUnknownFields()
	if err := d.Decode(&in); err != nil {
		return nil, fmt.Errorf("threads.setAccess sets an access, and nothing else: %v", err)
	}
	level, ok := accessOf[in.Access]
	if !ok {
		names := make([]string, len(accessLevels))
		for i, l := range accessLevels {
			names[i] = strconv.Quote(l.name)
		}
		return nil, fmt.Errorf("%q is no access; the access picker offers %s", in.Access, strings.Join(names, ", "))
	}
	var id api.ID
	if in.Thread != "" {
		var err error
		if id, err = api.ParseID(in.Thread); err != nil {
			return nil, fmt.Errorf("no thread %q", in.Thread)
		}
	}
	if id.IsZero() {
		if a.access != in.Access {
			a.access = in.Access
			a.saveComposer()
		}
		return nil, nil
	}
	t, err := a.pluginFind(args)
	if err != nil {
		return nil, err
	}
	// Sent though the thread shows the level already: one asked for just
	// before may not have landed yet, and this one must come after it.
	return nil, a.sendConfigure(t, api.ThreadConfigure{Access: &level})
}

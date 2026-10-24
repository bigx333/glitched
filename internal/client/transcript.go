package client

import (
	"encoding/json"
	"slices"
	"strings"
	"time"

	"glitched/internal/api"
)

// Transcript is a thread's conversation as its events tell it. Entries are
// in the order things happened. Queue holds the messages still waiting to
// run or to be steered in, in the order they will. Each joins Entries once
// it runs, is steered in or is given up, so a queued message never splits
// the turn it waits behind.
type Transcript struct {
	Entries  []Entry
	Queue    []*UserEntry
	Cursor   api.Cursor // the last event folded in
	Ended    time.Time  // when the last turn folded in ended
	starting api.ID     // the run admitted and not yet started, stopped or ended
}

// Entry is sealed: *UserEntry, *AssistantEntry, *WorkEntry or *RequestEntry.
type Entry interface{ entry() }

type MessageState uint8

const (
	Queued MessageState = iota
	Held                // deferred until something promotes it
	// Claimed: admitted as its run's message, or bound to a live run that
	// it will be steered into.
	Claimed
	Sent
	Cancelled
	Uncertain // handing it to the agent was cut off
)

type UserEntry struct {
	Message api.ID
	Text    string
	State   MessageState
	Reason  string // why it was cancelled, when the engine said
	pos     int64
	run     api.ID // the run that claimed it
}

type AssistantEntry struct {
	Run       api.ID
	Text      string
	Streaming bool // more text may follow
	text      strings.Builder
}

// write appends s. Text shares the builder's bytes, so a long run's deltas
// cost what they hold rather than a copy of all the text before each.
func (a *AssistantEntry) write(s string) {
	a.text.WriteString(s)
	a.Text = a.text.String()
}

// WorkEntry is a run's tool calls with no text between them. Ended is zero
// while one of its items is still going and the run is live.
type WorkEntry struct {
	Run     api.ID
	Items   []WorkStep
	Started time.Time
	Ended   time.Time
}

type WorkStep struct {
	Item   string
	Kind   string
	Title  string
	Status api.WorkStatus
}

type RequestEntry struct {
	Run        api.ID
	Request    api.ID
	Generation uint64
	Kind       string // approval | question
	Body       json.RawMessage
	State      api.RequestState
	Answer     json.RawMessage
	Reason     string // why it was invalidated, when the engine said
}

func (*UserEntry) entry()      {}
func (*AssistantEntry) entry() {}
func (*WorkEntry) entry()      {}
func (*RequestEntry) entry()   {}

// fold builds a transcript from a thread snapshot, which holds every event.
func fold(snap api.Snapshot) Transcript {
	var t Transcript
	for _, e := range snap.Events {
		t.apply(e)
	}
	t.Cursor = snap.Cursor
	return t
}

// apply folds one event in, mirroring how the engine's projection reads
// it. An event at or before the cursor is already in. One this client can't
// decode is skipped, and the cursor still moves past it.
func (t *Transcript) apply(e api.Event) {
	if e.Cursor.Log == t.Cursor.Log && e.Cursor.Seq <= t.Cursor.Seq {
		return
	}
	t.Cursor = e.Cursor
	body, err := e.Decode()
	if err != nil {
		return
	}
	switch b := body.(type) {
	case api.MessageQueued:
		state := Queued
		if b.Held {
			state = Held
		}
		i := slices.IndexFunc(t.Queue, func(u *UserEntry) bool { return u.pos > b.Pos })
		if i < 0 {
			i = len(t.Queue)
		}
		t.Queue = slices.Insert(t.Queue, i, &UserEntry{Message: b.Message, Text: b.Text, State: state, pos: b.Pos})
	case api.MessageClaimed:
		if i := t.waiting(b.Message); i >= 0 {
			t.Queue[i].State, t.Queue[i].run = Claimed, b.Run
		}
	case api.TurnAdmitted:
		if i := t.waiting(b.Run); i >= 0 {
			u := t.Queue[i]
			u.State, u.run = Claimed, b.Run
			t.join(i)
		}
		t.starting = b.Run
	case api.TurnStarted:
		t.started(b.Run)
	case api.TurnRestored:
		// A run restored past starting is unchanged.
		if b.Run == t.starting {
			t.started(b.Run)
		}
	case api.MessageDispatched:
		t.settle(b.Message, Sent, "")
	case api.MessageCancelled:
		t.settle(b.Message, Cancelled, b.Reason)
	case api.MessageUncertain:
		t.settle(b.Message, Uncertain, "")
	case api.AssistantDelta:
		a, ok := t.last().(*AssistantEntry)
		if !ok || a.Run != b.Run {
			a = &AssistantEntry{Run: b.Run, Streaming: true}
			t.add(a)
		}
		a.write(b.Text)
	case api.WorkItem:
		t.work(b, e.At)
	case api.RequestOpened:
		t.add(&RequestEntry{Run: b.Run, Request: b.Request, Generation: b.Generation, Kind: b.Kind, Body: b.Body, State: api.RequestPending})
	case api.RequestAnswer:
		if r := t.request(b.Request); r != nil {
			r.State, r.Answer = api.RequestAnswered, b.Answer
		}
	case api.RequestDelivery:
		if r := t.request(b.Request); r != nil {
			r.State = api.RequestDelivered
		}
	case api.RequestInvalidation:
		if r := t.request(b.Request); r != nil {
			r.State, r.Reason = api.RequestInvalidated, b.Reason
		}
	case api.TurnStopping:
		t.starting = api.ID{}
		t.void(b.Run)
	case api.TurnUncertain:
		t.end(b.Run, Uncertain, e.At)
	case api.TurnEnded:
		t.end(b.Run, Cancelled, e.At)
		t.Ended = e.At
	}
}

// started marks the run's own message sent.
func (t *Transcript) started(run api.ID) {
	t.starting = api.ID{}
	t.settle(run, Sent, "")
}

func (t *Transcript) last() Entry {
	if len(t.Entries) == 0 {
		return nil
	}
	return t.Entries[len(t.Entries)-1]
}

// add appends e. The text before e is done streaming.
func (t *Transcript) add(e Entry) {
	if a, ok := t.last().(*AssistantEntry); ok {
		a.Streaming = false
	}
	t.Entries = append(t.Entries, e)
}

func (t *Transcript) waiting(message api.ID) int {
	return slices.IndexFunc(t.Queue, func(u *UserEntry) bool { return u.Message == message })
}

// join moves Queue[i] into the conversation.
func (t *Transcript) join(i int) {
	u := t.Queue[i]
	t.Queue = slices.Delete(t.Queue, i, i+1)
	t.add(u)
}

// settle gives a message a state past waiting, moving it into the
// conversation if it was still queued.
func (t *Transcript) settle(message api.ID, state MessageState, reason string) {
	if i := t.waiting(message); i >= 0 {
		t.Queue[i].State, t.Queue[i].Reason = state, reason
		t.join(i)
		return
	}
	if u := t.message(message); u != nil {
		u.State, u.Reason = state, reason
	}
}

// message finds a user entry wherever it is.
func (t *Transcript) message(id api.ID) *UserEntry {
	if i := t.waiting(id); i >= 0 {
		return t.Queue[i]
	}
	for i := len(t.Entries) - 1; i >= 0; i-- {
		if u, ok := t.Entries[i].(*UserEntry); ok && u.Message == id {
			return u
		}
	}
	return nil
}

func (t *Transcript) request(id api.ID) *RequestEntry {
	for i := len(t.Entries) - 1; i >= 0; i-- {
		if r, ok := t.Entries[i].(*RequestEntry); ok && r.Request == id {
			return r
		}
	}
	return nil
}

// since returns the entries after run's own message, the only place its
// output can be.
func (t *Transcript) since(run api.ID) []Entry {
	for i := len(t.Entries) - 1; i >= 0; i-- {
		if u, ok := t.Entries[i].(*UserEntry); ok && u.Message == run {
			return t.Entries[i+1:]
		}
	}
	return t.Entries
}

// work upserts an item: in the entry that already holds it, else in the
// newest entry when that is the run's work, else in a new one.
func (t *Transcript) work(w api.WorkItem, at time.Time) {
	step := WorkStep{Item: w.Item, Kind: w.Kind, Title: w.Title, Status: w.Status}
	var into *WorkEntry
	for _, e := range t.since(w.Run) {
		if we, ok := e.(*WorkEntry); ok && we.Run == w.Run {
			if i := slices.IndexFunc(we.Items, func(s WorkStep) bool { return s.Item == w.Item }); i >= 0 {
				into = we
				we.Items[i] = step
				break
			}
		}
	}
	if into == nil {
		if we, ok := t.last().(*WorkEntry); ok && we.Run == w.Run {
			into = we
		} else {
			into = &WorkEntry{Run: w.Run, Started: at}
			t.add(into)
		}
		into.Items = append(into.Items, step)
	}
	into.Ended = time.Time{}
	if !slices.ContainsFunc(into.Items, func(s WorkStep) bool { return s.Status == api.WorkStarted }) {
		into.Ended = at
	}
}

// void invalidates the run's open requests, as the engine does once the run
// stops.
func (t *Transcript) void(run api.ID) {
	for _, e := range t.since(run) {
		if r, ok := e.(*RequestEntry); ok && r.Run == run && r.State == api.RequestPending {
			r.State = api.RequestInvalidated
		}
	}
}

// end closes out a run. The messages it claimed and never delivered get
// released, its open requests are void, and its text and work are over.
func (t *Transcript) end(run api.ID, released MessageState, at time.Time) {
	t.starting = api.ID{}
	for i := 0; i < len(t.Queue); {
		if u := t.Queue[i]; u.run == run && u.State == Claimed {
			u.State = released
			t.join(i)
			continue
		}
		i++
	}
	if u := t.message(run); u != nil && u.State == Claimed {
		u.State = released
	}
	t.void(run)
	for _, e := range t.since(run) {
		switch e := e.(type) {
		case *AssistantEntry:
			if e.Run == run {
				e.Streaming = false
			}
		case *WorkEntry:
			if e.Run == run && e.Ended.IsZero() {
				e.Ended = at
			}
		}
	}
}

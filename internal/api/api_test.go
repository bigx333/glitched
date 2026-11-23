package api

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func TestIDRoundTrip(t *testing.T) {
	for range 1000 {
		id := NewID()
		got, err := ParseID(id.String())
		if err != nil || got != id {
			t.Fatalf("round trip %s: %v %v", id, got, err)
		}
	}
	a := newIDAt(time.UnixMilli(1000))
	b := newIDAt(time.UnixMilli(2000))
	if a.String() >= b.String() {
		t.Fatalf("ids do not sort by time: %s >= %s", a, b)
	}
	if _, err := ParseID("8ZZZZZZZZZZZZZZZZZZZZZZZZZ"); err == nil {
		t.Fatal("accepted an id over 128 bits")
	}
}

func TestCommandJSON(t *testing.T) {
	for _, body := range []Intent{
		MessageDispatch{Text: "hi", Mode: SteerActive},
		ThreadRename{Title: "Websocket loop"},
		ThreadDelete{},
		TurnInterrupt{Drop: true},
		QueueResume{},
		ThreadConfigure{Model: new("Claude Sonnet 5.5"), Effort: new("low"), Fast: new(true), Access: new(AccessFull)},
		ThreadConfigure{Fast: new(false), Access: new(AccessAsk)},
		ThreadConfigure{Effort: new("")},
		ThreadConfigure{},
	} {
		in := Command{ID: NewID(), Target: NewID(), Revision: 3, Body: body}
		b, err := json.Marshal(in)
		if err != nil {
			t.Fatal(err)
		}
		var out Command
		if err := json.Unmarshal(b, &out); err != nil {
			t.Fatal(err)
		}
		if out.ID != in.ID || out.Target != in.Target || out.Revision != 3 || !reflect.DeepEqual(out.Body, in.Body) {
			t.Fatalf("got %+v from %s", out, b)
		}
	}
	var out Command
	if err := json.Unmarshal([]byte(`{"type":"nope","body":{}}`), &out); err == nil {
		t.Fatal("accepted an unknown command type")
	}
}

// A configure gives only what it names. Its wire form leaves the rest out,
// and tells a zero given, as Ask or fast mode off, from a field left out:
// onto a thread's model and access, what it gives takes their place and
// the rest stays.
func TestAConfigureGivesOnlyWhatItNames(t *testing.T) {
	opus := ModelRef{Provider: "claude", Model: "Claude Opus 5.5", Effort: "high", Fast: true}
	for name, tc := range map[string]struct {
		c      ThreadConfigure
		wire   string
		model  ModelRef
		access Access
	}{
		"nothing":      {ThreadConfigure{}, `{}`, opus, AccessFull},
		"the effort":   {ThreadConfigure{Effort: new("low")}, `{"effort":"low"}`, ModelRef{Provider: "claude", Model: "Claude Opus 5.5", Effort: "low", Fast: true}, AccessFull},
		"zeros":        {ThreadConfigure{Effort: new(""), Fast: new(false), Access: new(AccessAsk)}, `{"effort":"","fast":false,"access":0}`, ModelRef{Provider: "claude", Model: "Claude Opus 5.5"}, AccessAsk},
		"all it holds": {ThreadConfigure{Model: new("Claude Sonnet 5.5"), Effort: new("max"), Fast: new(true), Access: new(AccessAutoEdits)}, `{"model":"Claude Sonnet 5.5","effort":"max","fast":true,"access":1}`, ModelRef{Provider: "claude", Model: "Claude Sonnet 5.5", Effort: "max", Fast: true}, AccessAutoEdits},
	} {
		b, err := json.Marshal(tc.c)
		if err != nil || string(b) != tc.wire {
			t.Errorf("%s: its wire form is %s (%v), want %s", name, b, err, tc.wire)
		}
		var back ThreadConfigure
		if err := json.Unmarshal([]byte(tc.wire), &back); err != nil || !reflect.DeepEqual(back, tc.c) {
			t.Errorf("%s: %s reads as %+v (%v)", name, tc.wire, back, err)
		}
		if m, a := back.Onto(opus, AccessFull); m != tc.model || a != tc.access {
			t.Errorf("%s: onto %+v at Full it makes %+v at %v, want %+v at %v", name, opus, m, a, tc.model, tc.access)
		}
	}
}

func TestEventDecode(t *testing.T) {
	bodies := []EventBody{
		ThreadCreated{Workspace: "w", Title: "t", Model: ModelRef{Provider: "p"}, Access: AccessFull},
		MessageQueued{Message: NewID(), Text: "hi", Mode: Defer, Pos: -2, Held: true},
		MessageClaimed{Message: NewID(), Run: NewID()},
		MessageDispatched{Message: NewID()},
		MessageCancelled{Message: NewID(), Reason: "r"},
		MessageUncertain{Message: NewID()},
		MessageHeld{Message: NewID()},
		MessageReleased{Message: NewID()},
		TurnAdmitted{Run: NewID(), Generation: 3},
		TurnStarted{Run: NewID()},
		TurnStopping{Run: NewID()},
		TurnEnded{Run: NewID(), Outcome: OutcomeFailed, Error: "e"},
		TurnRestored{Run: NewID()},
		TurnUncertain{Run: NewID()},
		AssistantDelta{Run: NewID(), Text: "t"},
		WorkItem{Run: NewID(), Item: "c", Kind: "command", Title: "bun test", Status: WorkFailed, Path: "p", Exit: new(1), Output: "1 failed"},
		RequestOpened{Request: NewID(), Run: NewID(), Generation: 1, Dedup: "d", Kind: "approval", Body: json.RawMessage(`{"a":1}`)},
		RequestAnswer{Request: NewID(), Answer: json.RawMessage(`true`)},
		RequestDelivery{Request: NewID()},
		RequestInvalidation{Request: NewID(), Reason: "r"},
		DispositionChanged{Disposition: Disposition{State: Snoozed, Until: time.UnixMilli(1000).UTC()}},
		ThreadRenamed{Title: "t"},
		ThreadDeleted{},
	}
	seen := map[string]bool{}
	for _, b := range bodies {
		payload, err := json.Marshal(b)
		if err != nil {
			t.Fatal(err)
		}
		got, err := Event{Type: EventType(b), Payload: payload}.Decode()
		if err != nil || !reflect.DeepEqual(got, b) {
			t.Fatalf("%s: decoded %#v %v, want %#v", EventType(b), got, err, b)
		}
		seen[EventType(b)] = true
	}
	if len(seen) != len(bodies) {
		t.Fatal("two payloads share a type name")
	}
	if got, err := (Event{Type: "from.the.future", Payload: []byte(`{}`)}).Decode(); got != nil || err != nil {
		t.Fatalf("unknown type: %v %v, want skipped", got, err)
	}
}

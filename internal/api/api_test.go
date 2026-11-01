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
		if out.ID != in.ID || out.Target != in.Target || out.Revision != 3 || out.Body != in.Body {
			t.Fatalf("got %+v from %s", out, b)
		}
	}
	var out Command
	if err := json.Unmarshal([]byte(`{"type":"nope","body":{}}`), &out); err == nil {
		t.Fatal("accepted an unknown command type")
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

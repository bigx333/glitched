package api

import (
	"encoding/json"
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
	in := Command{ID: NewID(), Target: NewID(), Revision: 3, Body: MessageDispatch{Text: "hi", Mode: SteerActive}}
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
	if err := json.Unmarshal([]byte(`{"type":"nope","body":{}}`), &out); err == nil {
		t.Fatal("accepted an unknown command type")
	}
}

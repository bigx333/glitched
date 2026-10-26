package webtarget

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// recorder is a page that answers every script with its answer, noting
// the scripts.
type recorder struct {
	scripts []string
	answer  any
}

func (r *recorder) EvalContext(_ context.Context, code string) (any, error) {
	r.scripts = append(r.scripts, code)
	return r.answer, nil
}

func TestParseRef(t *testing.T) {
	if e, n, err := ParseRef("s41e7"); err != nil || e != 41 || n != 7 {
		t.Errorf("ParseRef(s41e7) = %d, %d, %v", e, n, err)
	}
	for _, bad := range []string{"e7", "s41", "s41e", "ref=s41e7", "S41E7", ""} {
		if _, _, err := ParseRef(bad); err == nil {
			t.Errorf("ParseRef(%q) took it", bad)
		}
	}
}

// Each snapshot has an epoch of its own, which its refs carry.
func TestTakeNumbersSnapshots(t *testing.T) {
	r := &recorder{answer: map[string]any{"epoch": 9, "url": "https://go.dev/", "title": "Go", "refs": 2, "text": `- link "Docs" [ref=s9e1]`}}
	s, err := Take(context.Background(), r, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if s.Title != "Go" || s.Refs != 2 || !strings.Contains(s.Text, "[ref=s9e1]") {
		t.Errorf("snapshot %+v", s)
	}
	Take(context.Background(), r, Options{Full: true, MaxRefs: 5})
	var first, second map[string]any
	for i, out := range []*map[string]any{&first, &second} {
		head, _, _ := strings.Cut(r.scripts[i], ";\n")
		if err := json.Unmarshal([]byte(strings.TrimPrefix(head, "const args = ")), out); err != nil {
			t.Fatalf("script %d's args: %v", i, err)
		}
	}
	if second["epoch"].(float64) != first["epoch"].(float64)+1 {
		t.Errorf("epochs %v then %v", first["epoch"], second["epoch"])
	}
	if first["interactive"] != true || second["interactive"] != false || second["maxRefs"].(float64) != 5 || first["maxChars"].(float64) != 50000 {
		t.Errorf("args %v then %v", first, second)
	}
}

// A malformed ref is refused before the page is asked.
func TestDescribeRefusesNonRefs(t *testing.T) {
	r := &recorder{}
	if _, err := Describe(context.Background(), r, "button"); err == nil || len(r.scripts) != 0 {
		t.Errorf("err %v after %d scripts", err, len(r.scripts))
	}
}

// Verify passes what a ref still names, and fails what changed or isn't
// the snapshot's.
func TestVerify(t *testing.T) {
	s := Snapshot{Epoch: 4, Elements: []Named{{Ref: "s4e1", Role: "button", Name: "Save"}}}
	if err := s.Verify("s4e1", Element{Role: "button", Name: "Save"}); err != nil {
		t.Error(err)
	}
	if err := s.Verify("s4e1", Element{Role: "button", Name: "Delete"}); err == nil {
		t.Error("a renamed element passed")
	}
	if err := s.Verify("s4e2", Element{Role: "button", Name: "Save"}); err == nil {
		t.Error("a ref the snapshot hasn't passed")
	}
}

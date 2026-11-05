package webtarget

import (
	"context"
	"encoding/json"
	"fmt"
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
	for _, bad := range []string{"e7", "s41", "s41e", "ref=s41e7", "S41E7", "", "s99999999999999999999999e1", "s1e99999999999999999999999"} {
		if _, _, err := ParseRef(bad); err == nil {
			t.Errorf("ParseRef(%q) took it", bad)
		}
	}
}

// argsOf is the args a script was run with.
func argsOf(t *testing.T, script string) map[string]any {
	t.Helper()
	head, _, _ := strings.Cut(script, ";\n")
	var args map[string]any
	if err := json.Unmarshal([]byte(strings.TrimPrefix(head, "const args = ")), &args); err != nil {
		t.Fatalf("script's args: %v", err)
	}
	return args
}

// snapshotter is a page answering snapshots as the script would, but as
// its fake changes them.
type snapshotter struct {
	t    *testing.T
	fake func(s map[string]any, epoch uint64)
}

func (p *snapshotter) EvalContext(_ context.Context, code string) (any, error) {
	epoch := uint64(argsOf(p.t, code)["epoch"].(float64))
	ref := fmt.Sprintf("s%de1", epoch)
	s := map[string]any{"epoch": epoch, "url": "https://go.dev/", "title": "Go", "refs": 1,
		"text":     `- link "Docs" [ref=` + ref + "]",
		"elements": []any{map[string]any{"ref": ref, "role": "link", "name": "Docs"}}}
	if p.fake != nil {
		p.fake(s, epoch)
	}
	return s, nil
}

// Each snapshot has an epoch of its own, which its refs carry.
func TestTakeNumbersSnapshots(t *testing.T) {
	r := &recorder{}
	p := &snapshotter{t: t}
	s, err := Take(context.Background(), p, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if s.Title != "Go" || s.Refs != 1 || !strings.Contains(s.Text, fmt.Sprintf("[ref=s%de1]", s.Epoch)) {
		t.Errorf("snapshot %+v", s)
	}
	Take(context.Background(), r, Options{})
	Take(context.Background(), r, Options{Full: true, MaxRefs: 5})
	var first, second map[string]any
	for i, out := range []*map[string]any{&first, &second} {
		head, _, _ := strings.Cut(r.scripts[i], ";\n")
		if err := json.Unmarshal([]byte(strings.TrimPrefix(head, "const args = ")), out); err != nil {
			t.Fatalf("script %d's args: %v", i, err)
		}
	}
	if first["epoch"].(float64) != float64(s.Epoch)+1 || second["epoch"].(float64) != first["epoch"].(float64)+1 {
		t.Errorf("epochs %v then %v", first["epoch"], second["epoch"])
	}
	if first["interactive"] != true || second["interactive"] != false || second["maxRefs"].(float64) != 5 || first["maxChars"].(float64) != 50000 ||
		first["maxTitle"].(float64) != maxTitle || first["maxURL"].(float64) != maxURL {
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

// A snapshot the script wouldn't have taken, as a page faking it answers,
// fails, saying why: another epoch, refs not the epoch's in order, or more
// than the budgets allow.
func TestTakeRefusesWhatTheScriptWouldNotAnswer(t *testing.T) {
	long := func(n int) string { return strings.Repeat("x", n) }
	for name, fake := range map[string]func(s map[string]any, epoch uint64){
		"an earlier epoch": func(s map[string]any, epoch uint64) { s["epoch"] = epoch - 1 },
		"another epoch's ref": func(s map[string]any, epoch uint64) {
			s["elements"] = []any{map[string]any{"ref": fmt.Sprintf("s%de1", epoch+1), "role": "link", "name": "Docs"}}
		},
		"refs out of order": func(s map[string]any, epoch uint64) {
			s["elements"] = []any{map[string]any{"ref": fmt.Sprintf("s%de2", epoch), "role": "link", "name": "Docs"}}
		},
		"not a ref": func(s map[string]any, _ uint64) {
			s["elements"] = []any{map[string]any{"ref": "button", "role": "link"}}
		},
		"refs miscounted": func(s map[string]any, _ uint64) { s["refs"] = 7 },
		"too many refs": func(s map[string]any, epoch uint64) {
			var es []any
			for i := range 6 {
				es = append(es, map[string]any{"ref": fmt.Sprintf("s%de%d", epoch, i+1), "role": "link"})
			}
			s["elements"], s["refs"] = es, len(es)
		},
		"a text too long":  func(s map[string]any, _ uint64) { s["text"] = long(1000 + maxMarker + 1) },
		"a title too long": func(s map[string]any, _ uint64) { s["title"] = long(maxTitle + 1) },
		"a URL too long":   func(s map[string]any, _ uint64) { s["url"] = "https://go.dev/" + long(maxURL) },
		"a name too long": func(s map[string]any, epoch uint64) {
			s["elements"] = []any{map[string]any{"ref": fmt.Sprintf("s%de1", epoch), "role": "link", "name": long(maxName + 1)}}
		},
		"a role too long": func(s map[string]any, epoch uint64) {
			s["elements"] = []any{map[string]any{"ref": fmt.Sprintf("s%de1", epoch), "role": long(maxRole + 1)}}
		},
	} {
		s, err := Take(context.Background(), &snapshotter{t: t, fake: fake}, Options{MaxRefs: 5, MaxChars: 1000})
		if err == nil || !strings.Contains(err.Error(), "not the script's") {
			t.Errorf("%s: took %+v (%v)", name, s, err)
		} else if s.Text != "" || len(s.Elements) != 0 {
			t.Errorf("%s: failed, and returned %+v", name, s)
		}
	}
	// At its budgets, a snapshot is taken.
	at := func(s map[string]any, _ uint64) {
		s["text"], s["title"], s["url"] = long(1000+maxMarker), long(maxTitle), long(maxURL)
	}
	if _, err := Take(context.Background(), &snapshotter{t: t, fake: at}, Options{MaxRefs: 5, MaxChars: 1000}); err != nil {
		t.Errorf("a snapshot at its budgets: %v", err)
	}
}

// An element described past what the script makes fails too.
func TestDescribeRefusesWhatTheScriptWouldNotAnswer(t *testing.T) {
	r := &recorder{answer: map[string]any{"role": "button", "name": strings.Repeat("x", maxName+1)}}
	if e, err := Describe(context.Background(), r, "s4e1"); err == nil || e.Name != "" {
		t.Errorf("described %+v (%v)", e, err)
	}
	r.answer = map[string]any{"role": "button", "name": "Save"}
	if e, err := Describe(context.Background(), r, "s4e1"); err != nil || e.Name != "Save" {
		t.Errorf("described %+v (%v)", e, err)
	}
}

// An action names its element as the snapshot did, for the page script to
// check before it acts, and takes only that snapshot's refs.
func TestActionsActAsTheSnapshotNamed(t *testing.T) {
	s := Snapshot{Epoch: 9, Elements: []Named{{Ref: "s9e1", Role: "link", Name: "Docs"}, {Ref: "s9e2", Role: "textbox", Name: "Search"}}}
	r := &recorder{answer: map[string]any{"done": true}}
	ctx := context.Background()
	if err := Click(ctx, r, s, "s9e1"); err != nil {
		t.Fatal(err)
	}
	if err := Type(ctx, r, s, "s9e2", "go", true); err != nil {
		t.Fatal(err)
	}
	click, typed := argsOf(t, r.scripts[0]), argsOf(t, r.scripts[1])
	if click["op"] != "act" || click["action"] != "click" || click["ref"] != "s9e1" || click["role"] != "link" || click["name"] != "Docs" {
		t.Errorf("a click ran with %v", click)
	}
	if typed["action"] != "type" || typed["text"] != "go" || typed["submit"] != true || typed["role"] != "textbox" || typed["name"] != "Search" {
		t.Errorf("typing ran with %v", typed)
	}
	for _, err := range []error{
		Click(ctx, r, s, "s8e1"),
		Click(ctx, r, s, "s9e3"),
		Type(ctx, r, s, "s9e2", strings.Repeat("é", MaxTyped+1), false),
	} {
		if err == nil {
			t.Error("an action the snapshot doesn't allow ran")
		}
	}
	if len(r.scripts) != 2 {
		t.Errorf("%d scripts ran, want 2", len(r.scripts))
	}
	// A page answering for the script, without it, fails.
	if err := Click(ctx, &recorder{answer: map[string]any{}}, s, "s9e1"); err == nil {
		t.Error("an answer not the script's passed")
	}
}

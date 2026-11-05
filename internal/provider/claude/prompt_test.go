package claude

import (
	"encoding/json"
	"fmt"
	"testing"

	"glitched/internal/plain"
)

// A line break in a tool's key shows as JSON spells it, in the quoted
// key, apart from a key that spells the escape out: the two keys are
// different arguments to the tool.
func TestTheCardShowsAKeysLineBreak(t *testing.T) {
	broken := argsCard(t, `{"sql: SELECT 1\nx":"y","sql":"DROP TABLE users"}`)
	if want := `"sql: SELECT 1\nx": "y"` + "\n" + `sql: "DROP TABLE users"`; broken != want {
		t.Errorf("the card shows %q, want %q", broken, want)
	}
	if spelled := argsCard(t, `{"sql: SELECT 1\\u{A}x":"y","sql":"DROP TABLE users"}`); spelled == broken {
		t.Errorf("a key with a line break and a key that spells it out both show as %q", broken)
	}
}

// argsCard is the args text the card shows for a tool's input.
func argsCard(t *testing.T, input string) string {
	t.Helper()
	e, _ := newTurn("/w").ask("ask", canUseTool{ToolName: "mcp__db__exec", Input: json.RawMessage(input)})
	var b struct{ Args string }
	if err := json.Unmarshal(e.Body, &b); err != nil {
		t.Fatal(err)
	}
	s, _ := plain.Text(b.Args, true)
	return s
}

// Inputs a tool reads differently show differently: a string apart from
// the value it spells, and a key with a separator apart from a key and a
// value.
func TestTheCardShowsEachToolInputApart(t *testing.T) {
	for _, p := range [][2]string{
		{`{"env":"prod","dry_run":"true: yes"}`, `{"env":"prod","dry_run: true":"yes"}`},
		{`{"force":false}`, `{"force":"false"}`},
		{`{"limit":0}`, `{"limit":"0"}`},
		{`{"paths":["a","b"]}`, `{"paths":"[\"a\",\"b\"]"}`},
		{`{"x":null}`, `{"x":"null"}`},
		{`{"sql: DROP":"x"}`, `{"sql":"DROP: x"}`},
		{`{"a":"x\ny"}`, `{"a":"x","  y":1}`},
	} {
		if a, b := argsCard(t, p[0]), argsCard(t, p[1]); a == b {
			t.Errorf("%s and %s both show as %q", p[0], p[1], a)
		}
	}
}

// Every input of up to two fields, from keys and values built to collide,
// shows as no other does.
func TestTheCardShowsNoTwoToolInputsAlike(t *testing.T) {
	keys := []string{"a", "b", "", " ", "a: b", "a:", ":", "\"", "\\", "a\nb", "  a", "a b", "|"}
	values := []string{`false`, `"false"`, `true`, `0`, `"0"`, `1.0`, `null`, `"null"`, `[]`, `"[]"`, `["a"]`, `"[\"a\"]"`,
		`{}`, `"{}"`, `{"a":"b"}`, `""`, `" "`, `"a"`, `"a "`, `"a\nb"`, `"a\n"`, `"\n"`, `"a: b"`, `"a\n  b: c"`, `"a\nb: c"`,
		`"\""`, `"\\"`, `"|"`, `"\\u{A}"`, `"é"`}
	var inputs []string
	for _, k := range keys {
		kj, _ := json.Marshal(k)
		for _, v := range values {
			inputs = append(inputs, fmt.Sprintf(`{%s:%s}`, kj, v))
			for _, k2 := range keys[:4] {
				k2j, _ := json.Marshal(k2)
				for _, v2 := range values {
					inputs = append(inputs, fmt.Sprintf(`{%s:%s,%s:%s}`, kj, v, k2j, v2))
				}
			}
		}
	}
	seen := map[string]string{}
	for _, in := range inputs {
		shown := argsCard(t, in)
		if prev, ok := seen[shown]; ok && prev != in {
			t.Fatalf("%s and %s both show as %q", prev, in, shown)
		}
		seen[shown] = in
	}
}

// A lone surrogate in a tool's input, which Go's decoder reads as U+FFFD,
// shows apart from U+FFFD and from text that spells its escape out: the
// tool reads the three differently. A pair shows as the character it
// spells.
func TestTheCardShowsALoneSurrogateApart(t *testing.T) {
	for _, p := range [][2]string{
		{`{"sql":"\udc00"}`, `{"sql":"�"}`},
		{`{"sql":"\udc00"}`, `{"sql":"\\udc00"}`},
		{`{"sql":"\ud800x"}`, `{"sql":"�x"}`},
		{`{"sql":"\ud800\ud800"}`, `{"sql":"��"}`},
		{`{"\udc00":1}`, `{"�":1}`},
		{`{"\udc00":1}`, `{"\\udc00":1}`},
		{`{"sql":"a\udc00\nb"}`, `{"sql":"a�\nb"}`},
		{`{"sql":"a\udc00\nb"}`, `{"sql":"a\\udc00\nb"}`},
	} {
		if a, b := argsCard(t, p[0]), argsCard(t, p[1]); a == b {
			t.Errorf("%s and %s both show as %q", p[0], p[1], a)
		}
	}
	if pair, char := argsCard(t, `{"sql":"😀"}`), argsCard(t, `{"sql":"😀"}`); pair != char {
		t.Errorf("a surrogate pair shows as %q, want %q as its character does", pair, char)
	}
}

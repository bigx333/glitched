package claude

import (
	"encoding/json"
	"testing"

	"glitched/internal/plain"
)

// A line break in a tool's key reaches the card as an escape, so the card
// flags it, and shows it apart from a key that spells the escape out: the
// two keys are different arguments to the tool.
func TestTheCardFlagsAKeysLineBreak(t *testing.T) {
	card := func(input string) (string, bool) {
		e, _ := newTurn("/w").ask("ask", canUseTool{ToolName: "mcp__db__exec", Input: json.RawMessage(input)})
		var b struct{ Args string }
		if err := json.Unmarshal(e.Body, &b); err != nil {
			t.Fatal(err)
		}
		return plain.Text(b.Args, true)
	}
	broken, flagged := card(`{"sql: SELECT 1\nx":"y","sql":"DROP TABLE users"}`)
	if !flagged {
		t.Errorf("the card shows %q with no flag, for a key with a line break", broken)
	}
	if spelled, _ := card(`{"sql: SELECT 1\\u{A}x":"y","sql":"DROP TABLE users"}`); spelled == broken {
		t.Errorf("a key with a line break and a key that spells it out both show as %q", broken)
	}
}

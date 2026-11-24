package plugins

import (
	"strings"
	"testing"

	"glitched/internal/api"
)

// threads.create refuses arguments it can't take whole, and says which.
func TestParseCreateRefusesWhatItCantTake(t *testing.T) {
	const model = `"model": {"provider": "codex", "model": "gpt-6-luna"}`
	for _, c := range []struct{ args, want string }{
		{`{"prompt": " "}`, "needs a prompt"},
		{`{"prompt": "go", "modle": {}}`, "unknown field"},
		{`{"prompt": "go", "model": {"provider": "codex"}}`, "needs its provider and its model"},
		{`{"prompt": "go", "model": {"provider": "codex", "model": "m", "speed": 2}}`, "unknown field"},
		{`{"prompt": "go", "model": {"provider": "codex", "model": "m", "fast": "yes"}}`, "cannot unmarshal"},
		{`{"prompt": "go", "model": {"provider": "co\u0000dex", "model": "m"}}`, "no control characters"},
		{`{"prompt": "go", "model": {"provider": "codex", "model": "m\nx"}}`, "no control characters"},
		{`{"prompt": "go", "model": {"provider": "codex", "model": "m", "effort": "low\ty"}}`, "no control characters"},
		{`{"prompt": "go", "worktree": {"base": "--output=x"}}`, "no branch or commit"},
		{`{"prompt": "go", "worktree": {"base": "main\nx"}}`, "no branch or commit"},
		{`{"prompt": "go", "worktree": {"from": "main"}}`, "unknown field"},
		{`{"prompt": "go", "worktree": {"guarded": false}}`, "unknown field"},
		{`{"prompt": "go", ` + model + `, "key": {"at": "2026-11-24T07:00:00Z"}}`, "an id of 1 to 200 bytes"},
		{`{"prompt": "go", ` + model + `, "key": {"id": "` + strings.Repeat("k", 201) + `", "at": "2026-11-24T07:00:00Z"}}`, "an id of 1 to 200 bytes"},
		{`{"prompt": "go", ` + model + `, "key": {"id": "k\u0000x", "at": "2026-11-24T07:00:00Z"}}`, "no control characters"},
		{`{"prompt": "go", ` + model + `, "key": {"id": "k"}}`, "since 1970"},
		{`{"prompt": "go", ` + model + `, "key": {"id": "k", "at": "1969-12-31T23:59:59Z"}}`, "since 1970"},
		{`{"prompt": "go", ` + model + `, "key": {"id": "k", "at": "2026-11-24 07:00"}}`, "cannot parse"},
		{`{"prompt": "go", "key": {"id": "k", "at": "2026-11-24T07:00:00Z"}}`, "a key needs a model"},
		{`{"prompt": "go", "access": "root"}`, "is no access"},
	} {
		if _, err := ParseCreate([]byte(c.args)); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want an error saying %q", c.args, err, c.want)
		}
	}
}

// A create takes the call's own type, as glitched.threads.create sends it,
// and a worktree a plugin asks for is always guarded.
func TestParseCreateTakesTheCallsEnvelope(t *testing.T) {
	c, err := ParseCreate([]byte(`{"type": "threads.create", "prompt": "go", "title": "Say hi", "worktree": {"base": "main"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if c.Title != "Say hi" || c.Worktree == nil || *c.Worktree != (api.NewWorktree{Base: "main", Guarded: true}) {
		t.Fatalf("parsed %+v with worktree %+v", c, c.Worktree)
	}
}

// A key's ids are the same on every repeat and for one instant however
// it is written, and another plugin's, another key's or another instant's
// are others, down to the nanosecond.
func TestACreateKeysIDsAreItsPluginsAndInstantsAlone(t *testing.T) {
	ids := func(plugin, id, at string) [2]api.ID {
		t.Helper()
		c, err := ParseCreate([]byte(`{"prompt": "go", "model": {"provider": "codex", "model": "m"}, "key": {"id": "` + id + `", "at": "` + at + `"}}`))
		if err != nil {
			t.Fatal(err)
		}
		thread, cmd, err := c.Key.IDs(plugin)
		if err != nil {
			t.Fatal(err)
		}
		return [2]api.ID{thread, cmd}
	}
	first := ids("schedules", "k", "2026-11-24T07:00:00Z")
	if first[0] == first[1] {
		t.Fatal("the thread and its command share an id")
	}
	for _, same := range [][3]string{
		{"schedules", "k", "2026-11-24T07:00:00Z"},
		{"schedules", "k", "2026-11-24T09:00:00+02:00"},
		{"schedules", "k", "2026-11-24T07:00:00.000000000Z"},
	} {
		if got := ids(same[0], same[1], same[2]); got != first {
			t.Errorf("%v derived %v, want the first's %v", same, got, first)
		}
	}
	seen := map[[2]api.ID][3]string{first: {"schedules", "k", "2026-11-24T07:00:00Z"}}
	for _, other := range [][3]string{
		{"schedules-copy", "k", "2026-11-24T07:00:00Z"},
		{"schedules", "k2", "2026-11-24T07:00:00Z"},
		{"schedules", "k", "2026-11-24T07:00:00.0000001Z"},
		{"schedules", "k", "2026-11-24T07:00:00.000000101Z"},
	} {
		got := ids(other[0], other[1], other[2])
		if was, ok := seen[got]; ok {
			t.Errorf("%v derived the ids of %v", other, was)
		}
		seen[got] = other
	}
}

package luau

import (
	"errors"
	"strings"
	"testing"
)

const prelude = `return { echo = function(chunk) return tostring(chunk()) end }`

func TestCall(t *testing.T) {
	vm, err := New(prelude)
	if err != nil {
		t.Fatal(err)
	}
	defer vm.Close()
	cases := []struct{ chunk, want, err string }{
		{"return 1 + 2", "3", ""},
		{"return (string.rep('a', 3))", "aaa", ""},
		{"error('boom')", "", "boom"},
		{"return (", "", "Expected"},
		{"local ok = pcall(error, 'x') return ok", "false", ""},
		{"while true do end", "", "step budget"},
		{"while true do pcall(function() while true do end end) end", "", "step budget"},
		{"local t = {} for i = 1, 1e9 do t[i] = string.rep('x', 8 * 1048576) .. i end", "", "memory"},
		{"while true do local s = string.rep('x', 1e5) end", "", "time budget"},
		{"print('hi')", "", "attempt to call a nil value"},
		{"return type(io) .. type(os) .. type(require) .. type(debug) .. type(loadstring) .. type(getfenv)", "nilnilnilnilnilnil", ""},
		{"string.upper = nil", "", "readonly"},
		{"return " + strings.Repeat("1+", 200000) + "1", "", "larger than 256 KiB"},
		{"return 'still alive'", "still alive", ""},
	}
	for _, c := range cases {
		got, err := vm.Call("echo", "test", c.chunk)
		var se *ScriptError
		switch {
		case c.err == "" && err != nil:
			t.Errorf("%q: %v", c.chunk, err)
		case c.err != "" && (!errors.As(err, &se) || !strings.Contains(se.Message, c.err)):
			t.Errorf("%q: error %v, want %q", c.chunk, err, c.err)
		case got != c.want:
			t.Errorf("%q: got %q, want %q", c.chunk, got, c.want)
		}
	}
}

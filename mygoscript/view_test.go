package mygoscript

import (
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

const counter = `
return {
	state = { count = 0 },
	view = function(state)
		return ui.column({ key = "counter" }, {
			ui.text("Count: " .. state.count),
			ui.button("Increment", { on = "increment" }),
			ui.button("Break", { on = "explode" }),
		})
	end,
	actions = {
		increment = function(state) return { count = state.count + 1 } end,
		explode = function() error("kaboom") end,
	},
}`

// waitFor frames tt until text shows: the VM starts off the UI thread.
func waitFor(t *testing.T, tt *ui.Tester, text string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for !tt.HasText(text) {
		if time.Now().After(deadline) {
			t.Fatalf("%q never showed; texts %q", text, tt.Texts())
		}
		time.Sleep(10 * time.Millisecond)
		tt.Frame()
	}
}

func TestCounter(t *testing.T) {
	p := New("counter.luau", counter)
	defer p.Close()
	tt := ui.NewTester(func(c *ui.Context) { View(c, p) }, 400, 300)
	waitFor(t, tt, "Count: 0")

	for range 3 {
		if err := tt.Click("Increment"); err != nil {
			t.Fatal(err)
		}
	}
	if !tt.HasText("Count: 3") || p.Value("count") != 3.0 {
		t.Fatalf("after 3 clicks: texts %q, count %v", tt.Texts(), p.Value("count"))
	}

	if err := tt.Click("Break"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("Script error in counter.luau") || !tt.HasText("Count: 3") {
		t.Fatalf("after a failing action: %q", tt.Texts())
	}
	if !strings.Contains(strings.Join(tt.Texts(), "\n"), "kaboom") {
		t.Fatalf("error message missing: %q", tt.Texts())
	}

	// The host's state outlives the VM: a restart draws the same count.
	if err := tt.Click("Restart script"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, "Count: 3")
	if tt.HasText("Script error in counter.luau") {
		t.Fatalf("error still shown after restart: %q", tt.Texts())
	}
	if err := tt.Click("Increment"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("Count: 4") {
		t.Fatalf("after restart and a click: %q", tt.Texts())
	}
}

func TestScriptErrorsShowInPlace(t *testing.T) {
	for name, src := range map[string]string{
		"syntax":     "return {",
		"no view":    "return {}",
		"bad widget": "return { view = function() return { kind = 'iframe' } end }",
		"sandbox":    "return { view = function() return ui.text(io.open('/etc/passwd')) end }",
		"bad utf8":   "return { view = function() return ui.text(string.char(255)) end }",
		"bad state":  "return { state = { f = function() end }, view = function() return ui.text('x') end }",
	} {
		t.Run(name, func(t *testing.T) {
			p := New(name, src)
			defer p.Close()
			tt := ui.NewTester(func(c *ui.Context) { View(c, p) }, 400, 300)
			waitFor(t, tt, "Script error in "+name)
		})
	}
}

func TestKeysStayUnique(t *testing.T) {
	src := `return { view = function() return ui.column({
		ui.button("A", { key = "button#1.0" }), ui.button("B"), ui.button("C", { key = "x" }), ui.button("D", { key = "x" }),
	}) end }`
	p := New("keys", src)
	defer p.Close()
	tt := ui.NewTester(func(c *ui.Context) { View(c, p) }, 400, 300)
	waitFor(t, tt, "D")
}

func TestLuaString(t *testing.T) {
	in := "a\"b\\c\nd\x00é"
	p := New("echo", `return { state = { s = "" }, view = function(s) return ui.text(s.s) end }`)
	defer p.Close()
	p.state["s"] = in
	tt := ui.NewTester(func(c *ui.Context) { View(c, p) }, 400, 300)
	waitFor(t, tt, in)
}

package mygoscript

import (
	"reflect"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

func regressionPlugin(t *testing.T, src string) (*Plugin, *ui.Tester) {
	t.Helper()
	p := New("review", src)
	t.Cleanup(p.Close)
	tt := ui.NewTester(func(c *ui.Context) { View(c, p) }, 800, 800)
	tt.Frame()
	if p.err != nil || len(p.faults) > 0 {
		t.Fatalf("initial: %v %v", p.err, p.faults)
	}
	return p, tt
}

func TestRegressionCheckboxBothHandlers(t *testing.T) {
	p, tt := regressionPlugin(t, `return { state={checked=false,clicks=0,changes=0}, view=function(s) return ui.checkbox("Check", {bind="checked",onChange=function(s) return {changes=s.changes+1} end,onClick=function(s) return {clicks=s.clicks+1} end}) end }`)
	if err := tt.Click("Check"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Frame()
	t.Logf("checked=%v clicks=%v changes=%v err=%v", p.Value("checked"), p.Value("clicks"), p.Value("changes"), p.err)
	if p.Value("clicks") != 1.0 || p.Value("changes") != 1.0 {
		t.Fail()
	}
}

func TestRegressionUnboundSeed(t *testing.T) {
	p, tt := regressionPlugin(t, `return {state={seed=true}, view=function(s) return ui.column({ui.checkbox("Remember",{key="remember",value=s.seed}),ui.button("Reseed",{onClick=function() return {seed=false} end})}) end }`)
	if err := tt.Click("Reseed"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	t.Logf("local=%v seed=%v", p.local, p.Value("seed"))
	if err := tt.Click("Remember"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	t.Logf("after click: local=%v", p.local)
	if p.local["review/remember@bind"] != false {
		t.Fail()
	}
}

func TestRegressionAlertBothHandlers(t *testing.T) {
	p, tt := regressionPlugin(t, `return {state={open=true,chosen=-1,changes=0}, view=function(s) return ui.alertDialog("Question",{bind="open",buttons={"Cancel","OK"},onChoose=function(s,v) return {chosen=v} end,onChange=function(s) return {changes=s.changes+1} end}) end}`)
	if err := tt.Click("OK"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Frame()
	t.Logf("open=%v chosen=%v changes=%v err=%v", p.Value("open"), p.Value("chosen"), p.Value("changes"), p.err)
	if p.Value("chosen") != 1.0 || p.Value("changes") != 1.0 {
		t.Fail()
	}
}

func TestRegressionCheckboxGroup(t *testing.T) {
	p, tt := regressionPlugin(t, `return {state={a=false,b=false}, view=function(s) return ui.checkboxGroup("All",{ui.checkbox("One",{bind="a"}),ui.checkbox("Two",{bind="b"})}) end}`)
	if err := tt.Click("All"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Frame()
	t.Logf("a=%v b=%v", p.Value("a"), p.Value("b"))
	if p.Value("a") != true || p.Value("b") != true {
		t.Fail()
	}
}

func TestRegressionListKeyedIdentity(t *testing.T) {
	p, tt := regressionPlugin(t, `return {state={prepend=false,a="",b="",z=""}, view=function(s)
 local kids={ui.textInput({key="a",bind="a",label="A"}),ui.textInput({key="b",bind="b",label="B"})}
 if s.prepend then table.insert(kids,1,ui.textInput({key="z",bind="z",label="Z"})) end
 return ui.column({ui.list({key="list",height=200},kids),ui.button("Prepend",{onClick=function() return {prepend=true} end})})
 end}`)
	if err := tt.Click("A"); err != nil {
		t.Fatal(err)
	}
	tt.Type("x")
	tt.Frame()
	t.Logf("before a=%v b=%v z=%v", p.Value("a"), p.Value("b"), p.Value("z"))
	p.fire(newProps(p.tree.Children[1]).fn("onClick"), nil)
	tt.Frame()
	tt.Type("y")
	tt.Frame()
	t.Logf("after a=%v b=%v z=%v err=%v faults=%v", p.Value("a"), p.Value("b"), p.Value("z"), p.err, p.faults)
	if p.Value("a") != "xy" {
		t.Fail()
	}
}

func TestRegressionInputSubmitAndChange(t *testing.T) {
	p, tt := regressionPlugin(t, `return {state={text="",changes=0,submits=0},view=function(s) return ui.textInput({bind="text",label="Name",onChange=function(s) return {changes=s.changes+1} end,onSubmit=function(s) return {submits=s.submits+1} end}) end}`)
	if err := tt.Click("Name"); err != nil {
		t.Fatal(err)
	}
	tt.Type("x")
	tt.Frame()
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	t.Logf("text=%v changes=%v submits=%v", p.Value("text"), p.Value("changes"), p.Value("submits"))
	if p.Value("changes") != 1.0 || p.Value("submits") != 1.0 {
		t.Fail()
	}
}

func TestRegressionListKeyedDuringTyping(t *testing.T) {
	p, tt := regressionPlugin(t, `return {state={prepend=false,a="",b="",z=""}, view=function(s)
 local kids={ui.textInput({key="a",bind="a",label="A",onChange=function() return {prepend=true} end}),ui.textInput({key="b",bind="b",label="B"})}
 if s.prepend then table.insert(kids,1,ui.textInput({key="z",bind="z",label="Z"})) end
 return ui.list({height=200},kids)
 end}`)
	if err := tt.Click("A"); err != nil {
		t.Fatal(err)
	}
	tt.Type("x")
	tt.Frame()
	tt.Type("y")
	tt.Frame()
	t.Logf("a=%v b=%v z=%v", p.Value("a"), p.Value("b"), p.Value("z"))
	if p.Value("a") != "xy" {
		t.Fail()
	}
}

func TestRegressionRichTextColors(t *testing.T) {
	p, tt := regressionPlugin(t, `return {view=function() return ui.richText({spans={{text="red",color="#ff0000",underline=true,decorationColor="#ff0000"}}}) end}`)
	pr := newProps(*p.tree)
	s := spans(pr)
	t.Logf("spans=%+v texts=%v", s, tt.Texts())
	if len(s) != 1 || s[0].Color.R != 255 || s[0].Color.A != 255 {
		t.Fail()
	}
}

func TestRegressionEmptyPropTable(t *testing.T) {
	p := New("review", `return {view=function() return ui.text("hello",{hovered={}}) end}`)
	defer p.Close()
	tt := ui.NewTester(func(c *ui.Context) { View(c, p) }, 800, 800)
	tt.Frame()
	t.Logf("faults=%v", p.faults)
	if len(p.faults) > 0 {
		t.Fail()
	}
}

// A wrong-type seed on an unbound widget keeps its error on every pass and
// stores nothing, instead of showing the error once and then drawing the
// widget with its default value.
func TestRegressionInvalidSeedKeepsItsError(t *testing.T) {
	for kind, view := range map[string]string{
		"checkbox": `ui.checkbox("Check", { key = "check", value = "true" })`,
		"slider":   `ui.slider({ key = "check", value = "loud", min = 0, max = 1 })`,
		"input":    `ui.textInput({ key = "check", value = 42 })`,
	} {
		t.Run(kind, func(t *testing.T) {
			p := New("seed", `return { view = function() return ui.column({ `+view+` }) end }`)
			defer p.Close()
			tt := ui.NewTester(func(c *ui.Context) { View(c, p) }, 600, 400)
			for pass := range 3 {
				tt.Frame()
				p.mu.Lock()
				faults, local := strings.Join(p.faults, "\n"), len(p.local)
				p.mu.Unlock()
				if !strings.Contains(faults, "does not suit this widget") {
					t.Fatalf("pass %d: the bad seed's error is gone: faults %q", pass, faults)
				}
				if local != 0 {
					t.Fatalf("pass %d: a bad seed was stored: %v", pass, p.local)
				}
			}
		})
	}
}

// A valid seed of each value type is stored on the first pass and keeps the
// widget's value when the script later seeds it differently.
func TestRegressionValidSeedsAreStored(t *testing.T) {
	for kind, c := range map[string]struct {
		widget string
		want   any
	}{
		"checkbox": {`ui.checkbox("Check", { key = "w", value = s.seed == 0 })`, true},
		"slider":   {`ui.slider({ key = "w", value = 0.25 + s.seed, min = 0, max = 2 })`, 0.25},
		"input":    {`ui.textInput({ key = "w", value = "first" .. s.seed })`, "first0"},
		"tokens":   {`ui.tokenField({ key = "w", value = { "a" .. s.seed } })`, []any{"a0"}},
		"color":    {`ui.colorWell({ key = "w", value = if s.seed == 0 then "#ff0000ff" else "#00ff00ff" })`, "#ff0000ff"},
	} {
		t.Run(kind, func(t *testing.T) {
			src := `return { state = { seed = 0 }, view = function(s) return ui.column({ ` + c.widget + `, ui.button("Reseed", { onClick = function() return { seed = 1 } end }) }) end }`
			p := New("seed", src)
			defer p.Close()
			tt := ui.NewTester(func(c *ui.Context) { View(c, p) }, 600, 400)
			tt.Frame()
			if err := tt.Click("Reseed"); err != nil {
				t.Fatal(err)
			}
			tt.Frame()
			tt.Frame()
			p.mu.Lock()
			defer p.mu.Unlock()
			if len(p.faults) > 0 || p.err != nil {
				t.Fatalf("faults %q, error %v", p.faults, p.err)
			}
			if p.state["seed"] != 1.0 {
				t.Fatalf("the reseed did not run: %v", p.state)
			}
			got := p.local["seed/w@bind"]
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("stored %#v, want the first seed %#v (local %v)", got, c.want, p.local)
			}
		})
	}
}

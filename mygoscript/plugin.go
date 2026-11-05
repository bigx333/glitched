// Package mygoscript lets a Luau script describe native UI that the Go host
// draws with MyGo's ui package. The script returns a widget tree from its
// view function; the host owns the state the tree is built from, runs the
// script's handlers when the user acts, and rebuilds. See DESIGN.md.
package mygoscript

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/egoist/mygo/ui"

	"glitched/mygoscript/internal/luau"
)

//go:embed prelude.luau
var preludeBody string

// prelude is preludeBody after the table of widget kinds it builds `ui`
// from, so the Go registry is the one list of kinds.
var prelude = sync.OnceValue(func() string {
	kinds := make([]string, 0, len(widgets))
	for k := range widgets {
		kinds = append(kinds, k)
	}
	slices.Sort(kinds)
	var b strings.Builder
	b.WriteString("local KINDS = {")
	for _, k := range kinds {
		fmt.Fprintf(&b, "[%s]=%s,", luaString(k), luaString(widgets[k].lead))
	}
	b.WriteString("}\n")
	return b.String() + preludeBody
})

// Plugin is one script and the state it is drawn from. The state belongs
// to the host: it survives errors and reloads of the script's VM.
type Plugin struct {
	name, source string

	mu      sync.Mutex
	started bool
	vm      *luau.VM
	// state holds what the script sees: strings, numbers, booleans and
	// lists of strings. Widgets bound to a key edit it.
	state map[string]any
	// local holds the values of widgets bound to no key, by widget path,
	// and handles their Go-side state (a list's scroll and selection).
	local   map[string]any
	handles map[string]any
	svgs    map[string]*ui.SVG
	tree    *node
	// faults are the widget errors the last pass showed in place.
	faults []string
	dirty  bool
	err    error
}

// New returns a plugin that runs source. name labels it in error messages.
// Nothing runs until a View shows it.
func New(name, source string) *Plugin {
	return &Plugin{
		name: name, source: source,
		state: map[string]any{}, local: map[string]any{},
		handles: map[string]any{}, svgs: map[string]*ui.SVG{},
	}
}

// Close stops the plugin's VM. A View that shows it again starts a new one.
func (p *Plugin) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stop()
	p.started = false
}

// Value is the state value at key, as the script's handlers and the
// widgets bound to it left it.
func (p *Plugin) Value(key string) any {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.state[key]
}

// start boots a VM and mounts the script, on the UI thread: both take well
// under a millisecond. The caller holds mu.
func (p *Plugin) start() {
	p.started = true
	vm, err := luau.New(prelude())
	if err == nil {
		p.vm = vm
		err = p.mount()
	}
	p.fail(err)
}

func (p *Plugin) mount() error {
	out, err := p.vm.Call("mount", p.name, p.source)
	if err != nil {
		return err
	}
	init, err := decodeState(out)
	if err != nil {
		return err
	}
	// The script's initial state fills only the keys the host lacks, so a
	// reload keeps what the user did.
	for k, v := range init {
		if _, ok := p.state[k]; !ok {
			p.state[k] = v
		}
	}
	return p.render()
}

func (p *Plugin) render() error {
	p.dirty = false
	out, err := p.vm.Call("render", p.name+" view", "return "+luaTable(p.state))
	if err != nil {
		return err
	}
	var n node
	if err := json.Unmarshal([]byte(out), &n); err != nil {
		return fmt.Errorf("decoding the view: %w", err)
	}
	p.tree = &n
	return nil
}

// fire runs the handler f, from the tree that named it, with the event's
// value, merges the patch it returns into the state and renders it.
func (p *Plugin) fire(f *fnRef, value any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.vm == nil {
		return
	}
	out, err := p.vm.Call("act", p.name+" handler", "return "+luaString(f.Fn)+", "+luaTable(p.state)+", "+luaValue(value))
	if err == nil {
		var patch map[string]any
		if patch, err = decodeState(out); err == nil {
			for k, v := range patch {
				p.state[k] = v
			}
			err = p.render()
		}
	}
	p.fail(err)
}

// get reads a widget's value: the state at key, or its own at path id.
func (p *Plugin) get(key, id string) (any, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if key != "" {
		v, ok := p.state[key]
		return v, ok
	}
	v, ok := p.local[id]
	return v, ok
}

// set stores a widget's new value, and reports whether it changed. A
// bound value changes what the script sees, so the view renders again.
func (p *Plugin) set(key, id string, v any) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	m, k := p.local, id
	if key != "" {
		m, k = p.state, key
	}
	if old, ok := m[k]; ok && reflect.DeepEqual(old, v) {
		return false
	}
	m[k] = v
	if key != "" {
		p.dirty = true
	}
	return true
}

// handle is the Go-side state of the widget at id, made by mk once.
func handle[T any](p *Plugin, id string, mk func() *T) *T {
	p.mu.Lock()
	defer p.mu.Unlock()
	if h, ok := p.handles[id].(*T); ok {
		return h
	}
	h := mk()
	p.handles[id] = h
	return h
}

func (p *Plugin) svg(src string) (*ui.SVG, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if s, ok := p.svgs[src]; ok {
		return s, nil
	}
	s, err := ui.ParseSVG([]byte(src))
	if err != nil {
		return nil, err
	}
	p.svgs[src] = s
	return s, nil
}

// reload throws the VM away and starts a new one on the same state.
func (p *Plugin) reload() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stop()
	p.start()
}

// fail records err for the view to show. A script error keeps the VM; any
// other error has already closed it.
func (p *Plugin) fail(err error) {
	p.err = err
	if err != nil {
		if se := (*luau.ScriptError)(nil); !errors.As(err, &se) {
			p.stop()
		}
	}
}

func (p *Plugin) stop() {
	if p.vm != nil {
		p.vm.Close()
		p.vm = nil
	}
	p.tree = nil
}

func decodeState(s string) (map[string]any, error) {
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return nil, fmt.Errorf("decoding state: %w", err)
	}
	return m, nil
}

// luaTable writes state as a Luau table constructor.
func luaTable(state map[string]any) string {
	keys := make([]string, 0, len(state))
	for k := range state {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	var b strings.Builder
	b.WriteString("{")
	for _, k := range keys {
		b.WriteString("[" + luaString(k) + "]=" + luaValue(state[k]) + ",")
	}
	b.WriteString("}")
	return b.String()
}

// luaValue writes a state value as a Luau expression. The prelude and the
// widgets only put strings, finite numbers, booleans and lists of strings
// in the state.
func luaValue(v any) string {
	switch v := v.(type) {
	case nil:
		return "nil"
	case string:
		return luaString(v)
	case float64:
		if math.IsInf(v, 0) || math.IsNaN(v) {
			panic("mygoscript: non-finite number in state")
		}
		return strconv.FormatFloat(v, 'g', -1, 64)
	case bool:
		return strconv.FormatBool(v)
	case []any:
		parts := make([]string, len(v))
		for i, s := range v {
			parts[i] = luaValue(s)
		}
		return "{" + strings.Join(parts, ",") + "}"
	}
	panic(fmt.Sprintf("mygoscript: %T in state", v))
}

// luaString quotes s as a Luau string literal, escaping every byte that is
// not printable ASCII.
func luaString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x20 || c >= 0x7f || c == '"' || c == '\\' {
			fmt.Fprintf(&b, "\\%03d", c)
		} else {
			b.WriteByte(c)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// Package mygoscript lets a Luau script describe native UI that the Go host
// draws with MyGo's ui package. The script returns a widget tree from its
// view function; the host owns the state the tree is built from, runs the
// script's actions when the user clicks, and rebuilds. See DESIGN.md.
package mygoscript

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"sync"

	"glitched/mygoscript/internal/luau"
)

//go:embed prelude.luau
var prelude string

// Plugin is one script and the state it is drawn from. The state belongs
// to the host: it survives errors and reloads of the script's VM.
type Plugin struct {
	name, source string

	mu      sync.Mutex
	started bool
	vm      *luau.VM
	state   map[string]any
	tree    *node
	err     error
}

// New returns a plugin that runs source. name labels it in error messages.
// Nothing runs until a View shows it.
func New(name, source string) *Plugin {
	return &Plugin{name: name, source: source, state: map[string]any{}}
}

// Close stops the plugin's VM. A View that shows it again starts a new one.
func (p *Plugin) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stop()
	p.started = false
}

// Value is the state value at key, as the script's actions left it.
func (p *Plugin) Value(key string) any {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.state[key]
}

// start boots a VM and mounts the script, on the UI thread: both take well
// under a millisecond. The caller holds mu.
func (p *Plugin) start() {
	p.started = true
	vm, err := luau.New(prelude)
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

// act runs the script's action name and merges what it returns into the
// state, then renders the new state.
func (p *Plugin) act(name string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.vm == nil {
		return
	}
	out, err := p.vm.Call("act", p.name+" "+name, "return "+luaString(name)+", "+luaTable(p.state))
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

// luaTable writes state as a Luau table constructor. The prelude only lets
// strings, finite numbers and booleans into the state.
func luaTable(state map[string]any) string {
	keys := make([]string, 0, len(state))
	for k := range state {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	var b strings.Builder
	b.WriteString("{")
	for _, k := range keys {
		b.WriteString("[" + luaString(k) + "]=")
		switch v := state[k].(type) {
		case string:
			b.WriteString(luaString(v))
		case float64:
			if math.IsInf(v, 0) || math.IsNaN(v) {
				panic("mygoscript: non-finite number in state")
			}
			b.WriteString(strconv.FormatFloat(v, 'g', -1, 64))
		case bool:
			b.WriteString(strconv.FormatBool(v))
		default:
			panic(fmt.Sprintf("mygoscript: %T in state", v))
		}
		b.WriteString(",")
	}
	b.WriteString("}")
	return b.String()
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

package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	plugin "github.com/yxxbc/mygo-plugin"
)

// The bounds of a service.
const (
	// ServiceMemory is what one service's runtime may keep.
	ServiceMemory = 64 << 20
	// ServiceIdle stops a service with nothing to do; its next call,
	// tool or timer starts it again.
	ServiceIdle = 5 * time.Minute
	// ToolTimeout bounds one tool call.
	ToolTimeout = 60 * time.Second
)

// Services is the headless half of the plugins, which the engine runs
// with no window: each plugin's service module (glitched.service), or, for
// a plugin with tools and no service, its main module. It answers agents'
// tool calls, the windows' service calls and the services' timers. Its
// calls run on a goroutine of their own, a serial queue, as the window's
// UI thread runs the GUI's.
type Services struct {
	h     *Host
	queue chan func()
	stop  chan struct{}
	once  sync.Once

	mu     sync.Mutex
	active map[string]time.Time // when each service last worked
}

// ServiceOptions are what the engine gives the services.
type ServiceOptions struct {
	Logf func(format string, args ...any)
	// Do runs a service's effect, as the GUI's Options.Do does a page's.
	Do  func(plugin string, e Effect) (any, error)
	Run func(ctx context.Context, id string, allowed []string, args json.RawMessage) (any, error)
	// Idle and TimerScale shorten the idle stop and timers, in tests.
	Idle       time.Duration
	TimerScale float64
}

// StartServices runs the services of the plugins installed under
// stateDir, and keeps up with what the user changes there: a plugin
// installed or saved, turned on or off, a grant given or taken back.
func StartServices(stateDir string, o ServiceOptions) *Services {
	s := &Services{queue: make(chan func(), 256), stop: make(chan struct{}), active: map[string]time.Time{}}
	go s.loop()
	idle := o.Idle
	if idle <= 0 {
		idle = ServiceIdle
	}
	s.do(func() {
		s.h = Open(stateDir, Options{
			Logf:        o.Logf,
			Post:        s.post,
			Do:          o.Do,
			Run:         o.Run,
			MemoryLimit: ServiceMemory,
			TimerScale:  o.TimerScale,
			Main: func(m plugin.Manifest, x Extension) string {
				switch {
				case x.Service != "" && slices.Contains(m.Capabilities, "service"):
					return x.Service
				case len(x.Tools) > 0:
					return m.Main
				}
				return ""
			},
		})
	})
	// What the plugins are now, before StartServices returns, so that a
	// change made once it has is seen.
	last := stamp(stateDir)
	s.do(s.startTimers)
	go s.watch(stateDir, idle, last)
	return s
}

// stamp changes as plugins.json or the plugins folder does.
func stamp(stateDir string) string {
	fi, err := os.Stat(prefsPath(stateDir))
	if err != nil {
		return fingerprint(Dir(stateDir))
	}
	return fmt.Sprint(fi.ModTime().UnixNano(), fi.Size(), fingerprint(Dir(stateDir)))
}

func (s *Services) loop() {
	for {
		select {
		case fn := <-s.queue:
			fn()
		case <-s.stop:
			return
		}
	}
}

func (s *Services) post(fn func()) {
	select {
	case s.queue <- fn:
	case <-s.stop:
	}
}

// do runs fn on the services' queue and waits for it.
func (s *Services) do(fn func()) {
	done := make(chan struct{})
	s.post(func() { fn(); close(done) })
	select {
	case <-done:
	case <-s.stop:
	}
}

// watch reloads the services as plugins.json or the plugins folder
// change, starts each service's timers, and stops services left idle.
func (s *Services) watch(stateDir string, idle time.Duration, last string) {
	tick := time.NewTicker(min(time.Second, idle/2))
	defer tick.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-tick.C:
		}
		if now := stamp(stateDir); now != last {
			last = now
			s.do(func() {
				var p prefs
				readPrefs(stateDir, &p)
				s.h.mu.Lock()
				s.h.prefs = p
				s.h.mu.Unlock()
				s.h.Reload()
				s.startTimers()
			})
		}
		s.do(func() { s.stopIdle(idle) })
	}
}

// startTimers wakes each running service on its timers.
func (s *Services) startTimers() {
	for _, in := range s.h.running() {
		id := in.manifest.ID
		p := in.p
		if !p.Can("timer") {
			continue
		}
		for _, t := range in.ext.Timers {
			every := time.Duration(t.Every) * time.Second
			if s.h.opts.TimerScale > 0 {
				every = time.Duration(float64(every) * s.h.opts.TimerScale)
			}
			go func() {
				tick := time.NewTicker(every)
				defer tick.Stop()
				for {
					select {
					case <-s.stop:
						return
					case <-tick.C:
					}
					if s.h.running1(id) == nil || s.h.running1(id).p != p {
						return // reloaded: the new one has timers of its own
					}
					s.touch(id)
					var out json.RawMessage
					if err := p.Call("timer", map[string]string{"id": t.ID}, &out); err != nil {
						s.h.opts.Logf("plugins: %s: timer %s: %v", id, t.ID, err)
						continue
					}
					s.effects(id, p, out)
				}
			}()
		}
	}
}

func (s *Services) touch(id string) {
	s.mu.Lock()
	s.active[id] = time.Now()
	s.mu.Unlock()
}

// stopIdle stops the runtimes of services with nothing to do for idle;
// mygo-plugin starts one again on its next call.
func (s *Services) stopIdle(idle time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, at := range s.active {
		if time.Since(at) < idle {
			continue
		}
		if in := s.h.running1(id); in != nil {
			go in.p.Close()
		}
		delete(s.active, id)
	}
}

// effects runs what a service's answer asks for, as a command's: their
// answers go nowhere.
func (s *Services) effects(id string, p *plugin.Plugin, out json.RawMessage) {
	var a struct {
		Effects []Effect `json:"effects"`
	}
	if json.Unmarshal(out, &a) != nil || len(a.Effects) > MaxEffects {
		return
	}
	for _, e := range a.Effects {
		s.h.effect(id, p, e, nil, func(_ any, err error) {
			if err != nil {
				s.h.opts.Logf("plugins: %s: %s: %v", id, e.Type, err)
			}
		})
	}
}

// Close stops every service.
func (s *Services) Close() {
	s.do(func() { s.h.Close() })
	s.once.Do(func() { close(s.stop) })
}

// ToolInfo is a tool an agent may call: the plugin's, and its manifest's
// entry.
type ToolInfo struct {
	Plugin      string          `json:"plugin"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Input       json.RawMessage `json:"input,omitempty"`
}

// Tools are the running plugins' tools.
func (s *Services) Tools(context.Context) ([]ToolInfo, error) {
	var out []ToolInfo
	s.do(func() {
		for _, in := range s.h.running() {
			if !s.h.Allowed(in.manifest.ID, "tools") {
				continue
			}
			for _, t := range in.ext.Tools {
				out = append(out, ToolInfo{in.manifest.ID, t.Name, t.Description, t.Input})
			}
		}
	})
	return out, nil
}

// ToolCall is an agent's call of a plugin's tool: Thread is the thread the
// agent runs in.
type ToolCall struct {
	Plugin string          `json:"plugin"`
	Name   string          `json:"name"`
	Args   json.RawMessage `json:"args"`
	Thread string          `json:"thread"`
}

// CallTool answers an agent's tool call with the plugin's tool export.
func (s *Services) CallTool(ctx context.Context, c ToolCall) (json.RawMessage, error) {
	var p *plugin.Plugin
	s.do(func() {
		in := s.h.running1(c.Plugin)
		if in == nil || !s.h.Allowed(c.Plugin, "tools") || !slices.ContainsFunc(in.ext.Tools, func(t Tool) bool { return t.Name == c.Name }) {
			return
		}
		p = in.p
	})
	if p == nil {
		return nil, fmt.Errorf("no plugin %q offers the tool %q", c.Plugin, c.Name)
	}
	if !p.Can("tool") {
		return nil, fmt.Errorf("plugin %s lists tools but has no tool export", c.Plugin)
	}
	s.touch(c.Plugin)
	args := c.Args
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	var out json.RawMessage
	err := p.CallWithin(ToolTimeout, "tool", map[string]any{"name": c.Name, "args": args, "call": map[string]string{"thread": c.Thread}}, &out)
	return out, err
}

// ServiceCall is a window's call of a method of its plugin's service.
type ServiceCall struct {
	Plugin string          `json:"plugin"`
	Method string          `json:"method"`
	Args   json.RawMessage `json:"args"`
}

// ErrNoService is a call of a plugin whose service doesn't run here.
var ErrNoService = errors.New("its service doesn't run: it has none, it's off, or service isn't granted")

// Call answers a window's call with the service's serve export.
func (s *Services) Call(ctx context.Context, c ServiceCall) (json.RawMessage, error) {
	var p *plugin.Plugin
	s.do(func() {
		in := s.h.running1(c.Plugin)
		if in == nil || in.ext.Service == "" || !s.h.Allowed(c.Plugin, "service") {
			return
		}
		p = in.p
	})
	if p == nil {
		return nil, ErrNoService
	}
	if !p.Can("serve") {
		return nil, fmt.Errorf("plugin %s's service has no serve export", c.Plugin)
	}
	s.touch(c.Plugin)
	timeout := 30 * time.Second
	if d, ok := ctx.Deadline(); ok {
		timeout = time.Until(d)
	}
	var out json.RawMessage
	err := p.CallWithin(timeout, "serve", map[string]any{"method": c.Method, "args": c.Args}, &out)
	return out, err
}

func readPrefs(stateDir string, p *prefs) {
	b, err := os.ReadFile(filepath.Join(stateDir, "plugins.json"))
	if err == nil {
		json.Unmarshal(b, p)
	}
}

package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"reflect"
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
}

// ServiceOptions are what the engine gives the services.
type ServiceOptions struct {
	Logf func(format string, args ...any)
	// Do runs a service's effect, as the GUI's Options.Do does a page's.
	Do  func(plugin string, e Effect) (any, error)
	Run func(ctx context.Context, id string, allowed, denied []string, args json.RawMessage) (any, error)
	// Idle, CallTimeout and TimerScale shorten the idle stop, call timeout and timers, in tests.
	Idle        time.Duration
	CallTimeout time.Duration
	TimerScale  float64
	// Bundled and ReleaseKeys are the built-in plugins, as Options has
	// them: those with a service run it here.
	Bundled     fs.FS
	ReleaseKeys []string
}

// StartServices runs the services of the plugins installed under
// stateDir, and keeps up with what the user changes there: a plugin
// installed or saved, turned on or off, a grant given or taken back.
func StartServices(stateDir string, o ServiceOptions) *Services {
	s := &Services{queue: make(chan func(), 256), stop: make(chan struct{})}
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
			CallTimeout: o.CallTimeout,
			IdleTimeout: idle,
			TimerScale:  o.TimerScale,
			Bundled:     o.Bundled,
			ReleaseKeys: o.ReleaseKeys,
			BundledDir:  filepath.Join(stateDir, "plugin-bundled-services"),
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
	choices, folder := choicesStamp(stateDir), fingerprint(Dir(stateDir))
	s.do(s.startTimers)
	go s.watch(stateDir, choices, folder)
	return s
}

// choicesStamp changes as plugins.json does.
func choicesStamp(stateDir string) string {
	fi, err := os.Stat(prefsPath(stateDir))
	if err != nil {
		return ""
	}
	return fmt.Sprint(fi.ModTime().UnixNano(), fi.Size())
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

// watch follows the window's changes: the plugins folder, which reloads
// the services, and plugins.json. A grant is read as it's used, so a
// change of grants needs no reload, but for service, which decides which
// module runs; nor do settings. Turning a plugin on or off, or a consent,
// reloads.
//
// It polls: the window that writes plugins.json may be another process,
// and polling two stamps a second costs less than a file watcher's
// dependency.
func (s *Services) watch(stateDir, choices, folder string) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-tick.C:
		}
		c, f := choicesStamp(stateDir), fingerprint(Dir(stateDir))
		if c == choices && f == folder {
			continue
		}
		reload := f != folder
		choices, folder = c, f
		s.do(func() {
			var p prefs
			readPrefs(stateDir, &p)
			s.h.mu.Lock()
			reload = reload || loadingChanged(s.h.prefs, p)
			s.h.prefs = p
			s.h.mu.Unlock()
			if reload {
				s.h.Reload()
				s.startTimers()
			}
		})
	}
}

// loadingChanged reports whether plugins.json changed what runs: a plugin
// turned on or off, a consent, or service granted or, for a built-in,
// revoked.
func loadingChanged(a, b prefs) bool {
	service := func(caps map[string][]string) map[string]bool {
		out := map[string]bool{}
		for id, g := range caps {
			if slices.Contains(g, "service") {
				out[id] = true // only these: none and others alike say no
			}
		}
		return out
	}
	return !slices.Equal(a.Disabled, b.Disabled) || !reflect.DeepEqual(a.Consent, b.Consent) ||
		!maps.Equal(service(a.Grants), service(b.Grants)) || !maps.Equal(service(a.Revoked), service(b.Revoked))
}

// MaxTimerCall is the longest a service's timer call may take. A timer runs
// one call at a time: a tick that comes while the last call still runs,
// past its period too, waits for the next, so a timer that waits on slow
// effects never overlaps itself and the host hears how each call went. A
// call past MaxTimerCall stops its runtime, so that nothing it left running
// goes on beside the next.
const MaxTimerCall = 5 * time.Minute

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
				var last chan struct{} // closed as the last call ends
				for {
					select {
					case <-s.stop:
						return
					case <-tick.C:
					}
					if s.h.running1(id) == nil || s.h.running1(id).p != p {
						return // reloaded: the new one has timers of its own
					}
					if last != nil {
						select {
						case <-last:
						default:
							s.h.opts.Logf("plugins: %s: timer %s: its last call still runs, so this tick waits for the next", id, t.ID)
							continue
						}
					}
					done := make(chan struct{})
					last = done
					go func() {
						defer close(done)
						begun := time.Now()
						if err := p.CallWithin(MaxTimerCall, "timer", map[string]string{"id": t.ID}, nil); err != nil {
							s.h.opts.Logf("plugins: %s: timer %s: %v", id, t.ID, err)
						}
						if time.Since(begun) >= MaxTimerCall {
							p.Close()
						}
					}()
				}
			}()
		}
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

// Call answers a window's call with the service's serve export, by ctx's
// deadline or in 30 s. It answers once ctx ends, whatever the call is
// waiting on: the services' queue, the runtime starting, or serve. A call
// whose ctx ended before it was handed to the runtime never runs serve;
// one handed over may still run, and do what it asked, after Call
// answers, as any call that timed out may.
func (s *Services) Call(ctx context.Context, c ServiceCall) (json.RawMessage, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
	}
	deadline, _ := ctx.Deadline()
	found := make(chan *plugin.Plugin, 1)
	lookup := func() {
		in := s.h.running1(c.Plugin)
		if in == nil || in.ext.Service == "" || !s.h.Allowed(c.Plugin, "service") {
			found <- nil
			return
		}
		found <- in.p
	}
	var p *plugin.Plugin
	select {
	case s.queue <- lookup:
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.stop:
		return nil, ErrNoService
	}
	select {
	case p = <-found:
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.stop:
	}
	if p == nil {
		return nil, ErrNoService
	}
	if !p.Can("serve") {
		return nil, fmt.Errorf("plugin %s's service has no serve export", c.Plugin)
	}
	type answer struct {
		out json.RawMessage
		err error
	}
	answered := make(chan answer, 1)
	go func() {
		var a answer
		a.err = p.CallWithin(time.Until(deadline), "serve", serveArgs{ctx, c}, &a.out)
		switch {
		case a.err == nil:
		case ctx.Err() != nil:
			a.err = ctx.Err()
		case !time.Now().Before(deadline):
			a.err = context.DeadlineExceeded
		}
		answered <- a
	}()
	select {
	case a := <-answered:
		return a.out, a.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// serveArgs are serve's argument. CallWithin encodes it once the runtime
// runs, right before it gives the runtime the call, so a call whose ctx has
// ended by then fails there, and serve never runs.
type serveArgs struct {
	ctx  context.Context
	call ServiceCall
}

func (a serveArgs) MarshalJSON() ([]byte, error) {
	if err := a.ctx.Err(); err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"method": a.call.Method, "args": a.call.Args})
}

func readPrefs(stateDir string, p *prefs) {
	b, err := os.ReadFile(filepath.Join(stateDir, "plugins.json"))
	if err == nil {
		json.Unmarshal(b, p)
	}
}

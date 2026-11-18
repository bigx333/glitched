package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"time"

	plugin "github.com/yxxbc/mygo-plugin"

	"glitched/internal/plugins/guard"
)

// LoadTimeout bounds a module's top level as its runtime starts.
const LoadTimeout = 5 * time.Second

// Prelude defines the glitched global every plugin runtime has: awaitable
// calls into the host, over mygo-plugin's host.call.
const Prelude = `const app = (type, args = {}) => host.call("app", { ...args, type });
globalThis.glitched = Object.freeze({
  toast: (title, description) => app("toast", { title, description }),
  openURL: (url) => app("url.open", { url }),
  pages: Object.freeze({ open: (page) => app("page.open", { page }) }),
  tabs: Object.freeze({ open: (tab) => app("tab.open", { tab }) }),
  panels: Object.freeze({
    toggle: (panel, open) => app("panels.toggle", { panel, open: open ?? null }),
    state: () => app("panels.state"),
  }),
  openIn: (app_, thread) => app("open.in", { app: app_, thread }),
  app: Object.freeze({ checkForUpdates: () => app("app.checkForUpdates") }),
  diff: Object.freeze({
    set: (thread, options = {}) => app("diff.set", { ...options, thread }),
    setReviewed: (thread, path, reviewed = true) => app("diff.setReviewed", { thread, path, reviewed }),
    openFile: (thread, path) => app("diff.openFile", { thread, path }),
  }),
  clipboard: Object.freeze({ write: (text) => app("clipboard.write", { text }) }),
  notify: (title, body) => app("notify", { title, body }),
  composer: Object.freeze({ draft: (text, workspace) => app("composer.draft", { text, workspace }) }),
  threads: Object.freeze({
    list: () => app("threads.list"),
    open: (thread) => app("threads.open", { thread }),
    create: (prompt, options = {}) => app("threads.create", { ...options, prompt }),
    send: (thread, text) => app("threads.send", { thread, text }),
  }),
  settings: Object.freeze({
    get: () => host.call("settings"),
    status: (setting, status) => app("setting.status", { setting, status: status ?? null }),
  }),
  run: (program, args = [], options = {}) => host.call("run", { ...options, program, args }),
  db: Object.freeze({
    exec: (sql, params) => host.call("db.exec", { sql, params: params ?? [] }),
    query: (sql, params) => host.call("db.query", { sql, params: params ?? [] }),
  }),
  service: Object.freeze({
    call: (method, args, options = {}) => host.call("service.call", { ...options, method, args: args ?? null }),
  }),
});`

// coded is an error whose code a plugin reads as err.code.
type coded struct {
	code string
	err  error
}

func (e coded) Error() string { return e.err.Error() }
func (e coded) Code() string  { return e.code }
func (e coded) Unwrap() error { return e.err }

func denied(format string, args ...any) error {
	return coded{"denied", fmt.Errorf("%w: "+format, append([]any{ErrDenied}, args...)...)}
}

// functions are the host functions of the plugin in, which its glitched
// global calls. Each checks the plugin's grants as it runs, so a grant
// taken back stops the next call.
//
// The database is in's own, opened on first use; the host closes it with
// in, so a reload that fails leaves the running one's open.
func (h *Host) functions(in *installed) map[string]plugin.Function {
	id := in.manifest.ID
	db := &pluginDB{path: filepath.Join(DataDir(h.state), id, "plugin.db")}
	in.db = db
	// alive is this load's: the host clears it as it replaces or closes
	// the runtime, on the UI thread, before a call of the old one can run.
	alive := new(atomic.Bool)
	alive.Store(true)
	in.alive = alive
	awaiting := new(atomic.Int32)
	in.awaiting = awaiting
	fns := map[string]plugin.Function{
		"db.exec":  db.exec,
		"db.query": db.query,
		// app asks the app to do what only it can, checked as it runs on
		// the UI thread, and answers what it did.
		"app": func(ctx context.Context, args json.RawMessage) (any, error) {
			var e Effect
			if err := json.Unmarshal(args, &e); err != nil || e.Type == "" {
				return nil, errors.New("an app call names its type")
			}
			type answer struct {
				v   any
				err error
			}
			ch := make(chan answer, 1)
			awaiting.Add(1)
			defer awaiting.Add(-1)
			h.effect(ctx, alive, id, e, func(v any, err error) { ch <- answer{v, err} })
			select {
			case a := <-ch:
				if errors.Is(a.err, ErrDenied) {
					return nil, coded{"denied", a.err}
				}
				return a.v, a.err
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		},
		"settings": func(context.Context, json.RawMessage) (any, error) {
			return h.SettingValues(id), nil
		},
		"run": func(ctx context.Context, args json.RawMessage) (any, error) {
			if !h.Allowed(id, "programs") {
				return nil, denied("running programs needs the programs capability, granted in Settings › Plugins")
			}
			run := h.opts.Run
			if run == nil {
				run = RunProgram
			}
			return run(ctx, id, in.ext.Programs, args)
		},
		"service.call": func(ctx context.Context, args json.RawMessage) (any, error) {
			if in.ext.Service == "" {
				return nil, coded{"no_service", errors.New("the plugin has no service")}
			}
			if !h.Allowed(id, "service") {
				return nil, denied("its service needs the service capability, granted in Settings › Plugins")
			}
			if h.opts.ServiceCall == nil {
				return nil, coded{"offline", errors.New("no engine runs services here")}
			}
			var a struct {
				Method  string          `json:"method"`
				Args    json.RawMessage `json:"args"`
				Timeout float64         `json:"timeout"`
			}
			if err := json.Unmarshal(args, &a); err != nil || a.Method == "" {
				return nil, errors.New("service.call takes a method")
			}
			timeout := 30 * time.Second
			if a.Timeout > 0 {
				timeout = min(time.Duration(a.Timeout*float64(time.Second)), 300*time.Second)
			}
			ctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			return h.opts.ServiceCall(ctx, id, a.Method, a.Args)
		},
	}
	return fns
}

// RunProgram is glitched.run: a program the plugin's manifest names, run
// as the user under the guard's rules, its output as text.
func RunProgram(ctx context.Context, _ string, allowed []string, raw json.RawMessage) (any, error) {
	var a struct {
		Program string            `json:"program"`
		Args    []string          `json:"args"`
		Cwd     string            `json:"cwd"`
		Stdin   string            `json:"stdin"`
		Timeout float64           `json:"timeout"`
		Env     map[string]string `json:"env"`
	}
	if err := json.Unmarshal(raw, &a); err != nil || a.Program == "" {
		return nil, errors.New("glitched.run takes a program and its args")
	}
	if a.Cwd == "" {
		a.Cwd, _ = os.UserHomeDir()
	}
	r, err := guard.Run(ctx, guard.Spec{
		Program: a.Program, Args: a.Args, Cwd: a.Cwd, Stdin: []byte(a.Stdin), Env: a.Env,
		Timeout: time.Duration(a.Timeout * float64(time.Second)), Allowed: allowed,
	})
	if errors.Is(err, guard.ErrRefused) {
		return nil, coded{"refused", err}
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{"status": r.Status, "stdout": string(r.Stdout), "stderr": string(r.Stderr)}, nil
}

// httpClient is what the plugin's fetch sends with: under the guard's
// rules, so a host it names that resolves to a private or metadata address
// is never reached unless named exactly.
func httpClient(hosts []string) (*http.Client, error) {
	if slices.Contains(hosts, "*") {
		hosts = nil // any public address
	}
	p, err := guard.NewNetPolicy(hosts)
	if err != nil {
		return nil, err
	}
	return guard.HTTPClient(p, 10*time.Minute), nil
}

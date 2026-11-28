package plugins

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	plugin "github.com/yxxbc/mygo-plugin"

	"glitched/internal/plugins/guard"
	"glitched/internal/provider"
	"glitched/internal/secrets"
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
  app: Object.freeze({
    checkForUpdates: () => app("app.checkForUpdates"),
    settings: Object.freeze({
      get: () => app("app.settings.get"),
      set: (key, value) => app("app.settings.set", { key, value }),
    }),
  }),
  diff: Object.freeze({
    set: (thread, options = {}) => app("diff.set", { ...options, thread }),
    setReviewed: (thread, path, reviewed = true) => app("diff.setReviewed", { thread, path, reviewed }),
    openFile: (thread, path) => app("diff.openFile", { thread, path }),
  }),
  clipboard: Object.freeze({ write: (text) => app("clipboard.write", { text }) }),
  notify: (title, body, options = {}) => app("notify", { ...options, title, body }),
  composer: Object.freeze({
    draft: (text, where) => app("composer.draft", typeof where === "object" && where !== null ? { ...where, text } : { text, workspace: where }),
    submit: (thread) => app("composer.submit", { thread: thread ?? "" }),
    quote: (thread, ask) => app("composer.quote", { thread: thread ?? "", ask: ask ?? "" }),
  }),
  threads: Object.freeze({
    list: () => app("threads.list"),
    open: (thread, options = {}) => app("threads.open", { ...options, thread }),
    search: (query, options = {}) => app("threads.search", { ...options, query }),
    create: (prompt, options = {}) => app("threads.create", { ...options, prompt }),
    send: (thread, text) => app("threads.send", { thread, text }),
    rename: (thread, title) => app("threads.rename", { thread, title }),
    delete: (thread) => app("threads.delete", { thread }),
    setDisposition: (thread, disposition, options = {}) => app("threads.setDisposition", { ...options, ...disposition, thread }),
    markRead: (thread) => app("threads.markRead", { thread }),
    interrupt: (thread) => app("threads.interrupt", { thread }),
    configure: (thread, options = {}) => app("threads.configure", { ...options, thread }),
    sweepNow: (thread) => app("threads.sweepNow", { thread }),
    setAccess: (thread, access) => app("threads.setAccess", { thread: thread ?? "", access }),
  }),
  skills: Object.freeze({ list: (thread) => app("skills.list", { thread }) }),
  models: Object.freeze({
    list: () => app("models.list"),
    ask: (prompt, options = {}) => host.call("models.ask", { ...options, prompt }),
  }),
  commands: Object.freeze({
    list: () => app("commands.list"),
    run: (id, palette) => app("commands.run", { id, palette: palette ?? "" }),
  }),
  palette: Object.freeze({ close: (palette) => app("palette.close", { palette: palette ?? "" }) }),
  sidebar: Object.freeze({ more: (key) => app("sidebar.more", { key }) }),
  browser: Object.freeze({
    blur: (tab) => app("browser.blur", { tab }),
    draft: (tab, text) => app("browser.draft", { tab, text }),
    navigate: (tab, url) => app("browser.navigate", { tab, url }),
    back: (tab) => app("browser.back", { tab }),
    forward: (tab) => app("browser.forward", { tab }),
    reload: (tab) => app("browser.reload", { tab }),
    stop: (tab) => app("browser.stop", { tab }),
    newTab: (url) => app("browser.newTab", { url: url ?? "" }),
    closeTab: (tab) => app("browser.closeTab", { tab }),
    activateTab: (tab) => app("browser.activateTab", { tab }),
  }),
  cliUpdates: Object.freeze({
    update: (machine, provider, whenIdle) => app("cliupdates.update", { machine, provider, whenIdle: whenIdle ?? false }),
    updateAll: () => app("cliupdates.updateAll"),
    check: () => app("cliupdates.check"),
  }),
  projects: Object.freeze({
    remove: (workspace, machine) => app("projects.remove", { workspace, machine: machine ?? "" }),
    list: () => app("projects.list"),
    filter: (keys) => app("projects.filter", { keys: keys ?? [] }),
    add: () => app("projects.add"),
    clone: () => app("projects.clone"),
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
				run = func(ctx context.Context, _ string, allowed, denied []string, args json.RawMessage) (any, error) {
					return RunProgram(ctx, allowed, denied, args)
				}
			}
			return run(ctx, id, in.ext.Programs, []string{secrets.Under(h.state)}, args)
		},
		"models.ask": func(ctx context.Context, args json.RawMessage) (any, error) {
			return h.ask(ctx, id, args)
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

// The bounds of an ask.
const (
	MaxAsk     = 200 << 10 // the prompt and the system prompt together, in bytes
	AskTimeout = 120 * time.Second
)

// AskRequest is what glitched.models.ask asks: the prompt, a system prompt,
// the provider and model to run it on, "" for the defaults, and the
// seconds it may take, 0 for AskTimeout.
type AskRequest struct {
	Prompt   string  `json:"prompt"`
	System   string  `json:"system,omitempty"`
	Provider string  `json:"provider,omitempty"`
	Model    string  `json:"model,omitempty"`
	Timeout  float64 `json:"timeout,omitempty"`
}

// AskAnswer is the text, and the provider and model that wrote it, as
// models.list names them.
type AskAnswer struct {
	Text     string `json:"text"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

// ask is glitched.models.ask: one at a time per plugin, as each spends
// the user's account.
func (h *Host) ask(ctx context.Context, id string, args json.RawMessage) (any, error) {
	if !h.Allowed(id, "models:ask") {
		return nil, denied("asking a model needs the models:ask capability, granted in Settings › Plugins")
	}
	var q AskRequest
	d := json.NewDecoder(bytes.NewReader(args))
	d.DisallowUnknownFields()
	if err := d.Decode(&q); err != nil || strings.TrimSpace(q.Prompt) == "" {
		return nil, errors.New("glitched.models.ask takes a prompt, and options of system, provider, model and timeout only")
	}
	if len(q.Prompt)+len(q.System) > MaxAsk {
		return nil, coded{"too_large", fmt.Errorf("an ask's prompt and system prompt are %d KB at most", MaxAsk>>10)}
	}
	if h.opts.Ask == nil {
		return nil, coded{"unavailable", errors.New("no model answers here")}
	}
	if _, busy := h.asking.LoadOrStore(id, true); busy {
		return nil, coded{"busy", errors.New("the plugin's last ask isn't answered yet")}
	}
	timeout := AskTimeout
	if q.Timeout > 0 {
		timeout = min(time.Duration(q.Timeout*float64(time.Second)), AskTimeout)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	type answer struct {
		a   AskAnswer
		err error
	}
	done := make(chan answer, 1)
	// The plugin hears of its deadline at once, but its next ask waits for
	// this one to end where it runs, which may be a while after. It ends
	// before the plugin hears its answer, so an ask right after isn't busy.
	go func() {
		a, err := h.opts.Ask(ctx, id, q)
		h.asking.Delete(id)
		done <- answer{a, err}
	}()
	var got answer
	select {
	case got = <-done:
	case <-ctx.Done():
		got.err = ctx.Err()
	}
	a, err := got.a, got.err
	switch {
	case err == nil:
		return a, nil
	case errors.Is(err, provider.ErrUnsupported):
		return nil, coded{"unsupported", err}
	case errors.Is(err, provider.ErrUnavailable):
		return nil, coded{"unavailable", err}
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return nil, coded{"timeout", fmt.Errorf("the model didn't answer within %s", timeout.Round(100*time.Millisecond))}
	}
	msg := remoteError.ReplaceAllString(err.Error(), "")
	if a.Provider != "" {
		msg = strings.TrimPrefix(msg, a.Provider+": ")
	}
	return nil, errors.New(msg)
}

// remoteError is how an error that crossed the engine's socket starts: an
// ask's failure reaches the plugin in the provider's own words, without
// that, and without the provider's name for itself.
var remoteError = regexp.MustCompile(`^transport: remote [a-z_]+: `)

// RunProgram is glitched.run: a program the plugin's manifest names, run
// as the user under the guard's rules, kept out of the denied folders,
// its output as text.
func RunProgram(ctx context.Context, allowed, denied []string, raw json.RawMessage) (any, error) {
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
		Timeout: time.Duration(a.Timeout * float64(time.Second)), Allowed: allowed, Denied: denied,
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

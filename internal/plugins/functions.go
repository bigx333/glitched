package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	plugin "github.com/yxxbc/mygo-plugin"
)

// LoadTimeout bounds a module's top level as its runtime starts.
const LoadTimeout = 5 * time.Second

// Prelude defines the glitched global every plugin runtime has: awaitable
// calls into the host, over mygo-plugin's host.call.
const Prelude = `globalThis.glitched = Object.freeze({
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
func (h *Host) functions(in *installed) map[string]plugin.Function {
	id := in.manifest.ID
	db := &pluginDB{path: filepath.Join(DataDir(h.state), id, "plugin.db")}
	h.mu.Lock()
	if old := h.dbs[id]; old != nil {
		old.close()
	}
	h.dbs[id] = db
	h.mu.Unlock()
	fns := map[string]plugin.Function{
		"db.exec":  db.exec,
		"db.query": db.query,
		"run": func(ctx context.Context, args json.RawMessage) (any, error) {
			if !h.Allowed(id, "programs") {
				return nil, denied("running programs needs the programs capability, granted in Settings › Plugins")
			}
			if h.opts.Run == nil {
				return nil, errors.New("this host runs no programs")
			}
			return h.opts.Run(ctx, id, in.ext.Programs, args)
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

package transport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"glitched/internal/api"
	"glitched/internal/cliupdate"
	"glitched/internal/hub"
	"glitched/internal/provider"
)

// Server serves an Endpoint and a hub replica on /ws. Either may be nil, and
// its methods are then not served.
type Server struct {
	Transport Transport
	Endpoint  api.Endpoint
	Hub       *hub.Replica
	// Plugins serves the plugins' services and tools, to this machine's
	// own callers only: the unix socket's server has it, a tailnet's never.
	Plugins Plugins
	// Providers lists the providers' slash commands, to this machine's own
	// callers only, as Plugins: a listing runs the provider's CLI in the
	// folder the caller names.
	Providers Providers
	// Launches reads and sets how each provider's program starts on this
	// machine, to any client the server admits: what a paired client may
	// already run there, a thread at Full access in any folder, is no
	// less than a program it names.
	Launches Launches
	// CLIs says what the providers are on this machine and updates their
	// CLIs, from any client the server admits: a window on another machine
	// shows this one's.
	CLIs CLIs
	// Asks opens credentials requests for this machine's agents, to its
	// own callers only, as Plugins: glitched mcp asks for its agent.
	Asks Asks
	// Secrets takes the credentials users type for a request, from any
	// client the server admits.
	Secrets Secrets
	// Projects lists the folders on the engine's machine and clones
	// repositories onto it, for any client the server admits.
	Projects Projects
	// Authorize admits a socket whose client proved, in the handshake, that
	// it holds one of the keys Authorize knows. proved reports whether the
	// proof was made with a key; Authorize asks it of every key it knows,
	// whatever the answers, so the time taken doesn't say which matched. It
	// returns that key, for the server's own proof, and a ctx whose end
	// closes the socket, as when its grant is revoked. A Tailnet server must
	// have one.
	Authorize func(r *http.Request, proved func(Key) bool) (Key, context.Context, error)

	keepalive keepalive     // zero means defaultKeepalive
	handshake time.Duration // how long a client has to prove its key; zero means handshakeWithin
	idle      time.Duration // each request's read, its response's write and each keep-alive wait; zero means defaultIdle
	limit     int64         // most bytes one message may hold; zero means maxMessage
}

type connKey struct{}

// Serve answers sockets on ln until ctx ends, then closes them and returns
// once their calls have.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	if _, ok := s.Transport.(credentialed); ok && s.Authorize == nil {
		ln.Close()
		return fmt.Errorf("transport: a %T server needs Authorize to check its clients' keys", s.Transport)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var (
		mu      sync.Mutex
		closed  bool
		sockets sync.WaitGroup
	)
	methods := s.methods()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ws", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		if closed {
			mu.Unlock()
			http.Error(w, "shutting down", http.StatusServiceUnavailable)
			return
		}
		sockets.Add(1)
		mu.Unlock()
		defer sockets.Done()
		s.serveSocket(ctx, w, r, methods)
	})
	idle := s.idle
	if idle == 0 {
		idle = defaultIdle
	}
	hs := &http.Server{
		Handler: mux,
		// Bound each request, body included, each response and each wait
		// between them, or a refused peer holds its connection: http drains
		// an unread body before it closes or reuses one, and a peer that
		// never reads blocks the response. Hijacking clears both deadlines,
		// so an accepted WebSocket isn't bound by them.
		ReadTimeout:  idle,
		WriteTimeout: idle,
		IdleTimeout:  idle,
		ConnContext: func(ctx context.Context, c net.Conn) context.Context {
			return context.WithValue(ctx, connKey{}, c)
		},
	}
	stop := context.AfterFunc(ctx, func() { hs.Close() })
	defer stop()
	err := hs.Serve(ln)
	cancel()
	mu.Lock()
	closed = true
	mu.Unlock()
	sockets.Wait()
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

func (s *Server) serveSocket(ctx context.Context, w http.ResponseWriter, r *http.Request, methods map[string]handler) {
	nc := r.Context().Value(connKey{}).(net.Conn)
	if _, err := s.Transport.WhoIs(nc); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	theirs := protocolOf(r.Header)
	w.Header().Set(protocolHeader, strconv.Itoa(api.Protocol))
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer ws.CloseNow()
	if theirs != api.Protocol {
		// A WebSocket close, not an HTTP status, whose reason an older
		// client shows; Close waits for the client's own close frame, as
		// the client reads the reason only after its first call is out.
		slog.Warn("refused a socket of another protocol", "protocol", theirs, "from", r.RemoteAddr)
		ws.Close(statusProtocol, fmt.Sprintf("this engine speaks protocol %d and the client %d; update the older one", api.Protocol, theirs))
		return
	}
	grant := context.Background()
	if s.Authorize != nil {
		ws.SetReadLimit(helloLimit)
		if grant, err = s.challenge(ctx, ws, r); err != nil {
			// Close sends the status, then waits up to 5s for the peer's
			// close frame. A read deadline already past ends that wait.
			nc.SetReadDeadline(time.Now())
			ws.Close(websocket.StatusPolicyViolation, "refused")
			return
		}
	}
	limit := s.limit
	if limit == 0 {
		limit = maxMessage
	}
	ws.SetReadLimit(limit)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer context.AfterFunc(grant, cancel)()
	k := s.keepalive
	if k == (keepalive{}) {
		k = defaultKeepalive
	}
	go k.run(ctx, ws)

	// A failed write closes ws, which ends the read loop below.
	send := func(f response) {
		// A socket going away, as its grant ended, answers nothing: what
		// its calls answer now is only that they were cut off.
		if ctx.Err() != nil {
			return
		}
		b, _ := json.Marshal(f)
		writeMessage(ctx, ws, b)
	}
	var (
		mu    sync.Mutex
		live  = map[uint64]*op{}
		calls sync.WaitGroup
	)
	defer calls.Wait()
	defer cancel()
	for {
		_, data, err := ws.Read(ctx)
		if err != nil {
			return
		}
		var req request
		if json.Unmarshal(data, &req) != nil {
			ws.Close(websocket.StatusInvalidFramePayloadData, "malformed message")
			return
		}
		mu.Lock()
		o := live[req.ID]
		switch {
		case req.Cancel:
			if o != nil {
				o.stop()
			}
			mu.Unlock()
			continue
		case req.Ack > 0:
			if o != nil {
				o.ack(req.Ack)
			}
			mu.Unlock()
			continue
		}
		h, known := methods[req.Method]
		if !known || o != nil {
			mu.Unlock()
			send(response{ID: req.ID, Error: &wireError{Code: "bad_request", Message: unknownMethod + " or id in use: " + req.Method}})
			continue
		}
		callCtx, stop := context.WithCancel(ctx)
		o = &op{stop: stop, acked: make(chan struct{}, 1), send: func(f response) {
			f.ID = req.ID
			send(f)
		}}
		live[req.ID] = o
		mu.Unlock()
		calls.Go(func() {
			defer func() {
				mu.Lock()
				delete(live, req.ID)
				mu.Unlock()
				stop()
			}()
			defer func() {
				if p := recover(); p != nil {
					slog.Error("transport: handler panicked", "method", req.Method, "panic", p, "stack", string(debug.Stack()))
					o.send(response{Error: &wireError{Code: "internal", Message: fmt.Sprintf("%s panicked: %v", req.Method, p)}})
				}
			}()
			h(callCtx, req.Params, o)
		})
	}
}

// An op is one call or stream in flight on a socket.
type op struct {
	stop    context.CancelFunc
	send    func(response)
	unacked atomic.Int64 // bytes of stream items sent and not acked
	acked   chan struct{}
}

func (o *op) ack(n int64) {
	o.unacked.Add(-n)
	select {
	case o.acked <- struct{}{}:
	default:
	}
}

// room waits until fewer than streamWindow bytes are unacked.
func (o *op) room(ctx context.Context) error {
	for o.unacked.Load() >= streamWindow {
		select {
		case <-o.acked:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// writeMessage writes b in frames of at most 32 KiB. The library writes a
// pong under the same lock as a frame and drops the socket when that takes
// 5s, so one frame for a large message on a slow link would kill it.
func writeMessage(ctx context.Context, ws *websocket.Conn, b []byte) error {
	w, err := ws.Writer(ctx, websocket.MessageText)
	if err != nil {
		return err
	}
	for len(b) > 0 {
		n := min(len(b), 32<<10)
		if _, err := w.Write(b[:n]); err != nil {
			return err
		}
		b = b[n:]
	}
	return w.Close()
}

// keepalive is how often a socket pings its peer and how late a pong may be
// before the socket is dropped.
type keepalive struct{ every, within time.Duration }

var defaultKeepalive = keepalive{every: 15 * time.Second, within: 10 * time.Second}

const defaultIdle = 10 * time.Second

func (k keepalive) run(ctx context.Context, ws *websocket.Conn) {
	t := time.NewTicker(k.every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		pctx, cancel := context.WithTimeout(ctx, k.within)
		err := ws.Ping(pctx)
		cancel()
		if err != nil {
			ws.CloseNow()
			return
		}
	}
}

// Plugins are the plugins' services and tools, as JSON both ways, so that
// the transport needn't know them.
type Plugins interface {
	PluginTools(ctx context.Context) (json.RawMessage, error)
	PluginTool(ctx context.Context, call json.RawMessage) (json.RawMessage, error)
	PluginCall(ctx context.Context, call json.RawMessage) (json.RawMessage, error)
}

// Asks are what glitched mcp asks of the engine for request_credentials.
type Asks interface {
	AskCredentials(ctx context.Context, a api.Ask) (api.Asked, error)
}

// Secrets keep the credentials users give agents. Their values travel in
// no other request and are never logged.
type Secrets interface {
	ProvideCredentials(ctx context.Context, p api.Provide) error
	ForgetCredentials(ctx context.Context, thread api.ID) error
}

// Projects are what a client adding a project on the engine's machine
// asks of it.
type Projects interface {
	Folders(ctx context.Context, q api.FolderQuery) (api.FolderList, error)
	// Clone says each line the clone prints to progress, and returns
	// once it has cloned or failed. A clone whose ctx ends is stopped.
	Clone(ctx context.Context, req api.CloneRequest, progress func(string)) (api.Cloned, error)
}

// cloneItem is one item of a projects.clone stream: a line the clone
// printed, or, last, where it went.
type cloneItem struct {
	Line string `json:"line,omitempty"`
	Path string `json:"path,omitempty"`
}

var errCloned = errors.New("transport: the clone is done")

// cloneStream runs one clone and gives its lines, then where it went, as
// a stream's items. The clone stops once the stream's ctx ends, and Close
// waits for it to have, so a folder it made is gone by then.
type cloneStream struct {
	lines  chan string
	done   chan struct{}
	cloned api.Cloned
	err    error
	given  bool // the item saying where it went was given
}

func startClone(ctx context.Context, p Projects, req api.CloneRequest) *cloneStream {
	cs := &cloneStream{lines: make(chan string), done: make(chan struct{})}
	go func() {
		defer close(cs.done)
		cs.cloned, cs.err = p.Clone(ctx, req, func(line string) {
			select {
			case cs.lines <- line:
			case <-ctx.Done():
			}
		})
	}()
	return cs
}

func (cs *cloneStream) Next(ctx context.Context) (cloneItem, error) {
	select {
	case line := <-cs.lines:
		return cloneItem{Line: line}, nil
	case <-cs.done:
		switch {
		case cs.err != nil:
			return cloneItem{}, cs.err
		case cs.given:
			return cloneItem{}, errCloned
		}
		cs.given = true
		return cloneItem{Path: cs.cloned.Path}, nil
	case <-ctx.Done():
		return cloneItem{}, ctx.Err()
	}
}

func (cs *cloneStream) Close() error {
	<-cs.done
	return nil
}

// forgetQuery names the thread whose credentials to forget.
type forgetQuery struct {
	Thread api.ID `json:"thread"`
}

// Providers are what the engine's providers offer a session.
type Providers interface {
	Commands(ctx context.Context, provider, workspace string) ([]provider.Command, error)
	Ask(ctx context.Context, provider string, spec provider.AskSpec) (provider.AskResult, error)
}

// Launches are how the engine's providers' programs start, as
// Settings › Providers sets them up.
type Launches interface {
	Launches(ctx context.Context) ([]LaunchInfo, error)
	// SetLaunch checks l and, when nothing is wrong with it, keeps it in
	// engine.json and starts the provider's next launches as it says.
	SetLaunch(ctx context.Context, provider string, l provider.Launch) (LaunchInfo, error)
}

// LaunchInfo is how one provider's program starts on the engine's
// machine, and what is wrong with that.
type LaunchInfo struct {
	Provider string          `json:"provider"`
	Launch   provider.Launch `json:"launch"` // as engine.json keeps it
	// HomeVar is the variable Launch.Home sets, and DefaultHome the
	// folder the program uses without it; "" when it takes no Home.
	HomeVar     string   `json:"homeVar,omitempty"`
	DefaultHome string   `json:"defaultHome,omitempty"`
	Reserved    []string `json:"reserved,omitempty"` // flags Launch.Args may not set
	// Path is the program it runs: Launch's, or the one found when Launch
	// names none; "" when there is none.
	Path    string `json:"path,omitempty"`
	Version string `json:"version,omitempty"` // what it answers to --version
	// Env and Command are how a new thread starts it, less what each
	// thread adds of its own.
	Env     []string `json:"env,omitempty"`
	Command []string `json:"command,omitempty"`
	// Errors is what is wrong with Launch, setting by setting.
	Errors provider.LaunchError `json:"errors,omitzero"`
	// Saved is that SetLaunch kept it, as it does only when Errors has none.
	Saved bool `json:"saved,omitempty"`
}

// launchQuery sets Provider's launch.
type launchQuery struct {
	Provider string          `json:"provider"`
	Launch   provider.Launch `json:"launch"`
}

// CLIs are the engine's providers and the CLIs they run on its machine.
type CLIs interface {
	// Providers is every provider the engine serves, each asked again
	// when fresh. Only check looks up whether its CLI has a newer version.
	Providers(ctx context.Context, fresh, check bool) []ProviderInfo
	// UpdateProvider runs the updater of name's CLI, refused while a turn
	// of it runs, unless whenIdle has it wait for none to.
	UpdateProvider(ctx context.Context, name string, whenIdle bool) (UpdateResult, error)
}

// ProviderInfo is one provider the engine serves: what it said of itself,
// or why it couldn't say.
type ProviderInfo struct {
	Name  string        `json:"name"`
	Info  provider.Info `json:"info"`
	Error string        `json:"error,omitempty"`
	// State is what the user must do before the provider runs, where
	// Error is what to do: install it, or sign in. "" otherwise.
	State ProviderState `json:"state,omitempty"`
	// Update is what the engine knows of the CLI's versions, when it was
	// asked to check.
	Update *cliupdate.Status `json:"update,omitempty"`
}

// UpdateResult is how an update went: the updater's Output, the end of it
// when long, and Error when it failed or didn't run. Busy is that it
// didn't, as a turn of the provider runs. Provider is the provider as it
// is after.
type UpdateResult struct {
	Output   string       `json:"output,omitempty"`
	Error    string       `json:"error,omitempty"`
	Busy     bool         `json:"busy,omitempty"`
	Provider ProviderInfo `json:"provider"`
}

// ProviderState is what stands between the user and a provider.
type ProviderState string

const (
	ProviderNotInstalled ProviderState = "notInstalled"
	ProviderSignedOut    ProviderState = "signedOut"
)

// providersQuery asks for every provider, each asked again, not answered
// from what it last said, when Fresh, and its CLI's versions when Check.
type providersQuery struct {
	Fresh bool `json:"fresh,omitempty"`
	Check bool `json:"check,omitempty"`
}

// updateQuery asks for an update of Provider's CLI.
type updateQuery struct {
	Provider string `json:"provider"`
	WhenIdle bool   `json:"whenIdle,omitempty"`
}

// commandsQuery asks for the slash commands of Provider's sessions in
// Workspace.
type commandsQuery struct {
	Provider  string `json:"provider"`
	Workspace string `json:"workspace"`
}

// askQuery asks Provider for one answer.
type askQuery struct {
	Provider string           `json:"provider"`
	Spec     provider.AskSpec `json:"spec"`
}

// A handler answers one request with every message for its id.
type handler func(ctx context.Context, params json.RawMessage, o *op)

func (s *Server) methods() map[string]handler {
	m := map[string]handler{}
	if e := s.Endpoint; e != nil {
		m["info"] = callHandler(func(ctx context.Context, _ struct{}) (api.Environment, error) { return e.Info(ctx) })
		m["read"] = callHandler(e.Read)
		m["submit"] = callHandler(e.Submit)
		m["subscribe"] = streamHandler(func(ctx context.Context, sub api.Subscription) (source[api.StreamItem], error) {
			return e.Open(ctx, sub)
		})
	}
	if p := s.Plugins; p != nil {
		m["plugins.tools"] = callHandler(func(ctx context.Context, _ struct{}) (json.RawMessage, error) { return p.PluginTools(ctx) })
		m["plugins.tool"] = callHandler(p.PluginTool)
		m["plugins.call"] = callHandler(p.PluginCall)
	}
	if p := s.Providers; p != nil {
		m["provider.commands"] = callHandler(func(ctx context.Context, q commandsQuery) ([]provider.Command, error) {
			return p.Commands(ctx, q.Provider, q.Workspace)
		})
		m["provider.ask"] = callHandler(func(ctx context.Context, q askQuery) (provider.AskResult, error) {
			return p.Ask(ctx, q.Provider, q.Spec)
		})
	}
	if l := s.Launches; l != nil {
		m["provider.launches"] = callHandler(func(ctx context.Context, _ struct{}) ([]LaunchInfo, error) { return l.Launches(ctx) })
		m["provider.launch"] = callHandler(func(ctx context.Context, q launchQuery) (LaunchInfo, error) {
			return l.SetLaunch(ctx, q.Provider, q.Launch)
		})
	}
	if c := s.CLIs; c != nil {
		m["providers"] = callHandler(func(ctx context.Context, q providersQuery) ([]ProviderInfo, error) {
			return c.Providers(ctx, q.Fresh, q.Check), nil
		})
		m["provider.update"] = callHandler(func(ctx context.Context, q updateQuery) (UpdateResult, error) {
			return c.UpdateProvider(ctx, q.Provider, q.WhenIdle)
		})
	}
	if a := s.Asks; a != nil {
		m["credentials.ask"] = callHandler(a.AskCredentials)
	}
	if c := s.Secrets; c != nil {
		m["credentials.provide"] = callHandler(func(ctx context.Context, p api.Provide) (struct{}, error) {
			return struct{}{}, c.ProvideCredentials(ctx, p)
		})
		m["credentials.forget"] = callHandler(func(ctx context.Context, q forgetQuery) (struct{}, error) {
			return struct{}{}, c.ForgetCredentials(ctx, q.Thread)
		})
	}
	if p := s.Projects; p != nil {
		m["projects.folders"] = callHandler(p.Folders)
		m["projects.clone"] = streamHandler(func(ctx context.Context, req api.CloneRequest) (source[cloneItem], error) {
			return startClone(ctx, p, req), nil
		})
	}
	if h := s.Hub; h != nil {
		m["hub.append"] = callHandler(h.Append)
		m["hub.acked"] = callHandler(func(ctx context.Context, q ackedQuery) (api.UploadAck, error) {
			n, err := h.Acked(ctx, q.Log)
			return api.UploadAck{Acked: n, Protocol: api.Protocol}, err
		})
		m["hub.events"] = callHandler(func(ctx context.Context, q eventsQuery) (eventsPage, error) {
			if q.Limit <= 0 || q.Limit > maxPage {
				q.Limit = maxPage
			}
			evs, err := h.Events(ctx, q.Thread, q.After, q.Limit)
			return eventsPage{Events: evs}, err
		})
		m["hub.follow"] = streamHandler(func(ctx context.Context, q eventsQuery) (source[api.Event], error) {
			return h.Follow(ctx, q.Thread, q.After)
		})
	}
	return m
}

const maxPage = 1000

type ackedQuery struct {
	Log api.ID `json:"log"`
}

// eventsQuery reads one thread when Thread is set, otherwise every thread.
type eventsQuery struct {
	Thread api.ID     `json:"thread,omitzero"`
	After  api.Cursor `json:"after,omitzero"`
	Limit  int        `json:"limit,omitempty"`
}

type eventsPage struct {
	Events []api.Event `json:"events"`
}

func callHandler[In, Out any](fn func(context.Context, In) (Out, error)) handler {
	return func(ctx context.Context, params json.RawMessage, o *op) {
		var in In
		if err := json.Unmarshal(params, &in); err != nil {
			o.send(response{Error: &wireError{Code: "bad_request", Message: err.Error()}})
			return
		}
		out, err := fn(ctx, in)
		if err != nil {
			o.send(response{Error: toWire(err)})
			return
		}
		b, err := json.Marshal(out)
		if err != nil {
			o.send(response{Error: toWire(err)})
			return
		}
		o.send(response{Result: b})
	}
}

type source[T any] interface {
	Next(ctx context.Context) (T, error)
}

func streamHandler[In, T any](open func(context.Context, In) (source[T], error)) handler {
	return func(ctx context.Context, params json.RawMessage, o *op) {
		var in In
		if err := json.Unmarshal(params, &in); err != nil {
			o.send(response{Error: &wireError{Code: "bad_request", Message: err.Error()}})
			return
		}
		src, err := open(ctx, in)
		if err != nil {
			o.send(response{Error: toWire(err)})
			return
		}
		if c, ok := src.(io.Closer); ok {
			defer c.Close()
		}
		o.send(response{Open: true})
		for {
			if o.room(ctx) != nil {
				return
			}
			item, err := src.Next(ctx)
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				o.send(response{Error: toWire(err)})
				return
			}
			b, err := json.Marshal(item)
			if err != nil {
				o.send(response{Error: toWire(err)})
				return
			}
			o.unacked.Add(int64(len(b)))
			o.send(response{Item: b})
		}
	}
}

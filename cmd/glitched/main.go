// Command glitched runs the host engine and the hub role, and drives them
// from a terminal until the GUI attaches to them.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"glitched/internal/api"
	"glitched/internal/browsermcp"
	"glitched/internal/daemon"
	eng "glitched/internal/engine"
	"glitched/internal/mcp"
	"glitched/internal/plain"
	"glitched/internal/tailnet"
	"glitched/internal/transport"
)

const exitLocked = 3

// usageError is a flag combination that can't work. main exits 2 on it, as
// flag does on a flag it can't parse.
type usageError string

func (e usageError) Error() string { return string(e) }

var commands = map[string]func(context.Context, []string) error{
	"serve":       serve,
	"status":      status,
	"pair":        pair,
	"run":         run,
	"threads":     threads,
	"replica":     replica,
	"mcp":         serveMCP,
	"browser-mcp": browserMCP,
}

const usage = `usage: glitched <command> [flags]

  serve     run the host engine, and the hub role unless -hub=false; the next serve
            on -dir, and the GUI's own engine, run as it did unless a flag says otherwise
  status    whether an engine serves -dir, its hub, its tailnet listeners and its tokens
  pair      mint a token for a client on the tailnet: glitched pair -name LABEL | -list | -revoke LABEL
  run       send a message and stream the turn: glitched run [-thread ID] [-provider claude] [-yes] text...
  threads   list this host's threads
  replica   print a thread's events as the hub holds them: glitched replica -thread ID
  mcp       serve MCP on stdin and stdout for the agent the engine started it for
  browser-mcp
            serve the browser of the app on -dir to an agent, as an MCP server on stdio;
            the engine gives it to each Claude Code run

run, threads and replica reach the engine under -dir, or another with -server ADDR -token TOKEN.`

func main() {
	if len(os.Args) < 2 || commands[os.Args[1]] == nil {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := commands[os.Args[1]](ctx, os.Args[2:])
	stop()
	switch {
	case errors.Is(err, daemon.ErrLocked):
		fmt.Fprintln(os.Stderr, "glitched:", err)
		os.Exit(exitLocked)
	case errors.As(err, new(usageError)):
		fmt.Fprintln(os.Stderr, "glitched:", err)
		os.Exit(2)
	case err != nil:
		fmt.Fprintln(os.Stderr, "glitched:", err)
		os.Exit(1)
	}
}

// serveMCP is glitched mcp, which each agent the engine runs starts.
func serveMCP(ctx context.Context, _ []string) error {
	return mcp.Main(ctx, os.Stdin, os.Stdout)
}

func dirFlag(fs *flag.FlagSet) *string {
	home, _ := os.UserHomeDir()
	return fs.String("dir", filepath.Join(home, ".glitched"), "state directory")
}

// reach adds the flags that pick the engine a command talks to: the one
// under -dir, or the one at -server with a token it paired.
func reach(fs *flag.FlagSet) func() (transport.Transport, error) {
	dir := dirFlag(fs)
	server := fs.String("server", "", "tailnet address of another engine, instead of the one under -dir")
	token := fs.String("token", "", "the token -server paired for this client")
	return func() (transport.Transport, error) {
		switch {
		case *server == "" && *token != "":
			return nil, usageError(fs.Name() + ": -token goes with -server; the engine under -dir takes none")
		case *server == "":
			return daemon.Socket(*dir), nil
		case *token == "":
			return nil, usageError(fs.Name() + ": -server needs -token, which glitched pair printed on that engine")
		}
		return transport.Tailnet{Address: *server, Token: *token}, nil
	}
}

// given reports which flags fs's arguments set.
func given(fs *flag.FlagSet) map[string]bool {
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	return set
}

type listFlag []string

func (l *listFlag) String() string { return strings.Join(*l, ",") }

func (l *listFlag) Set(v string) error {
	*l = append(*l, v)
	return nil
}

func serve(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	dir := dirFlag(fs)
	hub := fs.Bool("hub", true, "run the hub role in this process, unless -hub-dir or -hub-server names another")
	hubDir := fs.String("hub-dir", "", "upload to the hub of the engine with this state directory, on this machine")
	hubServer := fs.String("hub-server", "", "upload to the hub of the engine at this tailnet address")
	hubToken := fs.String("hub-token", "", "the token -hub-server paired for this engine")
	epoch := fs.Uint64("epoch", 1, "this host's epoch at the hub")
	var listen listFlag
	fs.Var(&listen, "listen", "also serve clients with a token on this loopback or Tailscale address; repeatable, and -listen '' serves none")
	tailnetOn := fs.Bool("tailnet", false, "serve clients with a token on the tailnet at port "+daemon.Port+": on an installed Tailscale, else on the engine's own node")
	control := fs.String("control", "", "the control server the engine's own node signs in to, such as Headscale's; Tailscale's when empty")
	def := eng.DefaultLimits
	maxWorking := fs.Int("max-working", def.Working, "the most child threads agents run under one thread at once")
	maxChildren := fs.Int("max-children", def.All, "the most child threads one thread has, not counting deleted ones")
	maxDepth := fs.Int("max-depth", def.Depth, fmt.Sprintf("how far below a thread of yours agents start child threads, 1 to %d", api.MaxDepth))
	fs.Parse(args)

	set := given(fs)
	switch {
	case *hubDir != "" && *hubServer != "":
		return usageError("serve: -hub-dir and -hub-server both name the hub; give one")
	case set["hub"] && *hub && (*hubDir != "" || *hubServer != ""):
		return usageError("serve: -hub runs the hub in this engine, so -hub-dir or -hub-server can't name another")
	case *hubToken != "" && *hubServer == "":
		return usageError("serve: -hub-token goes with -hub-server")
	case *hubServer != "" && *hubToken == "":
		return usageError("serve: -hub-server needs -hub-token, which glitched pair printed on that engine")
	case *maxWorking < 1 || *maxChildren < 1:
		return usageError("serve: -max-working and -max-children are at least 1")
	case *maxDepth < 1 || *maxDepth > api.MaxDepth:
		return usageError(fmt.Sprintf("serve: -max-depth is 1 to %d", api.MaxDepth))
	}
	cfg, err := daemon.Defaults(*dir)
	if err != nil {
		return err
	}
	// Kept for the next start, which may run from elsewhere.
	if *hubDir != "" {
		if *hubDir, err = filepath.Abs(*hubDir); err != nil {
			return err
		}
	}
	if set["hub"] || set["hub-dir"] || set["hub-server"] {
		cfg.Hub = *hub && *hubDir == "" && *hubServer == ""
		cfg.HubDir, cfg.HubServer, cfg.HubToken = *hubDir, *hubServer, *hubToken
	}
	if set["epoch"] {
		cfg.Epoch = *epoch
	}
	if set["listen"] {
		cfg.Listen = slices.DeleteFunc(listen, func(addr string) bool { return addr == "" })
	}
	if set["tailnet"] {
		cfg.Tailnet = *tailnetOn
	}
	if set["control"] {
		cfg.Control = *control
	}
	if set["max-working"] {
		cfg.Children.Working = *maxWorking
	}
	if set["max-children"] {
		cfg.Children.All = *maxChildren
	}
	if set["max-depth"] {
		cfg.Children.Depth = *maxDepth
	}
	cfg.WithClaude()
	d, err := daemon.Start(ctx, cfg)
	if err != nil {
		return err
	}
	if err := cfg.Save(); err != nil {
		return errors.Join(err, d.Close())
	}
	return d.Wait()
}

// browserMCP serves the app's browser to an agent on stdio.
func browserMCP(ctx context.Context, args []string) error {
	return browsermcp.Main(ctx, args, os.Stdin, os.Stdout)
}

func status(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	dir := dirFlag(fs)
	fs.Parse(args)
	st, err := daemon.Inspect(ctx, *dir)
	if err != nil {
		return err
	}
	row := func(k, v string) { fmt.Printf("%-10s %s\n", k, v) }
	if e := st.Engine; e != nil {
		row("engine", fmt.Sprintf("serving as %s (host %s, protocol %d)", e.Name, e.ID, e.Protocol))
	} else {
		row("engine", "not running: "+st.EngineErr.Error())
	}
	switch cfg := st.Config; {
	case cfg.HubServer != "":
		row("hub", "uploads to "+cfg.HubServer)
	case cfg.HubDir != "":
		row("hub", "uploads to the engine under "+cfg.HubDir)
	case cfg.Hub:
		row("hub", "this engine")
	default:
		row("hub", "none")
	}
	tn := st.Tailnet
	switch tn.Mode {
	case api.TailnetInstalled:
		row("tailscale", "installed, "+tailnetState(tn))
	case api.TailnetEmbedded:
		via := "Tailscale"
		if tn.Control != "" {
			via = tn.Control
		}
		row("tailscale", "the engine's own node, on "+via+", "+tailnetState(tn))
	default:
		row("tailscale", "not installed; serve -tailnet runs the engine's own node")
	}
	if tn.AuthURL != "" {
		row("sign in", tn.AuthURL)
	}
	switch {
	case tn.Serving != "":
		row("tailnet", "serving at "+tn.Serving)
	case st.Config.Tailnet:
		row("tailnet", "waiting to serve")
	default:
		row("tailnet", "not serving; serve -tailnet to reach this engine from other machines")
	}
	for _, l := range st.Listeners {
		state := "accepting"
		if !l.Accepting {
			state = "not accepting"
		}
		row("listen", l.Addr+"  "+state)
	}
	if len(st.Tokens) == 0 {
		row("tokens", "none; glitched pair -name LABEL mints one")
	}
	for _, t := range st.Tokens {
		row("token", t.Label+"  "+t.Created.Local().Format(time.DateTime))
	}
	return nil
}

// tailnetState is how a machine is on the tailnet, in a few words.
func tailnetState(t api.Tailnet) string {
	if !t.Running() {
		return strings.ToLower(t.State)
	}
	s := "up as " + t.Name
	if t.Name == "" {
		s = "up"
	}
	if t.IP.IsValid() {
		s += " (" + t.IP.String() + ")"
	}
	if t.User != "" {
		s += ", signed in as " + t.User
	}
	return s
}

func pair(_ context.Context, args []string) error {
	fs := flag.NewFlagSet("pair", flag.ExitOnError)
	dir := dirFlag(fs)
	name := fs.String("name", "", "mint a token with this label")
	list := fs.Bool("list", false, "list the tokens")
	revoke := fs.String("revoke", "", "revoke the token with this label")
	fs.Parse(args)
	switch {
	case *list:
		ts, err := daemon.Tokens(*dir)
		if err != nil {
			return err
		}
		for _, t := range ts {
			fmt.Printf("%s  %s\n", t.Created.Local().Format(time.DateTime), t.Label)
		}
		return nil
	case *revoke != "":
		if err := daemon.Revoke(*dir, *revoke); err != nil {
			return err
		}
		fmt.Println("revoked", *revoke)
		return nil
	case *name == "":
		return usageError("pair: give -name, -list or -revoke")
	}
	token, err := daemon.Pair(*dir, *name)
	if err != nil {
		return err
	}
	fmt.Printf("Token for %s, shown only this once:\n\n  %s\n\n", *name, token)
	cmd := "glitched serve"
	if given(fs)["dir"] {
		cmd += " -dir " + shellQuote(*dir)
	}
	host := "<this machine's tailnet name>"
	probe, cancel := context.WithTimeout(context.Background(), time.Second)
	if t, err := tailnet.Detect(probe); err == nil && t.Name != "" {
		host = t.Name
	}
	cancel()
	addr := net.JoinHostPort(host, daemon.Port)
	fmt.Printf("Serve on the tailnet with:\n\n  %s -tailnet\n\nThen on the other machine:\n\n  glitched run -server %s -token %s hello\n", cmd, addr, token)
	return nil
}

// shellQuote quotes s for a POSIX shell, unless it needs none.
func shellQuote(s string) string {
	if s != "" && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_./:@%+,") == "" {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func run(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	at := reach(fs)
	threadFlag := fs.String("thread", "", "thread to send to; a new one when empty")
	yes := fs.Bool("yes", false, "approve every request, and dismiss every question, without asking")
	modeFlag := fs.String("mode", "after", "now, after, steer, restart or defer; restart also ends an uncertain run")
	providerFlag := fs.String("provider", "fake", "the agent a new thread runs: fake or claude")
	modelFlag := fs.String("model", "", "the model a new thread runs, such as haiku; empty is the provider's default")
	fs.Parse(args)
	mode, ok := modes[*modeFlag]
	if !ok {
		return usageError(fmt.Sprintf("run: unknown mode %q", *modeFlag))
	}
	text := strings.Join(fs.Args(), " ")
	if text == "" {
		return usageError("run: no message")
	}
	tr, err := at()
	if err != nil {
		return err
	}
	ep, err := dialEngine(ctx, tr)
	if err != nil {
		return err
	}
	defer ep.Close()

	var thread api.ID
	if *threadFlag != "" {
		var err error
		if thread, err = api.ParseID(*threadFlag); err != nil {
			return err
		}
	} else {
		thread = api.NewID()
		wd, _ := os.Getwd()
		create := api.ThreadCreate{Workspace: wd, Title: text, Model: api.ModelRef{Provider: *providerFlag, Model: *modelFlag}}
		if _, err := ep.Submit(ctx, api.Command{ID: api.NewID(), Target: thread, Body: create}); err != nil {
			return err
		}
		fmt.Println("thread", thread)
	}

	snap, err := ep.Read(ctx, api.Query{Thread: thread})
	if err != nil {
		return err
	}
	stream, err := ep.Open(ctx, api.Subscription{Thread: thread, After: snap.Cursor})
	if err != nil {
		return err
	}
	defer stream.Close()

	msg := api.NewID()
	if _, err := ep.Submit(ctx, api.Command{ID: msg, Target: thread, Body: api.MessageDispatch{Text: text, Mode: mode}}); err != nil {
		return err
	}
	// A message starts a run under its own ID unless steering claims it into
	// the live run; the command streams whichever run carries it.
	runID := msg
	in := bufio.NewReader(os.Stdin)
	asked := map[api.ID]bool{}
	ask := func(r api.Request) error {
		if r.Run != runID || asked[r.ID] {
			return nil
		}
		asked[r.ID] = true
		var answer []byte
		if r.Kind == api.KindQuestion {
			var a api.Answers // -yes, or questions it can't show, dismiss
			if q, err := api.ParseQuestions(r.Body); err == nil && !*yes {
				a = askQuestions(in, os.Stdout, q)
			}
			answer, _ = json.Marshal(a)
		} else {
			allow := *yes
			if !allow {
				kind, k := plain.Text(r.Kind, false)
				body, b := plain.Text(string(r.Body), false)
				fmt.Print("\n" + asks)
				if k || b {
					fmt.Print("\n  ! " + escapes)
				}
				fmt.Printf("\n  ? %s %s [y/N] ", kind, body)
				line, _ := in.ReadString('\n')
				allow = strings.HasPrefix(strings.ToLower(line), "y")
			}
			answer, _ = json.Marshal(map[string]bool{"allow": allow})
		}
		respond := api.RequestRespond{Request: r.ID, Generation: r.Generation, Answer: answer}
		_, err := ep.Submit(ctx, api.Command{ID: api.NewID(), Target: thread, Body: respond})
		if errors.Is(err, api.ErrAlreadyAnswered) {
			return nil
		}
		return err
	}
	for {
		item, err := stream.Next(ctx)
		if err != nil {
			return err
		}
		if item.Kind != api.StreamEvent {
			continue
		}
		body, err := item.Event.Decode()
		if err != nil {
			return err
		}
		switch b := body.(type) {
		case api.MessageQueued:
			if b.Message == msg && b.Held {
				fmt.Println("[held until promoted]")
				return nil
			}
		case api.MessageClaimed:
			if b.Message != msg {
				continue
			}
			runID = b.Run
			// The run may have asked before this command subscribed.
			snap, err := ep.Read(ctx, api.Query{Thread: thread})
			if err != nil {
				return err
			}
			for _, t := range snap.Threads {
				for _, r := range t.Requests {
					if err := ask(r); err != nil {
						return err
					}
				}
			}
		case api.MessageUncertain:
			if b.Message == msg {
				return errors.New("the message may or may not have reached the agent")
			}
		case api.MessageCancelled:
			if b.Message == msg {
				return fmt.Errorf("the message was cancelled: %s", b.Reason)
			}
		case api.AssistantDelta:
			if b.Run == runID {
				fmt.Print(shown(b.Text))
			}
		case api.WorkItem:
			if b.Run == runID && b.Status != api.WorkStarted {
				fmt.Printf("\n  %s %s\n", marks[b.Status], shown(b.Title))
			}
		case api.RequestOpened:
			r := api.Request{ID: b.Request, Run: b.Run, Generation: b.Generation, Kind: b.Kind, Body: b.Body}
			if err := ask(r); err != nil {
				return err
			}
		case api.TurnEnded:
			if b.Run != runID {
				continue
			}
			if b.Error != "" {
				fmt.Printf("\n✗ %s\n", strings.ReplaceAll(shown(b.Error), "\n", "\n  "))
			}
			fmt.Printf("\n[turn ended: %s]\n", outcomes[b.Outcome])
			return nil
		case api.TurnUncertain:
			if b.Run == runID {
				return errors.New("the run is uncertain after a restart")
			}
		}
	}
}

// shown is the agent's text as glitched run prints it: in printable
// ASCII, so that no escape sequence can hide or redraw a [y/N] prompt.
func shown(s string) string {
	s, _ = plain.Text(s, true)
	return s
}

// asks heads each [y/N] prompt with a line that the agent's text, printed
// in ASCII, can't print.
var asks = strings.Repeat("─", 3) + " glitched asks " + strings.Repeat("─", 40)

// escapes says what the [y/N] prompt shows as escapes.
const escapes = `Shown with escapes: \u{...} is text outside printable ASCII, \\u{ a literal \u{`

var modes = map[string]api.DispatchMode{
	"now":     api.StartNow,
	"after":   api.AfterActive,
	"steer":   api.SteerActive,
	"restart": api.RestartActive,
	"defer":   api.Defer,
}

var marks = map[api.WorkStatus]string{
	api.WorkCompleted: "✓",
	api.WorkFailed:    "✗",
}

var outcomes = map[api.Outcome]string{
	api.OutcomeCompleted:   "completed",
	api.OutcomeInterrupted: "interrupted",
	api.OutcomeFailed:      "failed",
	api.OutcomeAbandoned:   "abandoned",
}

var runStates = map[api.RunState]string{
	api.RunIdle:      "idle",
	api.RunStarting:  "starting",
	api.RunRunning:   "running",
	api.RunWaiting:   "waiting",
	api.RunStopping:  "stopping",
	api.RunUncertain: "uncertain",
}

// dialEngine reaches the engine at tr, refusing one of another protocol,
// whose threads this build would misread.
func dialEngine(ctx context.Context, tr transport.Transport) (*transport.EndpointClient, error) {
	ep := transport.NewEndpointClient(tr, api.ID{})
	info, err := ep.Info(ctx)
	if err == nil && info.Protocol != api.Protocol {
		err = &api.ProtocolError{Theirs: info.Protocol}
	}
	if err != nil {
		ep.Close()
		return nil, err
	}
	return ep, nil
}

func threads(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("threads", flag.ExitOnError)
	at := reach(fs)
	fs.Parse(args)
	tr, err := at()
	if err != nil {
		return err
	}
	ep, err := dialEngine(ctx, tr)
	if err != nil {
		return err
	}
	defer ep.Close()
	snap, err := ep.Read(ctx, api.Query{})
	if err != nil {
		return err
	}
	for _, t := range snap.Threads {
		fmt.Printf("%s  %-9s  %s\n", t.ID, runStates[t.Run], t.Title)
	}
	return nil
}

func replica(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("replica", flag.ExitOnError)
	at := reach(fs)
	threadFlag := fs.String("thread", "", "thread whose replicated events to print")
	fs.Parse(args)
	tr, err := at()
	if err != nil {
		return err
	}
	thread, err := api.ParseID(*threadFlag)
	if err != nil {
		return err
	}
	hubc := transport.NewHubClient(tr, api.ID{})
	defer hubc.Close()
	evs, err := hubc.Events(ctx, thread, api.Cursor{}, 1000)
	if err != nil {
		return err
	}
	for _, e := range evs {
		fmt.Printf("%d  %s\n", e.Cursor.Seq, e.Type)
	}
	return nil
}

// askQuestions asks each question on out and reads its answer from in: an
// option's number, numbers apart by commas in a multi question, or the
// user's own words. An empty line dismisses them all. The agent's text
// shows as printable ASCII, as the [y/N] prompt's does.
func askQuestions(in *bufio.Reader, out io.Writer, q api.Questions) api.Answers {
	var as api.Answers
	for _, x := range q.Questions {
		escaped := false
		shown := func(s string) string {
			s, e := plain.Text(s, false)
			escaped = escaped || e
			return s
		}
		var b strings.Builder
		fmt.Fprint(&b, "  ? ")
		if x.Header != "" {
			fmt.Fprintf(&b, "%s: ", shown(x.Header))
		}
		fmt.Fprint(&b, shown(x.Text))
		if x.Multi {
			fmt.Fprint(&b, " (any: 1,3)")
		}
		fmt.Fprintln(&b)
		for i, o := range x.Options {
			fmt.Fprintf(&b, "    %d) %s", i+1, shown(o.Label))
			if o.About != "" {
				fmt.Fprintf(&b, "   %s", shown(o.About))
			}
			fmt.Fprintln(&b)
		}
		fmt.Fprintln(out)
		if escaped {
			fmt.Fprintln(out, "  ! "+escapes)
		}
		fmt.Fprint(out, b.String(), "    Type a number, your own answer, or Enter to dismiss.\n  > ")
		for {
			line, _ := in.ReadString('\n')
			line = strings.TrimSpace(line)
			if line == "" {
				fmt.Fprintln(out, "  dismissed")
				return api.Answers{}
			}
			a := answerOf(x, line)
			if (api.Answers{Answers: []api.Answer{a}}).Fit(api.Questions{Questions: []api.Question{x}}) == nil {
				as.Answers = append(as.Answers, a)
				break
			}
			fmt.Fprint(out, "  That answers nothing. Type a number, your own answer, or Enter to dismiss.\n  > ")
		}
	}
	fmt.Fprintln(out, "  answered")
	return as
}

// answerOf is what a line answers x: the options its numbers name, and
// in a multi question its other words beside them; a line that is not
// all numbers, in another, is the user's own words.
func answerOf(x api.Question, line string) api.Answer {
	var a api.Answer
	var words []string
	for f := range strings.SplitSeq(line, ",") {
		f = strings.TrimSpace(f)
		if n, err := strconv.Atoi(f); err == nil && n >= 1 && n <= len(x.Options) && !slices.Contains(a.Picked, x.Options[n-1].Label) {
			a.Picked = append(a.Picked, x.Options[n-1].Label)
		} else if f != "" {
			words = append(words, f)
		}
	}
	if !x.Multi && (len(words) > 0 || len(a.Picked) > 1) {
		return api.Answer{Other: line}
	}
	a.Other = strings.Join(words, ", ")
	return a
}

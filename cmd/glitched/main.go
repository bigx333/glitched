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
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"glitched/internal/api"
	"glitched/internal/daemon"
	"glitched/internal/transport"
)

const exitLocked = 3

// usageError is a flag combination that can't work. main exits 2 on it, as
// flag does on a flag it can't parse.
type usageError string

func (e usageError) Error() string { return string(e) }

var commands = map[string]func(context.Context, []string) error{
	"serve":   serve,
	"pair":    pair,
	"run":     run,
	"threads": threads,
	"replica": replica,
}

const usage = `usage: glitched <command> [flags]

  serve     run the host engine, and the hub role unless -hub=false; the next serve
            on -dir, and the GUI's own engine, run as it did unless a flag says otherwise
  pair      mint a token for a client on the tailnet: glitched pair -name LABEL | -list | -revoke LABEL
  run       send a message and stream the turn: glitched run [-thread ID] [-yes] text...
  threads   list this host's threads
  replica   print a thread's events as the hub holds them: glitched replica -thread ID

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
	d, err := daemon.Start(ctx, cfg)
	if err != nil {
		return err
	}
	if err := cfg.Save(); err != nil {
		return errors.Join(err, d.Close())
	}
	return d.Wait()
}

// port is where pair suggests serving; any port works.
const port = "7465"

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
	ip, ok := transport.TailscaleIP()
	if !ok {
		fmt.Printf("Tailscale isn't up here. Once it is, serve with:\n\n  %s -listen <this machine's Tailscale IP>:%s\n", cmd, port)
		return nil
	}
	addr := net.JoinHostPort(ip.String(), port)
	fmt.Printf("Serve with:\n\n  %s -listen %s\n\nThen on the other machine:\n\n  glitched run -server %s -token %s hello\n", cmd, addr, addr, token)
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
	yes := fs.Bool("yes", false, "approve every request without asking")
	modeFlag := fs.String("mode", "after", "now, after, steer, restart or defer; restart also ends an uncertain run")
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
	ep := transport.NewEndpointClient(tr, api.ID{})
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
		create := api.ThreadCreate{Workspace: wd, Title: text, Model: api.ModelRef{Provider: "fake", Model: "scripted"}}
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
		allow := *yes
		if !allow {
			fmt.Printf("\n  ? %s %s [y/N] ", r.Kind, r.Body)
			line, _ := in.ReadString('\n')
			allow = strings.HasPrefix(strings.ToLower(line), "y")
		}
		answer, _ := json.Marshal(map[string]bool{"allow": allow})
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
				fmt.Print(b.Text)
			}
		case api.WorkItem:
			if b.Run == runID && b.Status == api.WorkCompleted {
				fmt.Printf("\n  ✓ %s\n", b.Title)
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
			fmt.Printf("\n[turn ended: %s]\n", outcomes[b.Outcome])
			return nil
		case api.TurnUncertain:
			if b.Run == runID {
				return errors.New("the run is uncertain after a restart")
			}
		}
	}
}

var modes = map[string]api.DispatchMode{
	"now":     api.StartNow,
	"after":   api.AfterActive,
	"steer":   api.SteerActive,
	"restart": api.RestartActive,
	"defer":   api.Defer,
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

func threads(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("threads", flag.ExitOnError)
	at := reach(fs)
	fs.Parse(args)
	tr, err := at()
	if err != nil {
		return err
	}
	ep := transport.NewEndpointClient(tr, api.ID{})
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

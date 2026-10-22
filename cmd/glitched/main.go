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
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"glitched/internal/api"
	"glitched/internal/hub"
	"glitched/internal/provider/fake"
	"glitched/internal/runtime"
	"glitched/internal/store"
	"glitched/internal/transport"
)

// errLocked exits with its own code so a service manager doesn't restart a
// second engine in a loop.
var errLocked = errors.New("another engine holds the lock")

const exitLocked = 3

var commands = map[string]func(context.Context, []string) error{
	"serve":   serve,
	"run":     run,
	"threads": threads,
	"replica": replica,
}

const usage = `usage: glitched <command> [flags]

  serve     run the host engine, and the hub role unless -hub=false
  run       send a message and stream the turn: glitched run [-thread ID] [-yes] text...
  threads   list this host's threads
  replica   print a thread's events as the hub holds them: glitched replica -thread ID`

func main() {
	if len(os.Args) < 2 || commands[os.Args[1]] == nil {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := commands[os.Args[1]](ctx, os.Args[2:])
	stop()
	switch {
	case errors.Is(err, errLocked):
		fmt.Fprintln(os.Stderr, "glitched:", err)
		os.Exit(exitLocked)
	case err != nil:
		fmt.Fprintln(os.Stderr, "glitched:", err)
		os.Exit(1)
	}
}

func dirFlag(fs *flag.FlagSet) *string {
	home, _ := os.UserHomeDir()
	return fs.String("dir", filepath.Join(home, ".glitched"), "state directory")
}

func socket(dir string) transport.Loopback {
	return transport.Loopback{Network: "unix", Address: filepath.Join(dir, "engine.sock")}
}

func serve(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	dir := dirFlag(fs)
	asHub := fs.Bool("hub", true, "run the hub role in this process")
	hubDir := fs.String("hub-dir", "", "state directory of the engine that runs the hub, when -hub=false")
	epoch := fs.Uint64("epoch", 1, "this host's epoch at the hub")
	fs.Parse(args)

	if err := os.MkdirAll(*dir, 0o700); err != nil {
		return err
	}
	lk, err := lock(filepath.Join(*dir, "engine.lock"))
	if err != nil {
		return err
	}
	defer lk.Close()
	lb := socket(*dir)
	// The lock is ours, so a socket file left here is from a dead engine.
	os.Remove(lb.Address)

	st, err := store.Open(filepath.Join(*dir, "engine.db"))
	if err != nil {
		return err
	}
	defer st.Close()
	h, err := runtime.New(ctx, st, fake.New(fake.Turn(true)))
	if err != nil {
		return err
	}
	defer h.Close()

	srv := &transport.Server{Transport: lb, Endpoint: h}
	var hubc hub.Client
	switch {
	case *asHub:
		rep, err := hub.Open(filepath.Join(*dir, "hub.db"))
		if err != nil {
			return err
		}
		defer rep.Close()
		srv.Hub, hubc = rep, rep
	case *hubDir != "":
		hubc = transport.NewHubClient(socket(*hubDir), api.ID{})
	}
	if hubc != nil {
		up := hub.NewUploader(st.HostID(), *epoch, st, hubc)
		st.OnCommit(func([]api.Event) { up.Notify() })
		go func() {
			if err := up.Run(ctx); err != nil && ctx.Err() == nil {
				slog.Error("uploader stopped", "err", err)
			}
		}()
	}

	ln, err := lb.Listen(ctx)
	if err != nil {
		return err
	}
	slog.Info("serving", "socket", lb.Address, "host", st.HostID(), "log", st.LogID(), "hub", *asHub)
	return srv.Serve(ctx, ln)
}

func run(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	dir := dirFlag(fs)
	threadFlag := fs.String("thread", "", "thread to send to; a new one when empty")
	yes := fs.Bool("yes", false, "approve every request without asking")
	modeFlag := fs.String("mode", "after", "now, after, steer, restart or defer; restart also ends an uncertain run")
	fs.Parse(args)
	mode, ok := modes[*modeFlag]
	if !ok {
		return fmt.Errorf("run: unknown mode %q", *modeFlag)
	}
	text := strings.Join(fs.Args(), " ")
	if text == "" {
		return errors.New("run: no message")
	}
	ep := transport.NewEndpointClient(socket(*dir), api.ID{})

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
	in := bufio.NewReader(os.Stdin)
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
		case api.AssistantDelta:
			fmt.Print(b.Text)
		case api.WorkItem:
			if b.Status == api.WorkCompleted {
				fmt.Printf("\n  ✓ %s\n", b.Title)
			}
		case api.RequestOpened:
			if b.Run != msg {
				continue
			}
			allow := *yes
			if !allow {
				fmt.Printf("\n  ? %s %s [y/N] ", b.Kind, b.Body)
				line, _ := in.ReadString('\n')
				allow = strings.HasPrefix(strings.ToLower(line), "y")
			}
			answer, _ := json.Marshal(map[string]bool{"allow": allow})
			respond := api.RequestRespond{Request: b.Request, Generation: b.Generation, Answer: answer}
			if _, err := ep.Submit(ctx, api.Command{ID: api.NewID(), Target: thread, Body: respond}); err != nil {
				return err
			}
		case api.TurnEnded:
			if b.Run != msg {
				continue
			}
			fmt.Printf("\n[turn ended: %s]\n", outcomes[b.Outcome])
			return nil
		case api.TurnUncertain:
			if b.Run == msg {
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
	dir := dirFlag(fs)
	fs.Parse(args)
	snap, err := transport.NewEndpointClient(socket(*dir), api.ID{}).Read(ctx, api.Query{})
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
	dir := dirFlag(fs)
	threadFlag := fs.String("thread", "", "thread whose replicated events to print")
	fs.Parse(args)
	thread, err := api.ParseID(*threadFlag)
	if err != nil {
		return err
	}
	evs, err := transport.NewHubClient(socket(*dir), api.ID{}).Events(ctx, thread, api.Cursor{}, 1000)
	if err != nil {
		return err
	}
	for _, e := range evs {
		fmt.Printf("%d  %s\n", e.Cursor.Seq, e.Type)
	}
	return nil
}

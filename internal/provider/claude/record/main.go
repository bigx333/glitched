// Command record replays real Claude Code turns into the transcripts that
// the fake claude in the package's tests plays back. It needs a signed-in
// claude on PATH, and spends a few cents of haiku:
//
//	go run ./internal/provider/claude/record
//
// It drives the real adapter, with itself as the claude binary: in that
// role it runs the real one and tees every line each way into a transcript.
// It denies every tool call Claude Code asks about. The transcripts of
// tool prompts come from the claudelive contract test, run with -record.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"glitched/internal/api"
	"glitched/internal/provider"
	"glitched/internal/provider/claude"
	"glitched/internal/provider/claude/transcript"
)

func main() {
	if code, teeing := transcript.Tee(); teeing {
		os.Exit(code)
	}
	out := flag.String("out", "internal/provider/claude/testdata", "where the transcripts go")
	flag.Parse()
	if err := record(*out); err != nil {
		log.Fatal(err)
	}
}

type scenario struct {
	name, prompt string
	thread       *api.ID // shared with an earlier scenario, to resume its session
	signedOut    bool
}

func record(out string) error {
	real, err := exec.LookPath("claude")
	if err != nil {
		return err
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	version, _ := claude.Command(context.Background(), real, "--version").Output()
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	out, _ = filepath.Abs(out)
	// A fixed path, as the model's text may quote it in pieces the tee's
	// scrub can't see.
	ws := "/tmp/glitched-record"
	os.RemoveAll(ws)
	if err := os.Mkdir(ws, 0o755); err != nil {
		return err
	}
	defer os.RemoveAll(ws)
	if err := os.WriteFile(filepath.Join(ws, "hello.txt"), []byte("hello from glitched\n"), 0o644); err != nil {
		return err
	}
	os.Setenv(transcript.EnvReal, real)
	hi, tools := api.NewID(), api.NewID()
	for _, s := range []scenario{
		{name: "hi", prompt: "Say hi in three words.", thread: &hi},
		{name: "again", prompt: "What did I ask you to do last time? Answer in under ten words.", thread: &hi},
		{name: "tools", prompt: "Read hello.txt. Then use the Write tool to create out.txt containing ok. Then reply done.", thread: &tools},
		{name: "logged-out", prompt: "say hi", signedOut: true},
	} {
		thread := api.NewID()
		if s.thread != nil {
			thread = *s.thread
		}
		restore, err := environ(s)
		if err != nil {
			return err
		}
		os.Setenv(transcript.EnvOut, filepath.Join(out, s.name))
		evs, err := run(&claude.Adapter{Path: self}, provider.SessionSpec{
			Thread: thread, Run: api.NewID(), Workspace: ws, Model: api.ModelRef{Provider: "claude", Model: "haiku"},
		}, s.prompt)
		restore()
		if err != nil {
			return fmt.Errorf("%s: %w", s.name, err)
		}
		fmt.Printf("%s: %d events, last %+v\n", s.name, len(evs), evs[len(evs)-1])
	}
	fmt.Printf("✓ transcripts from claude %s", version)
	return nil
}

// environ is the environment a scenario runs claude in, and how to put the
// recorder's back. A signed-out claude gets an empty config and no token.
func environ(s scenario) (func(), error) {
	if !s.signedOut {
		return func() {}, nil
	}
	dir, err := os.MkdirTemp("", "claude-signed-out")
	if err != nil {
		return nil, err
	}
	keys := []string{"CLAUDE_CONFIG_DIR", "CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN"}
	saved := map[string]*string{}
	for _, k := range keys {
		if v, ok := os.LookupEnv(k); ok {
			saved[k] = &v
		} else {
			saved[k] = nil
		}
		os.Unsetenv(k)
	}
	os.Setenv("CLAUDE_CONFIG_DIR", dir)
	return func() {
		for k, v := range saved {
			if v == nil {
				os.Unsetenv(k)
			} else {
				os.Setenv(k, *v)
			}
		}
		os.RemoveAll(dir)
	}, nil
}

func run(ad provider.Adapter, spec provider.SessionSpec, prompt string) ([]provider.Event, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	ag, err := ad.Open(ctx, spec)
	if err != nil {
		return nil, err
	}
	defer ag.Close(context.Background())
	if err := ag.Do(ctx, provider.Start{Text: prompt}); err != nil {
		return nil, err
	}
	var evs []provider.Event
	for {
		e, err := ag.Next(ctx)
		if err != nil {
			return evs, err
		}
		evs = append(evs, e)
		switch e := e.(type) {
		case provider.RequestOpened:
			if err := ag.Do(ctx, provider.Respond{Request: e.ID, Answer: json.RawMessage(`{"allow":false}`)}); err != nil {
				return evs, err
			}
		case provider.TurnEnded:
			return evs, nil
		}
	}
}

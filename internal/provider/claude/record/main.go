// Command record replays real Claude Code turns into the transcripts that
// the fake claude in the package's tests plays back. It needs a signed-in
// claude on PATH, and spends a few cents of haiku:
//
//	go run ./internal/provider/claude/record
//
// It drives the real adapter, with itself as the claude binary: in that
// role it runs the real one and tees every line each way into a transcript.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"glitched/internal/api"
	"glitched/internal/provider"
	"glitched/internal/provider/claude"
)

const (
	envReal = "GLITCHED_RECORD_REAL" // the real claude, set when this runs as the tee
	envOut  = "GLITCHED_RECORD_OUT"  // the transcript the tee writes, without .jsonl
)

func main() {
	if real := os.Getenv(envReal); real != "" {
		os.Exit(tee(real, os.Getenv(envOut), os.Args[1:]))
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
	os.Setenv(envReal, real)
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
		os.Setenv(envOut, filepath.Join(out, s.name))
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
		if _, ok := e.(provider.TurnEnded); ok {
			return evs, nil
		}
	}
}

// tee runs the real claude with args and writes what crosses its pipes to
// out.jsonl, or out-resume.jsonl for a run that resumes a session.
func tee(real, out string, args []string) int {
	if hasArg(args, "--resume") {
		out += "-resume"
	}
	f, err := os.Create(out + ".jsonl")
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	wd, _ := os.Getwd()
	home, _ := os.UserHomeDir()
	w := &transcript{f: f, scrub: strings.NewReplacer(wd, "$WORKSPACE", home, "$HOME")}
	w.note("argv", args)

	cmd := claude.Command(context.Background(), real, args...)
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		log.Fatal(err)
	}
	go func() {
		lines(os.Stdin, func(l []byte) {
			w.line("sent", l)
			stdin.Write(l)
		})
		w.note("eof", true)
		stdin.Close()
	}()
	var copies sync.WaitGroup
	copies.Add(2)
	go func() {
		defer copies.Done()
		lines(stdout, func(l []byte) {
			w.line("recv", l)
			os.Stdout.Write(l)
		})
	}()
	go func() {
		defer copies.Done()
		lines(stderr, func(l []byte) {
			w.note("stderr", strings.TrimRight(string(l), "\n"))
			os.Stderr.Write(l)
		})
	}()
	copies.Wait()
	code := 0
	var exit *exec.ExitError
	if err := cmd.Wait(); errors.As(err, &exit) {
		code = exit.ExitCode()
	}
	w.note("exit", code)
	return code
}

func lines(r io.Reader, each func([]byte)) {
	br := bufio.NewReader(r)
	for {
		l, err := br.ReadBytes('\n')
		if len(l) > 0 {
			each(l)
		}
		if err != nil {
			return
		}
	}
}

func hasArg(args []string, a string) bool {
	for _, x := range args {
		if x == a {
			return true
		}
	}
	return false
}

type transcript struct {
	mu    sync.Mutex
	f     *os.File
	scrub *strings.Replacer
}

func (t *transcript) note(kind string, v any) {
	b, _ := json.Marshal(map[string]any{kind: v})
	t.write(b)
}

// line records one JSON line, less what only this machine has: hook
// output, the user's commands and skills, and its paths.
func (t *transcript) line(kind string, l []byte) {
	var m map[string]any
	if json.Unmarshal(l, &m) != nil {
		t.note(kind+"-raw", string(l))
		return
	}
	if slim(m) {
		b, _ := json.Marshal(map[string]any{kind: m})
		t.write(b)
	}
}

func (t *transcript) write(b []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.f.WriteString(t.scrub.Replace(string(b)) + "\n")
}

// slim trims m in place and reports whether to keep it at all.
func slim(m map[string]any) bool {
	switch m["type"] {
	case "system":
		switch m["subtype"] {
		case "hook_started", "hook_response", "hook_progress":
			return false
		case "init":
			for k := range m {
				switch k {
				case "type", "subtype", "cwd", "session_id", "model", "permissionMode", "claude_code_version", "uuid":
				default:
					delete(m, k)
				}
			}
		}
	case "control_response":
		if r, ok := m["response"].(map[string]any); ok {
			if _, ok := r["response"]; ok {
				r["response"] = map[string]any{}
			}
		}
	}
	return true
}

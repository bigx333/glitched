// Package transcript records real Claude Code runs for the fake claude in
// the claude package's tests to replay. A program that the adapter runs
// as its claude becomes a tee: it runs the real one and writes every line
// each way to a transcript.
package transcript

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"

	"glitched/internal/provider/claude"
)

const (
	EnvReal  = "GLITCHED_RECORD_REAL"  // the real claude, set when a program runs as the tee
	EnvOut   = "GLITCHED_RECORD_OUT"   // the transcript the tee writes, without .jsonl
	EnvDupe  = "GLITCHED_RECORD_DUPE"  // set: the tee follows each answer with a contrary one
	EnvStdin = "GLITCHED_CLAUDE_STDIN" // a file the tee, or the fake, appends each line claude got to
)

// Tee runs as the tee when EnvReal says so, and returns its exit code.
func Tee() (code int, teeing bool) {
	real := os.Getenv(EnvReal)
	if real == "" {
		return 0, false
	}
	return tee(real, os.Getenv(EnvOut), os.Args[1:]), true
}

// tee runs the real claude with args and writes what crosses its pipes to
// out.jsonl, or out-resume.jsonl for a run that resumes a session.
func tee(real, out string, args []string) int {
	if slices.Contains(args, "--resume") {
		out += "-resume"
	}
	f, err := os.Create(out + ".jsonl")
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	wd, _ := os.Getwd()
	home, _ := os.UserHomeDir()
	w := &writer{f: f, scrub: strings.NewReplacer(wd, "$WORKSPACE", home, "$HOME")}
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
			Got(l)
			stdin.Write(l)
			if d := contrary(l); d != nil && os.Getenv(EnvDupe) != "" {
				w.line("dupe", d)
				stdin.Write(d)
			}
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

// Got appends a line claude got on stdin to the file EnvStdin names.
func Got(line []byte) {
	if f := os.Getenv(EnvStdin); f != "" {
		out, _ := os.OpenFile(f, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		out.Write(line)
		out.Close()
	}
}

// contrary is a second reply to the prompt that line answers, saying the
// opposite, or nil when line answers no prompt.
func contrary(line []byte) []byte {
	var m struct {
		Type     string
		Response struct {
			RequestID string `json:"request_id"`
			Response  struct{ Behavior string }
		}
	}
	if json.Unmarshal(line, &m) != nil || m.Type != "control_response" || m.Response.Response.Behavior == "" {
		return nil
	}
	decision := map[string]any{"behavior": "deny", "message": "a second, contrary answer"}
	if m.Response.Response.Behavior == "deny" {
		decision = map[string]any{"behavior": "allow", "updatedInput": map[string]any{}}
	}
	b, _ := json.Marshal(map[string]any{"type": "control_response",
		"response": map[string]any{"subtype": "success", "request_id": m.Response.RequestID, "response": decision}})
	return append(b, '\n')
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

type writer struct {
	mu    sync.Mutex
	f     *os.File
	scrub *strings.Replacer
}

func (t *writer) note(kind string, v any) {
	b, _ := json.Marshal(map[string]any{kind: v})
	t.write(b)
}

// line records one JSON line, less what only this machine has: hook
// output, the user's commands and skills, and its paths.
func (t *writer) line(kind string, l []byte) {
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

func (t *writer) write(b []byte) {
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
				case "type", "subtype", "cwd", "session_id", "model", "permissionMode", "claude_code_version", "uuid", "capabilities":
				default:
					delete(m, k)
				}
			}
		}
	case "control_response":
		if r, ok := m["response"].(map[string]any); ok {
			if _, ok := r["response"]; ok && r["request_id"] == "glitched-init" {
				r["response"] = map[string]any{}
			}
		}
	}
	return true
}

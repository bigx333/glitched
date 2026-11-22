//go:build claudelive && linux

package claude_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/provider/claude"
	"glitched/internal/provider/claude/transcript"
	"glitched/internal/runtime"
)

// script writes an executable shell script to dir, since Claude Code's
// Bash tool refuses a long sleep it can see.
func script(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// TestLiveStopSteerAndCrash runs Stop, a steer and a killed engine against
// the signed-in claude on PATH, with haiku in scratch directories under
// /tmp. With -record, the stop and steer transcripts go to testdata.
func TestLiveStopSteerAndCrash(t *testing.T) {
	real, err := exec.LookPath("claude")
	if err != nil {
		t.Fatal(err)
	}
	if v, _, _ := strings.Cut(strings.TrimSpace(string(must(claude.Command(ctx, real, "--version").Output()))), " "); v != shippedCLI {
		t.Fatalf("claude on PATH is %s, want %s", v, shippedCLI)
	}
	tapes := t.TempDir()
	if *record {
		tapes = must(filepath.Abs("testdata"))
	}
	scratch := func(t *testing.T) string {
		ws := must(os.MkdirTemp("/tmp", "glitched-live-"))
		t.Cleanup(func() { os.RemoveAll(ws) })
		return ws
	}

	t.Run("stop", func(t *testing.T) {
		t.Setenv(transcript.EnvReal, real)
		t.Setenv(transcript.EnvOut, filepath.Join(tapes, "interrupt"))
		ws := scratch(t)
		script(t, ws, "busy.sh", "sleep 30\necho finished")
		h := host(t, nil)
		thread := threadIn(t, h, ws)
		got, _ := during(t, h, thread, "Run ./busy.sh with the Bash tool. Then reply done.", toolRunning, func() {
			time.Sleep(time.Second)
			submit(t, h, thread, api.TurnInterrupt{})
		})
		checkStopped(t, got, thread)
	})

	t.Run("stop at a prompt", func(t *testing.T) {
		ws := scratch(t)
		h := host(t, &claude.Adapter{})
		thread := api.NewID()
		submit(t, h, thread, api.ThreadCreate{Workspace: ws, Title: "ask", Access: api.AccessAsk, Model: api.ModelRef{Provider: "claude", Model: "haiku"}})
		got := playOn(t, h, thread, "Use the Write tool to create x.txt containing x. Then reply done.", func(requestLog) bool { return true })
		if got.ended.Outcome != api.OutcomeInterrupted || len(got.requests) != 1 || got.requests[0].state != api.RequestInvalidated {
			t.Errorf("the turn ended %+v with requests %+v, want it interrupted and its prompt void", got.ended, got.requests)
		}
		if _, err := os.Stat(filepath.Join(ws, "x.txt")); err == nil {
			t.Error("x.txt exists, though the turn stopped at its prompt")
		}
	})

	t.Run("steer", func(t *testing.T) {
		t.Setenv(transcript.EnvReal, real)
		t.Setenv(transcript.EnvOut, filepath.Join(tapes, "steer"))
		ws := scratch(t)
		script(t, ws, "slow.sh", "sleep 8\necho one")
		h := host(t, nil)
		thread := threadIn(t, h, ws)
		got, _ := during(t, h, thread, "Run ./slow.sh with the Bash tool. Then tell me what it printed.", toolRunning, func() {
			submit(t, h, thread, api.MessageDispatch{Text: "Also, after that, say the word BANANA.", Mode: api.SteerActive})
		})
		checkSteered(t, got)
	})

	t.Run("stop with a steer queued", func(t *testing.T) {
		tape := filepath.Join(t.TempDir(), "steer-stop")
		t.Setenv(transcript.EnvReal, real)
		t.Setenv(transcript.EnvOut, tape)
		ws := scratch(t)
		script(t, ws, "busy.sh", "sleep 30\necho finished")
		h := host(t, nil)
		thread := threadIn(t, h, ws)
		steer := api.NewID()
		got, _ := during(t, h, thread, "Run ./busy.sh with the Bash tool.", toolRunning, func() {
			if _, err := h.Submit(ctx, api.Command{ID: steer, Target: thread,
				Body: api.MessageDispatch{Text: "After that, run this exact Bash command: touch steer-ran", Mode: api.SteerActive}}); err != nil {
				t.Fatal(err)
			}
			for !dispatched(t, h, thread, steer) {
				time.Sleep(5 * time.Millisecond)
			}
			time.Sleep(time.Second)
			submit(t, h, thread, api.TurnInterrupt{})
		})
		checkStopped(t, got, thread)
		time.Sleep(5 * time.Second)
		if _, err := os.Stat(filepath.Join(ws, "steer-ran")); err == nil {
			t.Error("claude ran the steer's tool after the stopped turn ended")
		}
		steerID, queued := steerOf(t, tape+".jsonl")
		if len(queued) > 0 {
			t.Errorf("the interrupt left %q queued", queued)
		}
		if after := afterInterrupt(t, sessionFile(t, ws, thread), steerID, "touch steer-ran"); len(after) > 0 {
			t.Errorf("claude went on after the interrupt:\n%s", strings.Join(after, "\n"))
		}
	})

	t.Run("crash", func(t *testing.T) {
		ws := scratch(t)
		script(t, ws, "busy.sh", "sleep 60\necho finished")
		path := filepath.Join(t.TempDir(), "host.db")
		thread, _ := queue(t, path, ws, "Remember the word PAPAYA. Then run ./busy.sh with the Bash tool.")
		killedEngine(t, path, func() bool { return running(thread, "busy.sh") })
		if !noneLeft(thread, 6*time.Second) {
			t.Fatalf("processes %v of the run outlived the engine", marked(thread))
		}
		h := hostAt(t, path, &claude.Adapter{})
		if got := runOf(t, h, thread, uncertain); got != api.RunUncertain {
			t.Fatalf("the run is %v after the restart, want uncertain", got)
		}
		submit(t, h, thread, api.TurnInterrupt{})
		got := turn(t, h, thread, "Which word did I ask you to remember? Reply with only the word.")
		if got.ended.Outcome != api.OutcomeCompleted || !strings.Contains(strings.ToUpper(got.text), "PAPAYA") {
			t.Errorf("the next turn %+v, want it to resume the crashed run's session", got)
		}
	})
}

// running is whether one of run's processes runs name.
func running(thread api.ID, name string) bool {
	for _, pid := range marked(thread) {
		cmd, _ := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
		if strings.Contains(string(cmd), name) {
			return true
		}
	}
	return false
}

// dispatched is whether the thread's log holds message as handed to claude.
func dispatched(t *testing.T, h *runtime.Host, thread, message api.ID) bool {
	t.Helper()
	snap := must(h.Read(ctx, api.Query{Thread: thread}))
	for _, e := range snap.Events {
		if d, ok := must(e.Decode()).(api.MessageDispatched); ok && d.Message == message {
			return true
		}
	}
	return false
}

// steerOf is, from the transcript at tape, the uuid of the second user
// message sent, the steer, and what the interrupt's receipt left queued.
func steerOf(t *testing.T, tape string) (steer string, queued []string) {
	t.Helper()
	users := 0
	for _, l := range strings.Split(strings.TrimSpace(string(must(os.ReadFile(tape)))), "\n") {
		var e struct {
			Sent *struct{ Type, UUID string }
			Recv *struct {
				Type     string
				Response struct {
					RequestID string `json:"request_id"`
					Response  struct {
						StillQueued []string `json:"still_queued"`
					}
				}
			}
		}
		json.Unmarshal([]byte(l), &e)
		switch {
		case e.Sent != nil && e.Sent.Type == "user":
			if users++; users == 2 {
				steer = e.Sent.UUID
			}
		case e.Recv != nil && e.Recv.Type == "control_response" && e.Recv.Response.RequestID == "glitched-interrupt":
			queued = e.Recv.Response.Response.StillQueued
		}
	}
	if steer == "" {
		t.Fatalf("the transcript at %s holds no steer", tape)
	}
	return steer, queued
}

// sessionFile is where Claude Code keeps the session of a thread run in
// workspace ws.
func sessionFile(t *testing.T, ws string, thread api.ID) string {
	t.Helper()
	dir := os.Getenv("CLAUDE_CONFIG_DIR")
	if dir == "" {
		dir = filepath.Join(must(os.UserHomeDir()), ".claude")
	}
	slug := regexp.MustCompile(`[^A-Za-z0-9]`).ReplaceAllString(ws, "-")
	return filepath.Join(dir, "projects", slug, claude.SessionOf(thread)+".jsonl")
}

// afterInterrupt is what the session file at path says Claude Code did
// after it wrote the interrupt: took a queued message, called the model,
// or kept the steer, by its uuid or its text, on the session.
func afterInterrupt(t *testing.T, path, steer, text string) []string {
	t.Helper()
	var did []string
	stopped := false
	for _, l := range strings.Split(strings.TrimSpace(string(must(os.ReadFile(path)))), "\n") {
		var e struct {
			Type, Operation, UUID string
			UserMessageUUIDs      []string `json:"user_message_uuids"`
			Message               struct{ Content json.RawMessage }
		}
		json.Unmarshal([]byte(l), &e)
		content := string(e.Message.Content)
		switch {
		case e.Type == "user" && strings.Contains(content, "[Request interrupted by user"):
			stopped = true
		case !stopped:
		case e.Type == "queue-operation" && e.Operation == "dequeue",
			e.Type == "api-request",
			e.Type == "user" && (e.UUID == steer || strings.Contains(content, text)),
			e.Type == "system" && slices.Contains(e.UserMessageUUIDs, steer):
			did = append(did, e.Type+" "+e.Operation+" "+e.UUID)
		}
	}
	if !stopped {
		t.Errorf("the session at %s holds no interrupt", path)
	}
	return did
}

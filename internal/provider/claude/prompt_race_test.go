package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"glitched/internal/provider"
)

// Clients answer a prompt while Claude Code cancels it and ends the turn:
// Claude Code gets at most one reply, and an answer says it went only
// when one did.
func TestAnswersRacingACancel(t *testing.T) {
	ctx := context.Background()
	ws := t.TempDir()
	for range 300 {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		sent := make(chan []byte)
		go func() {
			b, _ := io.ReadAll(r)
			r.Close()
			sent <- b
		}()
		g := &Agent{p: &proc{stdin: w, lines: make(chan []byte, 2)}, turn: newTurn(ws), prompts: map[string]*prompt{}}
		g.p.lines <- []byte(`{"type":"control_request","request_id":"ask","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{"command":"ls"}}}`)
		if _, err := g.Next(ctx); err != nil {
			t.Fatal(err)
		}
		gate := make(chan struct{})
		errs := make([]error, 8)
		var wg sync.WaitGroup
		for i := range errs {
			wg.Go(func() {
				<-gate
				errs[i] = g.Do(ctx, provider.Respond{Request: "ask", Answer: json.RawMessage(`{"allow":true}`)})
			})
		}
		wg.Go(func() {
			<-gate
			g.p.lines <- []byte(`{"type":"control_cancel_request","request_id":"ask"}`)
			g.p.lines <- []byte(`{"type":"result","subtype":"success"}`)
			if _, err := g.Next(ctx); err != nil {
				t.Error(err)
			}
		})
		close(gate)
		wg.Wait()
		w.Close()
		replies := bytes.Count(<-sent, []byte(`"control_response"`))
		went := 0
		for _, err := range errs {
			if err == nil {
				went++
			}
		}
		if replies > 1 || (went > 0) != (replies == 1) {
			t.Fatalf("%d answers said they went and Claude Code got %d replies", went, replies)
		}
	}
}

// A client answers a prompt while Claude Code asks questions the card
// can't show: the answer and the deny that refuses the questions both
// reach Claude Code, and neither waits on the other for good.
func TestAnsweringAPromptWhileQuestionsAreRefusedDoesNotHang(t *testing.T) {
	ctx := context.Background()
	ws := t.TempDir()
	var misfit message
	if err := json.Unmarshal([]byte(`{"type":"control_request","request_id":"questions","request":{"subtype":"can_use_tool","tool_name":"AskUserQuestion","input":{"questions":[]}}}`), &misfit); err != nil {
		t.Fatal(err)
	}
	for i := range 2000 {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		sent := make(chan []byte)
		go func() {
			b, _ := io.ReadAll(r)
			r.Close()
			sent <- b
		}()
		g := &Agent{p: &proc{stdin: w, lines: make(chan []byte, 1)}, turn: newTurn(ws), prompts: map[string]*prompt{}}
		g.p.lines <- []byte(`{"type":"control_request","request_id":"ask","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{"command":"ls"}}}`)
		if _, err := g.Next(ctx); err != nil {
			t.Fatal(err)
		}
		gate := make(chan struct{})
		var answer error
		var wg sync.WaitGroup
		wg.Go(func() {
			<-gate
			answer = g.Do(ctx, provider.Respond{Request: "ask", Answer: json.RawMessage(`{"allow":true}`)})
		})
		wg.Go(func() {
			<-gate
			g.read(ctx, misfit)
		})
		close(gate)
		finished := make(chan struct{})
		go func() {
			wg.Wait()
			close(finished)
		}()
		select {
		case <-finished:
		case <-time.After(5 * time.Second):
			b := make([]byte, 1<<20)
			b = b[:runtime.Stack(b, true)]
			var stuck []string
			for _, s := range strings.Split(string(b), "\n\n") {
				if strings.Contains(s, "claude.(*Agent)") {
					stuck = append(stuck, s)
				}
			}
			t.Fatalf("iteration %d: the answer and the deny still wait on each other\n%s", i, strings.Join(stuck, "\n\n"))
		}
		w.Close()
		got := <-sent
		if answer != nil || bytes.Count(got, []byte(`"control_response"`)) != 2 ||
			!bytes.Contains(got, []byte(`"behavior":"allow"`)) || !bytes.Contains(got, []byte(`"behavior":"deny"`)) {
			t.Fatalf("iteration %d: the answer said %v and Claude Code got %s, want the allow and the deny", i, answer, got)
		}
	}
}

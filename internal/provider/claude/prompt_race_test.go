package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"sync"
	"testing"

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
		g := &Agent{stdin: w, turn: newTurn(ws), prompts: map[string]*prompt{}, lines: make(chan []byte, 2)}
		g.lines <- []byte(`{"type":"control_request","request_id":"ask","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{"command":"ls"}}}`)
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
			g.lines <- []byte(`{"type":"control_cancel_request","request_id":"ask"}`)
			g.lines <- []byte(`{"type":"result","subtype":"success"}`)
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

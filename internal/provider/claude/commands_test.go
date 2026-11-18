package claude

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

// printed is the read end of a pipe claude printed s to, and the write
// end, which the test closes as claude exiting would.
func printed(t *testing.T, s string) (out, claude *os.File) {
	t.Helper()
	claude, out, err := pipe(false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { out.Close(); claude.Close() })
	go claude.WriteString(s)
	return out, claude
}

func TestReadCommandsFindsTheInitializeReply(t *testing.T) {
	out, _ := printed(t, `{"type":"system","subtype":"hook_started"}
{"type":"control_response","response":{"subtype":"success","request_id":"other","response":{"commands":[{"name":"wrong"}]}}}
{"type":"control_response","response":{"subtype":"success","request_id":"`+initRequest+`","response":{"commands":[{"name":"pstack:poteto-mode","description":"Poteto mode","argumentHint":"<task>"},{"name":"__remote-workflow"},{"name":"review","description":"Review a PR"}]}}}
`)
	got, err := readCommands(context.Background(), out)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Name != "pstack:poteto-mode" || got[0].ArgumentHint != "<task>" || got[1].Description != "Review a PR" {
		t.Fatalf("commands = %+v", got)
	}
}

func TestReadCommandsSaysWhenClaudeExitsFirst(t *testing.T) {
	out, claude := printed(t, `{"type":"system"}`+"\n")
	time.Sleep(50 * time.Millisecond)
	claude.Close()
	if _, err := readCommands(context.Background(), out); err == nil {
		t.Fatal("no reply read as no error")
	}
}

// A process claude's hooks started may hold its stdout open past claude:
// the read still ends with the listing's time.
func TestReadCommandsEndsWithItsContextWhileStdoutStaysOpen(t *testing.T) {
	out, _ := printed(t, `{"type":"system"}`+"\n")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := readCommands(ctx, out)
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("err = %v, want the listing's deadline", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the read outlived its context while stdout stayed open")
	}
}

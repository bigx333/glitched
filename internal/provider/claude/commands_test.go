package claude

import (
	"strings"
	"testing"
)

func TestReadCommandsFindsTheInitializeReply(t *testing.T) {
	out := `{"type":"system","subtype":"hook_started"}
{"type":"control_response","response":{"subtype":"success","request_id":"other","response":{"commands":[{"name":"wrong"}]}}}
{"type":"control_response","response":{"subtype":"success","request_id":"` + initRequest + `","response":{"commands":[{"name":"pstack:poteto-mode","description":"Poteto mode","argumentHint":"<task>"},{"name":"review","description":"Review a PR"}]}}}
`
	got, err := readCommands(strings.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Name != "pstack:poteto-mode" || got[0].ArgumentHint != "<task>" || got[1].Description != "Review a PR" {
		t.Fatalf("commands = %+v", got)
	}
}

func TestReadCommandsSaysWhenClaudeExitsFirst(t *testing.T) {
	if _, err := readCommands(strings.NewReader(`{"type":"system"}` + "\n")); err == nil {
		t.Fatal("no reply read as no error")
	}
}

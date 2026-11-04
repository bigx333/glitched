package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/provider"
	"glitched/internal/provider/fake"
)

// The [y/N] prompt prints an agent's request in printable ASCII, with
// every other character as an escape such as \u{202E}, and says so: a
// terminal draws bidi controls, C1 sequences and right-to-left letters,
// so it could show one command while y allows another.
func TestRunPromptPrintsOnlyASCII(t *testing.T) {
	command := "echo safe ‮⁦; rm -rf ~/work⁩⁦#⁩‬ \u009b2K\x1b[1A א ./check️"
	body, _ := json.Marshal(map[string]string{"command": command, "cwd": "/w"})
	dir, ep := engine(t, fake.New([]provider.Event{provider.RequestOpened{ID: "ask", Kind: "approval", Body: body}}))
	thread := newThread(t, ep)

	in, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	w.WriteString("y\n")
	w.Close()
	out, err := os.Create(filepath.Join(t.TempDir(), "out"))
	if err != nil {
		t.Fatal(err)
	}
	stdin, stdout := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = in, out
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = run(ctx, []string{"-dir", dir, "-thread", thread.String(), "-mode", "now", "go"})
	os.Stdin, os.Stdout = stdin, stdout
	in.Close()
	out.Close()
	if err != nil {
		t.Fatal(err)
	}
	printed, err := os.ReadFile(out.Name())
	if err != nil {
		t.Fatal(err)
	}

	for i, c := range printed {
		if (c < ' ' || c > '~') && c != '\n' {
			t.Errorf("glitched run prints byte %#x as it is, at %d of %q", c, i, printed)
		}
	}
	for _, want := range []string{`echo safe \u{202E}\u{2066}; rm -rf ~/work\u{2069}\u{2066}#\u{2069}\u{202C} \u{9B}2K\u001b[1A \u{5D0} ./check\u{FE0F}`, `shown as \u{...}`, "[y/N]"} {
		if !strings.Contains(string(printed), want) {
			t.Errorf("glitched run prints %q, want %q in it", printed, want)
		}
	}

	snap, err := ep.Read(ctx, api.Query{Thread: thread})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range snap.Events {
		b, err := e.Decode()
		if err != nil {
			t.Fatal(err)
		}
		if r, ok := b.(api.RequestAnswer); ok && string(r.Answer) == `{"allow":true}` {
			return
		}
	}
	t.Error("y did not allow the request")
}

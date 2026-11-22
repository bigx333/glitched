//go:build claudelive

package claude_test

import (
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/provider/claude"
)

// TestLiveKeepsClaudeAcrossTurns runs three haiku turns of one thread on
// the signed-in claude on PATH: the second delegates a child, the third
// recalls the first. All three run on one claude process.
func TestLiveKeepsClaudeAcrossTurns(t *testing.T) {
	h := delegateHost(t, &claude.Adapter{})
	ws := must(os.MkdirTemp("/tmp", "glitched-keep-"))
	t.Cleanup(func() { os.RemoveAll(ws) })
	thread := api.NewID()
	submit(t, h, thread, api.ThreadCreate{Workspace: ws, Title: "keep", Access: api.AccessAsk,
		Model: api.ModelRef{Provider: "claude", Model: "haiku"}})
	pids := func() []int {
		out, _ := exec.Command("ps", "-axo", "pid=,args=").Output()
		var found []int
		for _, l := range strings.Split(string(out), "\n") {
			if f := strings.Fields(l); len(f) > 1 && strings.Contains(l, claude.SessionOf(thread)) && strings.HasSuffix(f[1], "claude") {
				found = append(found, must(strconv.Atoi(f[0])))
			}
		}
		return found
	}
	first := turn(t, h, thread, "Remember the word kumquat. Reply with only: ok")
	kept := pids()
	rss, _ := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(kept[0])).Output()
	t.Logf("turn 1: %+v; claude %v parked at %s KB", first.ended, kept, strings.TrimSpace(string(rss)))
	if first.ended.Outcome != api.OutcomeCompleted || len(kept) != 1 {
		t.Fatalf("turn 1 ended %+v with claudes %v, want completed and one claude kept", first.ended, kept)
	}
	second := turn(t, h, thread, delegatePrompt)
	kids := childrenOf(t, h, thread)
	t.Logf("turn 2: %+v; children %d; claudes %v", second.ended, len(kids), pids())
	if second.ended.Outcome != api.OutcomeCompleted || len(kids) != 1 || !slices.Equal(pids(), kept) {
		t.Errorf("turn 2 ended %+v with %d children on claudes %v, want completed, one child, on claude %v", second.ended, len(kids), pids(), kept)
	}
	// The child's report comes back as a message of its own, which starts
	// a turn on the same claude; the third turn waits it out.
	for deadline := time.Now().Add(time.Minute); ; time.Sleep(50 * time.Millisecond) {
		snap := must(h.Read(ctx, api.Query{Thread: thread}))
		if i := slices.IndexFunc(snap.Threads, func(th api.Thread) bool { return th.ID == thread }); i >= 0 && snap.Threads[i].Run == api.RunIdle && !snap.Threads[i].Reported.IsZero() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the child's report never came back and settled")
		}
	}
	third := turn(t, h, thread, "Which word did I ask you to remember? Reply with only the word.")
	t.Logf("turn 3: %+v %q; claudes %v", third.ended, third.text, pids())
	if third.ended.Outcome != api.OutcomeCompleted || !strings.Contains(strings.ToLower(third.text), "kumquat") || !slices.Equal(pids(), kept) {
		t.Errorf("turn 3 ended %+v saying %q on claudes %v, want the word, on claude %v", third.ended, third.text, pids(), kept)
	}
	for i, l := range []promptLog{first, second, third} {
		if l.ended.Tokens == 0 {
			t.Errorf("turn %d counted no tokens", i+1)
		}
	}
}

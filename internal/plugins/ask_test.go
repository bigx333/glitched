package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"glitched/internal/provider"
)

// askModule asks once for each prompt of its page's init, and shows each
// answer or err.code.
func askModule(asks string) string {
	return `
export async function init() {
	const out = []
	for (const [prompt, options] of ` + asks + `) {
		try {
			const a = await glitched.models.ask(prompt, options)
			out.push(a.text + " by " + a.provider + ":" + a.model)
		} catch (e) { out.push(e.code + ": " + e.message) }
	}
	return { state: out }
}
export function view({ state }) { return { body: { type: "text", text: state.join(" | ") } } }`
}

// asker is Options.Ask, recording what it was asked.
type asker struct {
	mu     sync.Mutex
	asked  []string
	answer func(AskRequest) (AskAnswer, error)
}

func (a *asker) ask(_ context.Context, id string, q AskRequest) (AskAnswer, error) {
	a.mu.Lock()
	a.asked = append(a.asked, fmt.Sprintf("%s %q %q %q %q", id, q.Prompt, q.System, q.Provider, q.Model))
	a.mu.Unlock()
	if a.answer != nil {
		return a.answer(q)
	}
	return AskAnswer{Text: "re " + q.Prompt, Provider: "claude", Model: "claude-haiku-5-5"}, nil
}

func TestAnAskNeedsModelsAskDeclaredAndGranted(t *testing.T) {
	state, u, fx := t.TempDir(), newUIThread(), &effects{}
	writeModule(t, state, "writer", `"capabilities": ["models:ask"], `, askModule(`[["Title it", { system: "One line.", model: "claude-sonnet-5-5" }]]`))
	writeModule(t, state, "plain", "", askModule(`[["Title it"]]`))
	q := &asker{}
	h := Open(state, Options{Post: u.post, Do: fx.do, Logf: t.Logf, Ask: q.ask})
	t.Cleanup(h.Close)

	plain := session(t, h, "plain")
	u.until(t, "the undeclared ask refused", func() bool { return strings.HasPrefix(texts(plain), "denied: ") })
	w := session(t, h, "writer")
	u.until(t, "the ungranted ask refused", func() bool { return strings.HasPrefix(texts(w), "denied: ") })
	if len(q.asked) != 0 {
		t.Fatalf("asked %q before any grant", q.asked)
	}
	if err := h.SetGranted("writer", "models:ask", true); err != nil {
		t.Fatal(err)
	}
	w.Retry()
	u.until(t, "the answer", func() bool { return texts(w) == "re Title it by claude:claude-haiku-5-5||" })
	if want := `writer "Title it" "One line." "" "claude-sonnet-5-5"`; len(q.asked) != 1 || q.asked[0] != want {
		t.Fatalf("asked %q, want %s", q.asked, want)
	}
}

func TestAnAskIsBoundedAndSaysWhyNot(t *testing.T) {
	state, u, fx := t.TempDir(), newUIThread(), &effects{}
	writeModule(t, state, "writer", `"capabilities": ["models:ask"], `, askModule(`[
		["x".repeat(200 * 1024 + 1)],
		["Title it", { prompt_: "typo" }],
		["unsupported"],
		["unavailable"],
		["broken"],
	]`))
	q := &asker{answer: func(r AskRequest) (AskAnswer, error) {
		switch r.Prompt {
		case "unsupported":
			return AskAnswer{}, fmt.Errorf("%w: acp can't answer a prompt on its own", provider.ErrUnsupported)
		case "unavailable":
			return AskAnswer{}, fmt.Errorf("%w: Claude Code is not installed", provider.ErrUnavailable)
		}
		return AskAnswer{}, errors.New("claude: Not logged in")
	}}
	h := Open(state, Options{Post: u.post, Do: fx.do, Logf: t.Logf, Ask: q.ask})
	t.Cleanup(h.Close)
	h.SetGranted("writer", "models:ask", true)
	w := session(t, h, "writer")
	u.until(t, "every ask answered", func() bool { return strings.Count(texts(w), " | ") == 4 })
	got := strings.Split(texts(w), " | ")
	for i, want := range []string{"too_large: ", "undefined: glitched.models.ask takes a prompt", "unsupported: ", "unavailable: ", "undefined: claude: Not logged in"} {
		if !strings.HasPrefix(got[i], want) {
			t.Errorf("ask %d: %q, want %q…", i, got[i], want)
		}
	}
	if len(q.asked) != 3 {
		t.Errorf("asked %q; the oversized and the misspelt asks reached the model", q.asked)
	}

	state, u = t.TempDir(), newUIThread()
	writeModule(t, state, "writer", `"capabilities": ["models:ask"], `, askModule(`[["Title it"]]`))
	h = Open(state, Options{Post: u.post, Do: fx.do, Logf: t.Logf})
	t.Cleanup(h.Close)
	h.SetGranted("writer", "models:ask", true)
	w = session(t, h, "writer")
	u.until(t, "no model here", func() bool { return strings.HasPrefix(texts(w), "unavailable: ") })
}

// An ask waits the seconds its timeout option gives, AskTimeout at most
// and without one, and then fails with the code timeout.
func TestAnAskWaitsItsTimeout(t *testing.T) {
	state, u, fx := t.TempDir(), newUIThread(), &effects{}
	writeModule(t, state, "writer", `"capabilities": ["models:ask"], `, askModule(`[["short", { timeout: 5 }], ["long", { timeout: 500 }], ["plain"], ["late", { timeout: 0.2 }]]`))
	var mu sync.Mutex
	waits := map[string]time.Duration{}
	h := Open(state, Options{Post: u.post, Do: fx.do, Logf: t.Logf, Ask: func(ctx context.Context, _ string, q AskRequest) (AskAnswer, error) {
		d, _ := ctx.Deadline()
		mu.Lock()
		waits[q.Prompt] = time.Until(d)
		mu.Unlock()
		if q.Prompt == "late" {
			<-ctx.Done()
			return AskAnswer{}, ctx.Err()
		}
		return AskAnswer{Text: q.Prompt}, nil
	}})
	t.Cleanup(h.Close)
	h.SetGranted("writer", "models:ask", true)
	w := session(t, h, "writer")
	u.until(t, "every ask answered", func() bool { return strings.Count(texts(w), " | ") == 3 })
	if got := strings.Split(texts(w), " | ")[3]; !strings.HasPrefix(got, "timeout: the model didn't answer within 200ms") {
		t.Errorf("the late ask: %q", got)
	}
	mu.Lock()
	defer mu.Unlock()
	for prompt, want := range map[string]time.Duration{"short": 5 * time.Second, "long": AskTimeout, "plain": AskTimeout} {
		if got := waits[prompt]; got > want || got < want-time.Second {
			t.Errorf("the %s ask may wait %s, want %s", prompt, got, want)
		}
	}
}

// A plugin's second ask, while its first is under way, is refused.
func TestAPluginAsksOneAtATime(t *testing.T) {
	state, u, fx := t.TempDir(), newUIThread(), &effects{}
	writeModule(t, state, "writer", `"capabilities": ["models:ask"], `, `
export async function init() {
	const out = await Promise.allSettled([glitched.models.ask("one"), glitched.models.ask("two")])
	return { state: out.map((r) => r.status === "fulfilled" ? r.value.text : r.reason.code) }
}
export function view({ state }) { return { body: { type: "text", text: state.sort().join(" | ") } } }`)
	release := make(chan struct{})
	q := &asker{answer: func(r AskRequest) (AskAnswer, error) {
		<-release
		return AskAnswer{Text: r.Prompt}, nil
	}}
	h := Open(state, Options{Post: u.post, Do: fx.do, Logf: t.Logf, Ask: q.ask})
	t.Cleanup(h.Close)
	h.SetGranted("writer", "models:ask", true)
	w := session(t, h, "writer")
	deadline := time.Now().Add(10 * time.Second)
	for {
		q.mu.Lock()
		n := len(q.asked)
		q.mu.Unlock()
		if n == 1 || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	close(release)
	u.until(t, "both settled", func() bool { return strings.Contains(texts(w), " | ") })
	if got := strings.TrimSuffix(texts(w), "||"); got != "busy | one" && got != "busy | two" {
		t.Fatalf("two asks at once: %q, want one answered and one busy", got)
	}
}

// A record says its workspace is local only to a plugin with threads:read.
func TestARecordSaysItsWorkspaceIsLocal(t *testing.T) {
	state, u, fx := t.TempDir(), newUIThread(), &effects{}
	writeModule(t, state, "reader", `"capabilities": ["threads:read"], `, `export function init() { return {} }`)
	h := Open(state, Options{Post: u.post, Do: fx.do, Logf: t.Logf})
	t.Cleanup(h.Close)
	full := ThreadRecord{ID: "t1", Workspace: "/w", Local: true}
	if b, _ := json.Marshal(h.Record("reader", full)); string(b) != `{"id":"t1"}` {
		t.Errorf("without threads:read: %s", b)
	}
	h.SetGranted("reader", "threads:read", true)
	if b, _ := json.Marshal(h.Record("reader", full)); string(b) != `{"id":"t1","workspace":"/w","local":true}` {
		t.Errorf("with threads:read: %s", b)
	}
	if b, _ := json.Marshal(ThreadRecord{ID: "t2"}); strings.Contains(string(b), "local") {
		t.Errorf("a remote thread's record: %s, want local left out", b)
	}
}

// A plugin that hears place.shown is told of each copy that comes, and
// again as one it shows changes its context; one that hears place.hidden
// is told of each that goes.
func TestPlaceShownTellsOfEachCopyThatComes(t *testing.T) {
	state, u, fx := t.TempDir(), newUIThread(), &effects{}
	writeModule(t, state, "shown", `"capabilities": ["ui.place"], "glitched": { "places": [{ "id": "badge", "at": "thread.header" }], "events": ["place.shown", "place.hidden"] }, `, `
export function init() { return { state: [] } }
export function place({ state, items }) { return items.map(() => ({ type: "badge", text: state.join(",") })) }
export function update({ state, event }) {
	const d = event.data
	if (event.type === "place.hidden") return { state: [...state, "-" + d.key] }
	if (event.type !== "place.shown") return {}
	return { state: [...state, d.id + "@" + d.at + ":" + d.key + "=" + d.context.title] }
}`)
	h := Open(state, Options{Post: u.post, Do: fx.do, Logf: t.Logf})
	t.Cleanup(h.Close)
	s := h.App("shown")
	text := func(key string) string {
		if n := s.PlaceView("badge", key); n != nil {
			return n.Text
		}
		return ""
	}
	s.SetItems("badge", []PlaceItem{{Key: "t1", Version: "1", Context: map[string]string{"title": "One"}}})
	u.until(t, "t1 shown", func() bool { return text("t1") == "badge@thread.header:t1=One" })
	s.SetItems("badge", []PlaceItem{{Key: "t1", Version: "2", Context: map[string]string{"title": "Uno"}}})
	s.SetItems("badge", []PlaceItem{{Key: "t2", Version: "1", Context: map[string]string{"title": "Two"}}})
	u.until(t, "t2 shown", func() bool {
		return text("t2") == "badge@thread.header:t1=One,badge@thread.header:t1=Uno,-t1,badge@thread.header:t2=Two"
	})
}

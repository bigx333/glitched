package main

import (
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/daemon"
	"glitched/internal/provider"
	"glitched/internal/provider/fake"
)

// metered is a thread whose one turn said of its context what reports
// do, shown, with the built-ins settled. With METERSHOTS set, the window
// is written there as name.png.
func metered(t *testing.T, name string, reports ...provider.Context) *harness {
	t.Helper()
	dir := tempDir(t)
	script := []provider.Event{provider.TextDelta{Text: "Read the tree."}}
	for _, r := range reports {
		script = append(script, r)
	}
	ad := fake.New(script)
	ad.Tokens = 48200
	d := engine(t, daemon.Config{Dir: dir}, ad)
	seed(t, d, "/work/glitched", "Meter the context", "Look around.")
	h := start(t, dir)
	h.a.select_(h.thread("Meter the context"))
	h.until("the turn's context", func() bool { h.tt.Frame(); return h.a.current().View.Context != nil })
	settlePlugins(t, h.a, h.tt, h.p)
	if out := os.Getenv("METERSHOTS"); out != "" {
		f, err := os.Create(filepath.Join(out, name+".png"))
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		png.Encode(f, h.tt.Image())
	}
	return h
}

// hover points at the header's text s and says what shows once the
// tooltip is up.
func hover(t *testing.T, h *harness, s string) []string {
	t.Helper()
	r, ok := h.tt.Find(s)
	if !ok {
		t.Fatalf("no %q in the header: %q", s, h.tt.Texts())
	}
	h.tt.Move(r.X+r.W/2, r.Y+r.H/2)
	h.tt.Advance(2 * time.Second)
	return h.tt.Texts()
}

func has(texts []string, sub string) bool {
	return slices.ContainsFunc(texts, func(s string) bool { return strings.Contains(s, sub) })
}

// The header meters how full the thread's context is, once its agent
// said: the share of the window used, and on hover, the tokens of it and
// what is left, then the thread's tokens the pill showed before.
func TestTheHeaderMetersTheThreadsContext(t *testing.T) {
	h := metered(t, "low", provider.Context{Used: 25041}, provider.Context{Used: 25041, Window: 1000000})
	if by := drawnBy(h, "thread.header.usage"); by != "child-threads" {
		t.Fatalf("the meter is drawn by %s, want the built-in Child threads", by)
	}
	if h.tt.HasText("48.2k tokens") {
		t.Error("the token pill still shows beside the meter")
	}
	texts := hover(t, h, "3%")
	for _, want := range []string{"Context window 25k / 1m tokens, 97% left", "This thread's tokens 48.2k"} {
		if !has(texts, want) {
			t.Errorf("the meter's tooltip doesn't say %q: %q", want, texts)
		}
	}
}

// A compaction leaves what the context holds unknown, so the meter says
// so until the next report; and a provider that gives no window gets
// the tokens in context, with no share of anything.
func TestTheMeterSaysWhatItDoesntKnow(t *testing.T) {
	h := metered(t, "compacted", provider.Context{Used: 25041, Window: 1000000}, provider.Context{Compacted: true})
	if !has(hover(t, h, "Compacted"), "Context compacted. Its size shows after the next reply.") {
		t.Errorf("a compacted context's tooltip: %q", h.tt.Texts())
	}
	h = metered(t, "no-window", provider.Context{Used: 25041})
	if !has(hover(t, h, "25k in context"), "25k tokens in context. Context limit not reported.") {
		t.Errorf("a context with no window's tooltip: %q", h.tt.Texts())
	}
}

// The meter turns amber from 75% and red from 90%, as T3 Code's
// neighbours bb and Zeron do.
func TestTheMeterWarnsNearTheLimit(t *testing.T) {
	for _, c := range []struct {
		used  int64
		text  string
		color string
	}{
		{25041, "3%", ""},
		{744999, "74%", ""},
		{745000, "75%", "$warning"},
		{895000, "90%", "$danger"},
		{1200000, "120%", "$danger"},
	} {
		n := usageBadge(headerUsage{Tokens: 1, Total: 1, Context: &api.Context{Used: c.used, Window: 1000000}})
		if n.Text != c.text || n.Color != c.color {
			t.Errorf("%d of 1m is %q in %q, want %q in %q", c.used, n.Text, n.Color, c.text, c.color)
		}
		if c.used > 1000000 && string(n.Value) != "1" {
			t.Errorf("past the window the ring is %s round, want all of it", n.Value)
		}
	}
	for _, used := range []int64{760000, 905000} {
		metered(t, map[int64]string{760000: "warning", 905000: "danger"}[used], provider.Context{Used: used, Window: 1000000})
	}
}

func TestContextTokensAreWrittenShort(t *testing.T) {
	for n, want := range map[int64]string{
		0: "0", 999: "999", 1000: "1k", 25041: "25k", 32500: "33k", 258400: "258k",
		999499: "999k", 999500: "1m", 1000000: "1m", 1048576: "1m", 1250000: "1.3m", 2000000: "2m",
	} {
		if got := contextTokens(n); got != want {
			t.Errorf("%d is written %q, want %q", n, got, want)
		}
	}
}

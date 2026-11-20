//go:build timelinebench

package main

// PROTOTYPE (GLIT-19): what drawing the timeline's rows through plugins
// costs on long threads, against today's native rows. Not part of CI:
//
//	CGO_ENABLED=0 go test -tags timelinebench -run TestTimelineCost -v -timeout 60m .
//
// TIMELINE_SIZES, TIMELINE_MODES and TIMELINE_ROUNDS narrow or repeat it;
// TIMELINE_OUT names a TSV file for every run's numbers.

import (
	"cmp"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"glitched/internal/api"
	"glitched/internal/client"
	"glitched/internal/daemon"
	"glitched/internal/plugins"
	"glitched/internal/provider/fake"
)

// thinModule embeds the row as Glitched draws it: the planned shape.
const thinModule = `export function init() { return { state: {} } }
export function update() {}
export function place({ items }) {
	return items.map(({ context: m }) => ({ type: "native", name: "transcript.body", props: { key: m.key } }))
}`

// fullModule draws each row with widgets, from the full context.
const fullModule = `export function init() { return { state: {} } }
export function update() {}
function step(s) {
	const kids = [{ type: "icon", name: "check" }, { type: "text", text: s.title }, { type: "spacer" }]
	if (s.exit !== undefined) kids.push({ type: "text", style: s.exit ? "error" : "success", text: (s.exit ? "✗ " : "✓ ") + s.exit })
	return { type: "row", gap: 8, children: kids }
}
function row(m) {
	switch (m.kind) {
	case "user": {
		const bubble = m.markdown ? { type: "markdown", text: m.markdown } : { type: "text", text: m.text }
		const col = [{ type: "column", padding: [10, 14], background: "surface", children: [bubble] }]
		if (m.note) col.push({ type: "text", style: "muted", text: m.note })
		return { type: "row", children: [{ type: "spacer" }, { type: "column", align: "end", gap: 6, children: col }] }
	}
	case "assistant":
		return { type: "markdown", text: m.text }
	case "work":
		return { type: "card", padding: [8, 12], gap: 2, children: [
			{ type: "row", children: [{ type: "text", text: m.took }, { type: "spacer" }, { type: "text", style: "muted", text: m.steps.length + " steps" }] },
			...m.steps.map(step),
		] }
	default:
		return { type: "row", gap: 8, children: [{ type: "text", style: "error", text: m.text }, { type: "button", id: "retry", label: "Retry", onClick: "retry" }] }
	}
}
export function place({ items }) { return items.map(({ context: m }) => row(m)) }`

var benchModes = map[string]struct {
	module      string
	fullContext bool
}{
	"native":   {},
	"thin":     {thinModule, false},
	"thinfull": {thinModule, true},
	"full":     {fullModule, true},
}

const benchPlugin = "glit19-rows"

// setMode installs the plugin a mode draws rows with, or none.
func setMode(t *testing.T, h *harness, mode string) {
	os.RemoveAll(filepath.Join(plugins.Dir(h.a.dir), benchPlugin))
	m := benchModes[mode]
	h.a.timelineFullContext = m.fullContext
	if m.module == "" {
		h.a.plugins.Reload()
		h.a.pluginsChanged(nil)
		return
	}
	writeOwnPlugin(t, h.a, benchPlugin, replacing(benchPlugin, "timeline.message"), m.module)
	if err := h.a.plugins.SetGranted(benchPlugin, "ui.replace", true); err != nil {
		t.Fatal(err)
	}
}

// synthetic is a transcript of n rows: turns of a user message, a run's
// work and the reply, as long threads have them.
func synthetic(n int) []client.Entry {
	var es []client.Entry
	at := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	for i := 0; len(es) < n; i++ {
		run := api.NewID()
		user := fmt.Sprintf("Turn %d: look at chat.go and tell me why the list jumps when a reply streams in.", i)
		if i%5 == 0 {
			user += "\n\n```go\nfor i := range es {\n\tdraw(es[i])\n}\n```"
		}
		es = append(es, &client.UserEntry{Message: api.NewID(), Text: user, State: client.Sent})
		if len(es) == n {
			break
		}
		exit0, exit1 := 0, 1
		steps := []client.WorkStep{
			{Item: "s1", Kind: "command", Title: "go test ./...", Status: api.WorkCompleted, Exit: &exit0, Started: at, Ended: at.Add(2 * time.Second)},
			{Item: "s2", Kind: "edit", Title: "Edited chat.go", Status: api.WorkCompleted, Started: at, Ended: at},
			{Item: "s3", Kind: "search", Title: "Searched for ListState", Status: api.WorkCompleted, Started: at, Ended: at},
		}
		if i%7 == 0 {
			steps = append(steps, client.WorkStep{Item: "s4", Kind: "command", Title: "go vet .", Status: api.WorkCompleted, Exit: &exit1, Output: "chat.go:12: unused variable\nchat.go:40: unreachable code\nexit status 1", Started: at, Ended: at.Add(time.Second)})
		}
		es = append(es, &client.WorkEntry{Run: run, Items: steps, Started: at, Ended: at.Add(40 * time.Second)})
		if len(es) == n {
			break
		}
		es = append(es, &client.AssistantEntry{Run: run, Text: reply(i)})
	}
	return es
}

func reply(i int) string {
	s := fmt.Sprintf("Reply %d. The list keeps its place by key, so a row that changes height above the fold moves everything under it. "+
		"`ListState` measures rows as they come into view and estimates the rest, which is why a long reply that streams in can shift the view.\n\n"+
		"- The row's key stays the same while it streams.\n- Heights above the viewport are estimates until drawn.\n- `FollowEnd` glides only while you are at the end.\n\n"+
		"I changed the anchor to the first visible row and kept the offset inside it, which holds the view still as rows above it settle.", i)
	if i%4 == 0 {
		s += "\n\n```go\nfunc (s *ListState) anchor() (row int, offset float32) {\n\tfirst, _ := s.Visible()\n\treturn first, s.offsetIn(first)\n}\n```"
	}
	return s
}

// cpu is the process's CPU time so far, every thread's.
func cpu() time.Duration {
	var ru syscall.Rusage
	syscall.Getrusage(syscall.RUSAGE_SELF, &ru)
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
}

// sample is one scenario's run: each step's UI-thread time, and what the
// process spent over the whole of it.
type sample struct {
	steps          []time.Duration // each step's UI time: a tick of a 60 Hz window
	frameTimes     []time.Duration // each frame's UI time: build, layout and paint
	frames         int
	cpu, wall      time.Duration
	mallocs, bytes uint64
	gcs            uint32
	blank          int // visible plugin rows with no drawing, summed over steps
	stale          int // steps whose streaming row showed old text
	settle         time.Duration
	visible, moved int
}

func pct(ds []time.Duration, p float64) time.Duration {
	d := slices.Clone(ds)
	sort.Slice(d, func(i, j int) bool { return d[i] < d[j] })
	return d[min(len(d)-1, int(float64(len(d))*p))]
}

type bench struct {
	t           *testing.T
	h           *harness
	starts      *[]time.Time // when each frame's build began
	long, other thread
	mode        string
	staleN      int
}

// measure runs step n times, a frame time apart, as a window would.
func (b *bench) measure(n int, step func(i int)) sample {
	var s sample
	runtime.GC()
	var m0, m1 runtime.MemStats
	runtime.ReadMemStats(&m0)
	c0, w0 := cpu(), time.Now()
	for i := range n {
		*b.starts = (*b.starts)[:0]
		start := time.Now()
		b.h.p.run()
		step(i)
		b.h.tt.Advance(time.Second / 60)
		end := time.Now()
		s.steps = append(s.steps, end.Sub(start))
		// A frame runs from its build to the next one's: the posts run
		// and the input handled before the first are the first's.
		from := start
		for _, at := range append((*b.starts)[min(1, len(*b.starts)):], end) {
			s.frameTimes = append(s.frameTimes, at.Sub(from))
			from = at
		}
		s.blank += b.blankRows()
		time.Sleep(time.Until(start.Add(time.Second / 60)))
	}
	s.wall, s.cpu, s.frames = time.Since(w0), cpu()-c0, len(s.frameTimes)
	runtime.ReadMemStats(&m1)
	s.mallocs, s.bytes, s.gcs = m1.Mallocs-m0.Mallocs, m1.TotalAlloc-m0.TotalAlloc, m1.NumGC-m0.NumGC
	return s
}

// blankRows counts the rows in view a plugin draws that have no drawing.
func (b *bench) blankRows() int {
	if b.mode == "native" {
		return 0
	}
	app := b.h.a.plugins.App(benchPlugin)
	es := entriesOf(b.long)
	first, last := b.h.a.listFor(b.long.id()).Visible()
	n := 0
	for i := first; i <= last && i < len(es); i++ {
		if k := es[i].kind; k == entryRequest || k == entryReport {
			continue
		}
		if app == nil || app.PlaceView("row", es[i].key) == nil {
			n++
		}
	}
	return n
}

func (b *bench) settled() bool {
	return (b.h.a.plugins == nil || b.h.a.plugins.Idle()) && b.blankRows() == 0
}

func (b *bench) settle() {
	deadline := time.Now().Add(20 * time.Second)
	for {
		ran := b.h.p.run()
		b.h.tt.Frame()
		b.h.tt.Advance(time.Second / 4)
		if !ran && b.settled() {
			return
		}
		if time.Now().After(deadline) {
			var why error
			var rows []string
			if app := b.h.a.plugins.App(benchPlugin); app != nil {
				why = app.Err
				es := entriesOf(b.long)
				first, last := b.h.a.listFor(b.long.id()).Visible()
				for i := first; i <= last && i < len(es); i++ {
					rows = append(rows, fmt.Sprintf("%d:%s item=%v view=%v", i, es[i].key, app.PlaceContext("row", es[i].key) != nil, app.PlaceView("row", es[i].key) != nil))
				}
				rows = append(rows, fmt.Sprintf("busy=%v idle=%v", app.Busy, b.h.a.plugins.Idle()))
			}
			b.t.Log(strings.Join(rows, "\n"))
			b.t.Fatalf("%s: never settled: %d blank rows (plugin: %v; %v)", b.mode, b.blankRows(), why, b.h.a.plugins.Failed(benchPlugin))
		}
		time.Sleep(time.Millisecond)
	}
}

// firstOpen opens the thread as if for the first time: no scroll state,
// no cached Markdown, and a plugin that has drawn none of its rows.
func (b *bench) firstOpen(marker string) sample {
	a := b.h.a
	a.select_(b.other)
	b.settle()
	delete(a.lists, b.long.id())
	a.userMDs = nil
	if b.mode != "native" {
		setMode(b.t, b.h, b.mode)
		b.settle()
	}
	opened, done := false, false
	var settledAt time.Duration
	start := time.Now()
	s := b.measure(60, func(i int) {
		if !opened {
			a.select_(b.long)
			opened = true
		}
		b.h.tt.Frame()
		if !done && b.settled() && b.h.tt.HasText(marker) {
			done, settledAt = true, time.Since(start)
		}
	})
	if !done {
		b.t.Fatalf("%s: the thread never showed %q", b.mode, marker)
	}
	s.settle = settledAt
	return s
}

func (b *bench) scroll() sample {
	l := b.h.a.listFor(b.long.id())
	l.ScrollToEnd()
	b.settle()
	from, _ := l.Visible()
	s := b.measure(150, func(i int) {
		b.h.tt.Wheel(800, 400, 0, -90)
		if os.Getenv("TIMELINE_DEBUG") != "" && b.mode != "native" && b.blankRows() > 0 && b.staleN < 4 {
			b.staleN++
			app := b.h.a.plugins.App(benchPlugin)
			es := entriesOf(b.long)
			first, last := l.Visible()
			var rows []string
			for i := first; i <= last && i < len(es); i++ {
				rows = append(rows, fmt.Sprintf("%d:%s item=%v view=%v", i, entryKindNames[es[i].kind], app.PlaceContext("row", es[i].key) != nil, app.PlaceView("row", es[i].key) != nil))
			}
			b.t.Logf("step %d busy=%v: %s", i, app.Busy, strings.Join(rows, " "))
		}
	})
	to, last := l.Visible()
	s.moved, s.visible = from-to, last-to+1
	if s.moved <= 0 {
		b.t.Fatalf("%s: scrolling didn't move the list: %d to %d", b.mode, from, to)
	}
	return s
}

// stream streams a reply at the end, a chunk a frame.
func (b *bench) stream() sample {
	tr := b.long.Transcript
	l := b.h.a.listFor(b.long.id())
	l.ScrollToEnd()
	b.settle()
	keep := len(tr.Entries)
	reply := &client.AssistantEntry{Run: api.NewID(), Streaming: true}
	tr.Entries = append(tr.Entries, reply)
	text := strings.Repeat(reply0, 4)
	const chunk = 24
	s := b.measure(len(text)/chunk, func(i int) {
		reply.Text = text[:(i+1)*chunk]
		b.h.a.changed()
		b.h.tt.Frame()
		if b.mode == "full" {
			if app := b.h.a.plugins.App(benchPlugin); app != nil {
				if n := app.PlaceView("row", "a/"+reply.Run.String()+"/1"); n == nil || n.Text != reply.Text {
					b.staleN++
				}
			}
		}
	})
	s.stale, b.staleN = b.staleN, 0
	if !l.AtEnd() {
		b.t.Errorf("%s: the list stopped following the end while streaming", b.mode)
	}
	tr.Entries = tr.Entries[:keep]
	b.settle()
	return s
}

var reply0 = reply(1)

func envInts(name string, def []int) []int {
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	var out []int
	for _, f := range strings.Split(v, ",") {
		n, _ := strconv.Atoi(f)
		out = append(out, n)
	}
	return out
}

func TestTimelineCost(t *testing.T) {
	sizes := envInts("TIMELINE_SIZES", []int{100, 1000, 5000})
	modes := strings.Split(cmp.Or(os.Getenv("TIMELINE_MODES"), "native,thin,thinfull,full"), ",")
	rounds := envInts("TIMELINE_ROUNDS", []int{1})[0]

	dir := tempDir(t)
	d := engine(t, daemon.Config{Dir: dir}, fake.New(fake.Turn(false)))
	seed(t, d, "/work/t3code", "A long thread", "Start")
	seed(t, d, "/work/t3code", "Another thread", "Hello")
	h := start(t, dir)
	var starts []time.Time
	// Reduced motion, so that each step is one frame: with motion, a
	// Tester frame at a still clock runs the transitions' frames twenty
	// times over.
	h.tt = ui.NewTesterWith(func(c *ui.Context) { starts = append(starts, time.Now()); h.a.view(c) }, 1320, 860, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: true}})
	b := &bench{t: t, h: h, starts: &starts, long: h.thread("A long thread"), other: h.thread("Another thread")}
	h.a.select_(b.long)
	h.until("its transcript", func() bool { return b.long.Transcript != nil && len(b.long.Transcript.Entries) > 0 })

	var out *os.File
	if p := os.Getenv("TIMELINE_OUT"); p != "" {
		out, _ = os.Create(p)
		defer out.Close()
		fmt.Fprintln(out, "round\tsize\tmode\tscenario\tsteps\tframes\tframe_p50_ms\tframe_p95_ms\tframe_max_ms\tui_ms_per_step\tcpu_ms_per_step\tkB_per_step\tmallocs_per_step\tgcs\tblank_row_steps\tstale_steps\tfirst_ms\tsettle_ms\tvisible\tmoved")
	}
	ms := func(d time.Duration) string { return fmt.Sprintf("%.2f", float64(d.Microseconds())/1000) }
	for r := range rounds {
		for _, n := range sizes {
			b.long.Transcript.Entries = synthetic(n)
			marker := "Reply " + strconv.Itoa((n-1)/3) + "."
			if n%3 == 1 {
				marker = "Turn " + strconv.Itoa(n/3) + ":"
			}
			// Each round starts the modes at a different one, so warmup and
			// drift favour none.
			k := r % len(modes)
			order := slices.Concat(modes[k:], modes[:k])
			for _, mode := range order {
				b.mode = mode
				setMode(t, h, mode)
				b.settle()
				for _, sc := range []struct {
					name string
					run  func() sample
				}{{"open", func() sample { return b.firstOpen(marker) }}, {"scroll", b.scroll}, {"stream", b.stream}} {
					s := sc.run()
					var total time.Duration
					for _, d := range s.steps {
						total += d
					}
					steps := time.Duration(len(s.steps))
					line := []string{strconv.Itoa(r), strconv.Itoa(n), mode, sc.name, strconv.Itoa(len(s.steps)), strconv.Itoa(s.frames),
						ms(pct(s.frameTimes, 0.5)), ms(pct(s.frameTimes, 0.95)), ms(pct(s.frameTimes, 1)), ms(total / steps), ms(s.cpu / steps),
						fmt.Sprintf("%.0f", float64(s.bytes)/1024/float64(steps)), strconv.FormatUint(s.mallocs/uint64(steps), 10), strconv.Itoa(int(s.gcs)),
						strconv.Itoa(s.blank), strconv.Itoa(s.stale), ms(s.steps[0]), ms(s.settle), strconv.Itoa(s.visible), strconv.Itoa(s.moved)}
					t.Log(strings.Join(line, "\t"))
					if out != nil {
						fmt.Fprintln(out, strings.Join(line, "\t"))
					}
				}
			}
		}
	}
}

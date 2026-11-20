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
	"glitched/internal/provider/fake"
)

// setMode is a no-op: only the native timeline is measured here.
func setMode(t *testing.T, h *harness, mode string) {}

// cpu is the process's CPU time so far, every thread's.
func cpu() time.Duration {
	var ru syscall.Rusage
	syscall.Getrusage(syscall.RUSAGE_SELF, &ru)
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
}

// sample is one scenario's run: each step's UI-thread time, and what the
// process spent over the whole of it.
type sample struct {
	steps      []time.Duration // each step's UI time: a tick of a 60 Hz window
	frameTimes []time.Duration // each frame's UI time: build, layout and paint
	// firstFrames are each step's first frame, which handles its input
	// and change: the frames after it in the step are a still clock's
	// repeats, which a Tester runs while something animates.
	firstFrames    []time.Duration
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
	// es are the rows while a scenario is measured, so that counting
	// blank rows doesn't list them anew each step inside the measurement.
	es []entry
}

// measure runs step n times, a frame time apart, as a window would.
func (b *bench) measure(n int, step func(i int)) sample {
	var s sample
	runtime.GC()
	var m0, m1 runtime.MemStats
	runtime.ReadMemStats(&m0)
	b.es = entriesOf(b.long)
	defer func() { b.es = nil }()
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
		for j, at := range append((*b.starts)[min(1, len(*b.starts)):], end) {
			s.frameTimes = append(s.frameTimes, at.Sub(from))
			if j == 0 {
				s.firstFrames = append(s.firstFrames, at.Sub(from))
			}
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

// blankRows counts the rows in view that the built-in Timeline rows plugin
// hasn't drawn yet, which show Glitched's own row meanwhile. Approval,
// question and report cards are always Glitched's.
func (b *bench) blankRows() int {
	if b.h.a.plugins == nil || b.h.a.selected != b.long.id() {
		return 0
	}
	app := b.h.a.plugins.App("timeline")
	if app == nil {
		return 0
	}
	es := b.es
	if es == nil {
		es = entriesOf(b.long)
	}
	first, last := b.h.a.listFor(b.long.id()).Visible()
	n := 0
	for i := first; i <= last && i < len(es); i++ {
		if k := es[i].kind; k != entryRequest && k != entryReport && app.PlaceView("row", es[i].key) == nil {
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
			b.t.Fatalf("never settled")
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
	opened, done := false, false
	var settledAt time.Duration
	start := time.Now()
	s := b.measure(90, func(i int) {
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
		// The row the frame drew is the reply as it is now.
		if es := b.h.a.rowsOf(b.long).es; es[len(es)-1].text != reply.Text {
			b.staleN++
		}
	})
	s.stale, b.staleN = b.staleN, 0
	if !l.AtEnd() {
		b.t.Errorf("%s: the list stopped following the end while streaming", b.mode)
	}
	tr.Entries = tr.Entries[:keep]
	b.h.a.changed()
	b.settle()
	return s
}

var reply0 = syntheticReply(1)

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
	modes := strings.Split(cmp.Or(os.Getenv("TIMELINE_MODES"), "native"), ",")
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
		fmt.Fprintln(out, "round\tsize\tmode\tscenario\tsteps\tframes\tframe_p50_ms\tframe_p95_ms\tframe_max_ms\twork_p50_ms\twork_p95_ms\tui_ms_per_step\tcpu_ms_per_step\tkB_per_step\tmallocs_per_step\tgcs\tblank_row_steps\tstale_steps\tfirst_ms\tsettle_ms\tvisible\tmoved")
	}
	ms := func(d time.Duration) string { return fmt.Sprintf("%.2f", float64(d.Microseconds())/1000) }
	for r := range rounds {
		for _, n := range sizes {
			b.long.Transcript.Entries = synthetic(n)
			h.a.changed()
			// The last reply's: rows go user, work, reply.
			last := n - 1 - (n-1-2+3)%3
			marker := "Reply " + strconv.Itoa(last/3) + "."
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
						ms(pct(s.frameTimes, 0.5)), ms(pct(s.frameTimes, 0.95)), ms(pct(s.frameTimes, 1)), ms(pct(s.firstFrames, 0.5)), ms(pct(s.firstFrames, 0.95)), ms(total / steps), ms(s.cpu / steps),
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

func BenchmarkHolds(b *testing.B) {
	tr := &client.Transcript{Entries: synthetic(20000)}
	c := &rowsCache{}
	c.derive(tr)
	b.ResetTimer()
	for range b.N {
		if !c.settled.holds(tr.Entries) {
			b.Fatal("changed")
		}
	}
}

func BenchmarkDeriveLive(b *testing.B) {
	tr := &client.Transcript{Entries: synthetic(20000)}
	c := &rowsCache{}
	c.derive(tr)
	b.ResetTimer()
	for range b.N {
		c.derive(tr)
	}
}

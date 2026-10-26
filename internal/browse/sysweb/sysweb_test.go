package sysweb

import (
	"os"
	"runtime"
	"testing"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"glitched/internal/browse"
	"glitched/internal/browse/conformance"
)

// The suite opens a window and runs the system's web view, so it runs
// only where GLITCHED_ENGINE_TESTS=1, on a desktop: the app's own event
// loop holds the main thread, and the tests run beside it.
var engine = os.Getenv("GLITCHED_ENGINE_TESTS") == "1"

// win is the window the suite's pages show in, and shown the page it
// shows.
var (
	win   *mygo.Window
	shown browse.Page
)

func init() {
	if engine {
		runtime.LockOSThread() // the event loop's
	}
}

func TestMain(m *testing.M) {
	if !engine {
		os.Exit(m.Run())
	}
	code := 0
	mygo.App.WhenReady(func() {
		win = mygo.NewWindow(mygo.WindowOptions{Title: "Glitched engine conformance", Width: 900, Height: 640,
			Content: ui.View(func(c *ui.Context) {
				if shown != nil {
					shown.Show(c).Fill()
				}
			})})
		go func() {
			code = m.Run()
			mygo.App.Quit()
		}()
	})
	if err := mygo.App.Run(); err != nil {
		panic(err)
	}
	os.Exit(code)
}

// do runs fn on the window's thread and waits for it.
func do(fn func()) {
	done := make(chan struct{})
	win.Update(func() {
		defer close(done)
		fn()
	})
	<-done
}

func TestConformance(t *testing.T) {
	if !engine {
		t.Skip("GLITCHED_ENGINE_TESTS is not 1: the suite opens a window")
	}
	var known map[string]string
	if runtime.GOOS == "windows" {
		known = map[string]string{"UnresolvedHost": "MyGo v0.5.5 takes WebView2's HOST_NAME_NOT_RESOLVED (13) for a cancelled load and drops it"}
	}
	conformance.Run(t, conformance.Harness{
		Open:  Opener(win),
		Do:    do,
		Known: known,
		Show: func(p browse.Page) {
			shown = p
			win.Invalidate()
		},
	})
}

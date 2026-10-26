// Package sysweb is the browser's adapter for the system's web views,
// through MyGo: WebView2 on Windows, WKWebView on macOS, WebKitGTK on
// Linux. A page is a web view of the window, which the view places with
// ui.WebView, under the native UI. Everything the browser knows of these
// engines is here (package browse has the seam).
package sysweb

import (
	"runtime"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"glitched/internal/browse"
)

// Background fills a page until it paints: the app's dark surface.
const Background = "#0c0c0e"

// page is a page in a web view.
type page struct {
	*mygo.Page
	view *mygo.WebView
	// closed is set as the page closes, on the view's thread, where its
	// events are delivered, which then go nowhere.
	closed *bool
}

func (p page) LoadHTML(html string)          { p.Page.LoadHTML(html, "") }
func (p page) Show(c *ui.Context) ui.Element { return ui.WebView(c, p.view) }

func (p page) Close() {
	*p.closed = true
	p.view.Destroy()
}

// Opener makes pages as web views of win, delivering their events through
// win.Update.
func Opener(win *mygo.Window) browse.Opener {
	return func(url string, events browse.Events) (browse.Page, error) {
		return open(win, url, events)
	}
}

// cancelled reports whether a failed load's code is a load given up for
// another, as one typed over it, which isn't a failure: NSURLErrorCancelled
// and WebKit's frame load interrupted on macOS, WebView2's
// OPERATION_CANCELED (14). MyGo v0.5.5 takes WebView2's 13 for it, which
// is HOST_NAME_NOT_RESOLVED: an address no name server knows fails with
// no event on Windows (docs/browser.md).
func cancelled(code int) bool {
	if runtime.GOOS == "windows" {
		return code == 14
	}
	return code == -999 || code == 102
}

// reloads reports whether a page whose process went, for reason, loads
// again: one that crashed or was ended, not one only unresponsive, which
// may come back with its state. MyGo v0.5.5 reports a WebView2 frame's
// process failing (kind 3) as the page's crash, which reloads it too
// (docs/browser.md).
func reloads(reason string) bool { return reason != "unresponsive" }

func open(win *mygo.Window, url string, ev browse.Events) (browse.Page, error) {
	v, err := win.NewWebView(mygo.WebViewOptions{URL: url, BackgroundColor: Background})
	if err != nil {
		return nil, err
	}
	p := v.Page()
	closed := new(bool)
	// The page's events come from the engine; the tab hears them where
	// the view builds, until the page closes.
	on := func(fn func()) {
		if fn != nil {
			win.Update(func() {
				if !*closed {
					fn()
				}
			})
		}
	}
	p.OnWillNavigate(func(*mygo.NavigateEvent) { on(ev.Changed) })
	p.OnDidFinishLoad(func() { on(ev.Changed) })
	p.OnDidNavigate(func(url string) {
		// LoadHTML's pages, as the one saying a load failed, are at none.
		if strings.HasPrefix(url, "about:") || strings.HasPrefix(url, "data:") || ev.Navigated == nil {
			return
		}
		on(func() { ev.Navigated(url) })
	})
	p.OnPageTitleUpdated(func(e *mygo.TitleEvent) {
		if title := e.Title; ev.Titled != nil {
			on(func() { ev.Titled(title) })
		}
	})
	p.OnDidFailLoad(func(e *mygo.LoadError) {
		if cancelled(e.Code) || ev.Failed == nil {
			return
		}
		at, why := e.URL, e.Description
		if strings.HasPrefix(at, "about:") {
			at = ""
		}
		on(func() {
			ev.Failed(at, why)
			if ev.Changed != nil {
				ev.Changed()
			}
		})
	})
	p.SetWindowOpenHandler(func(req mygo.WindowOpenRequest) *mygo.WindowOptions {
		if u := req.URL; ev.Opened != nil {
			on(func() { ev.Opened(u) })
		}
		return nil
	})
	p.OnRenderProcessGone(func(reason string) {
		if !reloads(reason) {
			return
		}
		on(func() {
			if !*closed {
				p.Reload()
			}
		})
	})
	return page{p, v, closed}, nil
}

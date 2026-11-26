// Package sysweb is the browser's adapter for the system's web views,
// through MyGo: WebView2 on Windows, WKWebView on macOS, WebKitGTK on
// Linux. A page is a web view of the window, which the view places with
// ui.WebView, under the native UI. Everything the browser knows of these
// engines is here (package browse has the seam).
package sysweb

import (
	"context"
	"runtime"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"glitched/internal/browse"
	"glitched/internal/webtarget"
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
	crash  *crashes
}

// The app's navigations are new ones, after which a crash reloads again.
func (p page) LoadURL(url string) error { p.crash.asked(); return p.Page.LoadURL(url) }
func (p page) Reload()                  { p.crash.asked(); p.Page.Reload() }
func (p page) GoBack()                  { p.crash.asked(); p.Page.GoBack() }
func (p page) GoForward()               { p.crash.asked(); p.Page.GoForward() }

func (p page) LoadHTML(html string)          { p.Page.LoadHTML(html, "") }
func (p page) Show(c *ui.Context) ui.Element { return ui.WebView(c, p.view) }

func (p page) Close() {
	*p.closed = true
	p.view.Destroy()
}

// CapturePage captures a PNG screenshot of the web view's visible page.
func (p page) CapturePage(ctx context.Context) ([]byte, error) {
	type res struct {
		png []byte
		err error
	}
	ch := make(chan res, 1)
	go func() {
		png, err := p.view.CapturePage()
		ch <- res{png, err}
	}()
	select {
	case r := <-ch:
		return r.png, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
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

// crashes is what a page does as its process goes (reloads): it loads
// again once for each navigation; going again before another, it shows
// that it failed, rather than reload a page that keeps crashing it.
// Its methods are called on the view's thread.
type crashes struct {
	reloaded bool
	at       string // where the page was as it reloaded
}

// asked notes a navigation the app asked for: LoadURL, Reload, back or
// forward.
func (c *crashes) asked() { c.reloaded = false }

// committed notes the page at url: another address than the one reloaded
// is a new navigation, the reload's own isn't.
func (c *crashes) committed(url string) {
	if c.reloaded && url != c.at {
		c.reloaded = false
	}
}

// gone says what the page at url does as its process goes, for reason:
// reload, or show it failed.
func (c *crashes) gone(reason, url string) (reload, fail bool) {
	if !reloads(reason) {
		return false, false
	}
	if c.reloaded {
		return false, true
	}
	c.reloaded, c.at = true, url
	return true, false
}

func open(win *mygo.Window, url string, ev browse.Events) (browse.Page, error) {
	v, err := win.NewWebView(mygo.WebViewOptions{
		URL:             url,
		BackgroundColor: Background,
		Page:            mygo.PageOptions{PreloadScript: webtarget.PreloadScript()},
	})
	if err != nil {
		return nil, err
	}
	p := v.Page()
	closed, crash := new(bool), &crashes{}
	var lastURL string
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
	syncSPAURL := func() {
		u := p.URL()
		if u == "" || u == lastURL || strings.HasPrefix(u, "about:") || strings.HasPrefix(u, "data:") {
			return
		}
		if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
			return
		}
		lastURL = u
		crash.committed(u)
		if ev.Navigated != nil {
			ev.Navigated(u)
		}
	}
	p.OnWillNavigate(func(*mygo.NavigateEvent) { on(ev.Changed) })
	p.OnDidFinishLoad(func() {
		on(func() {
			syncSPAURL()
			if ev.Changed != nil {
				ev.Changed()
			}
		})
	})
	p.OnDidNavigate(func(url string) {
		// LoadHTML's pages, as the one saying a load failed, are at none.
		if strings.HasPrefix(url, "about:") || strings.HasPrefix(url, "data:") {
			return
		}
		on(func() {
			lastURL = url
			crash.committed(url)
			if ev.Navigated != nil {
				ev.Navigated(url)
			}
		})
	})
	p.OnPageTitleUpdated(func(e *mygo.TitleEvent) {
		title := e.Title
		on(func() {
			syncSPAURL()
			if ev.Titled != nil {
				ev.Titled(title)
			}
		})
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
			at := p.URL()
			switch reload, fail := crash.gone(reason, at); {
			case reload:
				p.Reload()
			case fail && ev.Failed != nil:
				if strings.HasPrefix(at, "about:") {
					at = ""
				}
				ev.Failed(at, "The page crashed again as it reloaded.")
				if ev.Changed != nil {
					ev.Changed()
				}
			}
		})
	})
	return page{p, v, closed, crash}, nil
}

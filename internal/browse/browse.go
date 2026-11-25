// Package browse is the seam between the browser pane and the engines
// that show its pages. Above it, everything is the same on every engine:
// the tabs, the bar, the keys, and what agents do in pages, which goes
// through one page script (package webtarget). Below it, an adapter per
// engine: the system's web views today (package sysweb: WebView2 on
// Windows, WKWebView on macOS, WebKitGTK on Linux), and Chromium (CEF) if
// one is added. An adapter implements Opener and Page, and passes the
// conformance suite (package browse/conformance); nothing above the seam
// changes for a new one.
package browse

import (
	"context"

	"github.com/egoist/mygo/ui"
)

// Opener makes a page loading url, which tells what it does through
// events. Adapters deliver events on the thread that builds the view, one
// at a time, as the window's Update runs them.
type Opener func(url string, events Events) (Page, error)

// Page is a page of an engine: what the bar drives, what the pane shows,
// and what agents' scripts run in. Its methods but EvalContext are called
// on the thread that builds the view.
type Page interface {
	// LoadURL goes to url, as typed or asked for by an agent.
	LoadURL(url string) error
	// LoadHTML shows a page of the app's, as one saying a load failed,
	// at no address: it is no navigation (Events.Navigated).
	LoadHTML(html string)
	Reload()
	Stop()
	GoBack()
	GoForward()
	CanGoBack() bool
	CanGoForward() bool
	IsLoading() bool
	// EvalContext runs JavaScript in the page's top frame as the body of
	// an async function and returns its result decoded from JSON, awaiting
	// a promise it returns; a script that throws returns an error. It is
	// not subject to the page's Content Security Policy. It is called from
	// any goroutine but the view's, which it waits on, as agents call it,
	// and returns by the time ctx is done.
	EvalContext(ctx context.Context, js string) (any, error)
	// Show places the page in the view: the element fills what it is
	// given. A page that isn't shown keeps its state.
	Show(c *ui.Context) ui.Element
	// Close closes the page for good: no event is delivered after it, not
	// even one the engine sent before.
	Close()
}

// Capturer is an optional capability of a Page that can capture a PNG
// screenshot of its visible viewport.
type Capturer interface {
	CapturePage(ctx context.Context) ([]byte, error)
}

// Events are what a page tells its tab. A nil func is not called.
type Events struct {
	// Navigated: a navigation of the top frame to another document, at
	// url, committed: by the user, the page, LoadURL, back, forward or
	// reload; not for LoadHTML's pages. Changes within a document
	// (history.pushState, a fragment) need not be reported.
	Navigated func(url string)
	// Titled: the page's title changed.
	Titled func(title string)
	// Failed: the load of url failed, why says why; not for a load given
	// up for another, as one typed over it.
	Failed func(url, why string)
	// Opened: the page asked for a window at url (window.open, a link to
	// a new window); the adapter opens none itself.
	Opened func(url string)
	// Changed: the page may have started or stopped loading, which
	// IsLoading tells. It comes at least as a load ends; a load the page
	// starts may tell it as it starts, and the app's own (LoadURL, Reload)
	// need not, as the app knows of them.
	Changed func()
}

package main

import (
	"fmt"
	"html"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// The preview pane shows the project's dev server beside the chat, in a
// web view under the native UI, as MyGo's embed-webview example shows its
// pages: an address bar with back, forward and reload, and the page, so
// the agent's changes show as they land. Where nothing answers at the
// address, a page says so, in the app's colors.

const defaultPreviewURL = "http://localhost:5173"

// previewView returns the pane's web view, made the first time it shows;
// nil where there is no window, as in tests.
func (a *app) previewView() *mygo.WebView {
	if a.preview != nil || a.win == nil {
		return a.preview
	}
	v, err := a.win.NewWebView(mygo.WebViewOptions{URL: a.previewURL, BackgroundColor: "#0c0c0e"})
	if err != nil {
		a.previewErr = err.Error()
		return nil
	}
	page := v.Page()
	page.OnDidNavigate(func(url string) {
		// The page saying nothing answers is the address's still.
		if strings.HasPrefix(url, "about:") || strings.HasPrefix(url, "data:") {
			return
		}
		a.win.Update(func() { a.previewURL, a.previewDraft = url, url })
	})
	page.OnDidFailLoad(func(e *mygo.LoadError) {
		// A load given up for another, as one typed over it, isn't a
		// failure (NSURLErrorCancelled; WebKit's frame load interrupted).
		if e.Code == -999 || e.Code == 102 {
			return
		}
		a.win.Update(func() { page.LoadHTML(failedPage(a.previewURL, e.Description), "") })
	})
	a.preview = v
	return v
}

// failedPage is what the pane shows where a page couldn't load: for the
// project's own server, that it isn't running; for another, why.
func failedPage(url, why string) string {
	title, hint := "Couldn't load "+url, why
	if isLocal(url) {
		title = "Nothing running at " + url
		hint = `Start the project's dev server, as <code style="background:#18181b;padding:1px 5px;border-radius:4px">bun dev</code>, then reload.`
	} else {
		hint = html.EscapeString(why)
	}
	return fmt.Sprintf(`<!doctype html><meta charset="utf-8">
<body style="margin:0;height:100vh;display:grid;place-items:center;background:#0c0c0e;color:#ececf0;font:13px -apple-system,system-ui,sans-serif">
<div style="text-align:center;max-width:340px;line-height:1.6;padding:0 16px">
<div style="font-size:28px;margin-bottom:8px">🔌</div>
<div style="font-weight:600;font-size:15px;word-break:break-all">%s</div>
<div style="color:#8e8e98;margin-top:6px">%s</div>
</div>`, html.EscapeString(title), hint)
}

// normalizeURL makes what was typed in the address an address: as it is
// with a scheme; else https, as macOS's web view loads plain http only
// from the machine itself and the local network, where dev servers run,
// which keep http.
func normalizeURL(typed string) string {
	u := strings.TrimSpace(typed)
	if u == "" || strings.Contains(u, "://") {
		return u
	}
	if isLocal("http://" + u) {
		return "http://" + u
	}
	return "https://" + u
}

// isLocal reports whether an address is on this machine or the local
// network.
func isLocal(url string) bool {
	host := url
	if i := strings.Index(host, "://"); i >= 0 {
		host = host[i+3:]
	}
	if i := strings.IndexAny(host, "/?#"); i >= 0 {
		host = host[:i]
	}
	if i := strings.LastIndex(host, ":"); i >= 0 && !strings.Contains(host[i:], "]") {
		host = host[:i]
	}
	switch {
	case host == "localhost", strings.HasSuffix(host, ".localhost"), strings.HasSuffix(host, ".local"),
		host == "0.0.0.0", host == "[::1]", strings.HasPrefix(host, "127."),
		strings.HasPrefix(host, "10."), strings.HasPrefix(host, "192.168."):
		return true
	}
	return false
}

func (a *app) previewPanel(c *ui.Context) {
	v := a.previewView()
	ui.Column(c).Width(previewWidth).FillHeight().Children(func() {
		bar := c.TitleBar()
		head := ui.Row(c).Height(max(bar.Height, headerHeight)).Padding(0, 8, 0, 10).Gap(4).AlignItems(ui.Center)
		head.DragWindow().Children(func() {
			back := iconButton(c, "back", icArrowLeft, "Back", false).Padding(5)
			fwd := iconButton(c, "forward", icArrowRight, "Forward", false).Padding(5)
			reload := iconButton(c, "reload", icRotate, "Reload", false).Padding(5)
			if v != nil {
				page := v.Page()
				back.Disabled(!page.CanGoBack())
				fwd.Disabled(!page.CanGoForward())
				if back.Clicked() {
					page.GoBack()
				}
				if fwd.Clicked() {
					page.GoForward()
				}
				if reload.Clicked() {
					page.LoadURL(a.previewURL)
				}
			}
			// The address: Enter goes there; one without a scheme is
			// https, or http on this machine and the local network.
			in := ui.TextInput(c.Key("url"), &a.previewDraft).Grow(1).FontSize(12).Font("monospace").
				Padding(5, 10).Radius(8).Label("Address")
			if in.Submitted() && v != nil && strings.TrimSpace(a.previewDraft) != "" {
				url := normalizeURL(a.previewDraft)
				a.previewURL, a.previewDraft = url, url
				v.Page().LoadURL(url)
			}
			if iconButton(c, "external", icExternal, "Open in browser", false).Padding(5).Clicked() {
				c.OpenURL(a.previewURL)
			}
			if iconButton(c, "close-preview", icX, "Close preview", false).Padding(5).Clicked() {
				a.showPreview = false
			}
		})
		// Below its glass header, the panel on the sidebar's color.
		ui.Column(c).Grow(1).Background(palette.sidebar).BorderWidth(1, 0, 0, 1).BorderColor(palette.border).Children(func() {
			switch {
			case v != nil:
				ui.WebView(c, v).Grow(1)
			case a.previewErr != "":
				ui.Text(c, "The preview could not start: "+a.previewErr).TextColor(palette.muted).Margin(ui.Auto).Padding(24)
			default:
				ui.Text(c, "The preview shows here, in the app's window.").TextColor(palette.muted).Margin(ui.Auto)
			}
		})
	})
}

const previewWidth = 560

func (a *app) togglePreview() {
	a.showPreview = !a.showPreview
	if a.showPreview {
		a.showDiff = false // one pane beside the chat at a time
	}
}

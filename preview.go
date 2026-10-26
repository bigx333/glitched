package main

import (
	"fmt"
	"html"
	"strings"

	"github.com/egoist/mygo/ui"
)

// The preview pane shows the project's dev server beside the chat, in a
// web view under the native UI, as MyGo's embed-webview example shows its
// pages: an address bar with back, forward and reload, and the page, so
// the agent's changes show as they land; other tabs browse (browser.go).
// Where nothing answers at the address, a page says so, in the app's
// colors.

const defaultPreviewURL = "http://localhost:5173"

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

// previewWidth is the preview's width: as the unified diff's, a little
// wider, until its edge is dragged; then as dragged, within the room the
// window leaves (previewSpan).
func (a *app) previewWidth() float32 {
	if a.previewDragged == 0 {
		return panelWidth(a.roomBeside, 0.45, 400, 640)
	}
	lo, hi := previewSpan(a.roomBeside)
	return min(max(a.previewDragged, lo), hi)
}

// minPreviewWidth is the narrowest the preview drags to: a page's phone
// layout, and its bar.
const minPreviewWidth = 360

// previewSpan is how narrow and how wide the preview drags in room beside
// the sidebar: the chat keeps its least width, and where room is short the
// preview gives way first, as the diff does.
func previewSpan(room float32) (lo, hi float32) {
	hi = max(room-minChatWidth, 0)
	return min(minPreviewWidth, hi), hi
}

// previewEdge is the preview's left edge, against the chat, which the
// pointer drags to resize it, both reflowing as it moves. A double click
// gives it back its own width; where a drag ends, it stays, across
// launches (layout.go).
func (a *app) previewEdge(c *ui.Context) {
	edge := dragEdge(c, "preview-edge", "Resize preview").Left(-2)
	if dx, _, ok := edge.Dragged(); ok && dx != 0 {
		lo, hi := previewSpan(a.roomBeside)
		a.previewDragged = min(max(a.previewWidth()-dx, lo), hi)
		a.previewHeld = true
	}
	if _, _, ok := edge.DragEnded(); ok {
		a.previewHeld = false
		a.saveLayout()
	}
	if edge.DoubleClicked() {
		a.previewDragged = 0
		a.saveLayout()
	}
}

func (a *app) togglePreview() {
	a.showPreview = !a.showPreview
	if a.showPreview {
		a.showDiff = false // one pane beside the chat at a time
	}
}

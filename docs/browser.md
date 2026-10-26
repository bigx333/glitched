# The browser

The preview pane (⇧⌘P, the globe in the chat's header) is a browser beside
the chat, for the user and, next, for agents. Its first tab is the
project's dev server, `http://localhost:5173`; other tabs browse anywhere.

- **Bar:** back, forward, reload (stop while a page loads), the address,
  new tab, open in the system browser, close the pane. An address without a
  scheme loads over https, or http on this machine and the local network.
- **Tabs:** ⌘T (Ctrl+T on Windows and Linux), the `+` in the bar, View ›
  New Browser Tab, or the command palette open a tab after the one shown,
  with the keyboard in its empty address. The strip of tabs shows once
  there are two: each shows its page's title, a spinner while it loads, and
  a close button. Closing the last closes the pane. Links opening windows
  (`target="_blank"`, `window.open`) open as tabs after their page's; only
  http and https.
- **Keys while the pane shows:** ⌘L takes the address, ⌘R reloads, a page
  loading or not. While the terminal has the keyboard it keeps them (on
  Windows and Linux they are Ctrl+T, L and R, the shell's); while a page
  has it, its web view takes the keys first.
- **Pages:** each tab has a page of its own, made the first time the tab
  shows and kept while another shows, so a tab comes back where it was. A
  page that fails to load says why, in the app's colors (for the dev
  server, that it isn't running); reload tries its address again.

## The seam: engines behind one interface

```
 browser.go           tabs, bar, keys               ┐
 internal/webtarget   page script: snapshot, refs   │ the same on every engine
 (agent tools, next)                                ┘
 ──────────────── internal/browse: Opener, Page, Events ────────────────
 internal/browse/sysweb   WebView2 · WKWebView · WebKitGTK (through MyGo)
 (internal/browse/cef)    Chromium, if one is added
```

Package `browse` is the whole of what the browser knows of an engine. The
app picks the engine once, as it makes its window (`main.go`:
`a.browser.open = sysweb.Opener(a.win)`); nothing else names one.

- `Opener func(url string, events Events) (Page, error)` makes a page
  loading `url`.
- `Page` is what the bar drives (`LoadURL`, `LoadHTML`, `Reload`, `Stop`,
  `GoBack`, `GoForward`, `CanGoBack`, `CanGoForward`, `IsLoading`), what
  the pane shows (`Show(c) ui.Element`, filling what it's given; a page
  not shown keeps its state), what agents' scripts run in
  (`EvalContext`: the body of an async function, its result decoded from
  JSON, promises awaited, throws as errors, not subject to the page's CSP),
  and `Close`. Its methods are called where the view builds, but
  `EvalContext`, which agents call from goroutines of their own, never the
  view's, as it waits on it.
- `Events` are what a page tells its tab, delivered where the view builds,
  and none after `Close`, even one the engine sent before:
  - `Navigated`: the top frame committed another document, by the user,
    the page, `LoadURL`, back, forward or reload; not `LoadHTML`'s pages.
    Changes within a document (`history.pushState`, a fragment) need not
    count.
  - `Titled`: the page's title changed.
  - `Failed`: the URL whose load failed, and why; not a load given up for
    another, as one typed over it.
  - `Opened`: the page asked for a window; the adapter opens none itself.
  - `Changed`: loading may have started or stopped, which `IsLoading`
    tells; at least as a load ends.

Everything engine-specific is in the adapter: the web view's own events
and their quirks (which error codes mean a superseded load, which URLs
`LoadHTML` shows at), and how its view is placed. The tab model, the bar,
the page script and the agent tools never import an engine.

### Conformance

`internal/browse/conformance` is the suite every adapter passes, on a
real engine, with pages served by the test:

- **Navigation:** the first load and its title, `LoadURL`, back and
  forward and what they allow, reload as a new page, a link followed.
- **App pages:** `LoadHTML` is no navigation, and scripts run in it.
- **Failures:** a load nothing answers says its URL and why.
- **Unresolved host:** an address no name server knows fails too.
- **Windows:** `window.open` and a `target="_blank"` link are `Opened`, and
  open nothing.
- **Closed:** a page closed tells nothing more.
- **Eval:** values, objects, promises, `await`, throws, `undefined`.
- **Snapshot:** a page made to catch differences: `display: contents`,
  tree items nested in a group and named by their own text, a dropdown's
  options (shown, without refs), a closed `<details>`, a heading without a
  level, a disabled fieldset, `visibility: hidden` around a visible
  button, `hidden`, `aria-hidden` and `display: none`, an SVG link, an
  image link with no name, a label in a shadow root, a button in a
  bordered, padded iframe.
- **Limits:** the ref and character budgets, and every ref shown counted.
- **Refs:** a ref resolves to its element, at its place in the top
  viewport through frames; a newer snapshot, the element removed, or a
  reload make it fail, saying which.
- **Tampering:** a page replacing or deleting the script's state changes
  nothing refs find (what this guards, and what not, is below).

An adapter runs it from a test with a `Harness`: its `Opener`, a `Do` that
runs a func where the view builds, a `Show` for engines that load only
what shows, and `Known`: tests the engine fails for a known reason, a bug
of its own to fix upstream, which are skipped saying why. sysweb's opens a
MyGo window, so it runs on a desktop only:

```sh
GLITCHED_ENGINE_TESTS=1 go test -v ./internal/browse/sysweb
```

It passes on WebView2 (Windows 11); the log is in
`U:\glitched-proof\browser\conformance-windows.log`. On macOS the same
command runs it on WKWebView.

sysweb's one known failure is on Windows: MyGo v0.5.5 takes WebView2's
`HOST_NAME_NOT_RESOLVED` (13) for `OPERATION_CANCELED` (14) and drops it,
so a mistyped host fails with no event there (the tab keeps its old page). sysweb itself takes 14 as cancelled, so a load given up for
another doesn't show as failed. The fix is upstream, in MyGo's
`internal/windows/webview.go`.

### Adding an engine (CEF)

A CEF adapter is a package `internal/browse/cef` with an `Opener` and a
`Page`, a test that runs the conformance suite, and the line in `main.go`
that picks it. Nothing above the seam changes.

- `EvalContext`: DevTools' `Runtime.evaluate` with the script wrapped in
  an async function, `awaitPromise` and `returnByValue`.
- `Show`: CEF's window as a child of the app's, placed where the element
  is, or its off-screen frames painted into the element; MyGo would need a
  hook to place a native child view as it places web views.
- `Events`: `Page.frameNavigated` for the top frame, title changes,
  `loadingFailed`, `targetCreated` for windows (denied, then `Opened`),
  `frameStartedLoading` and `frameStoppedLoading`.
- Trusted input (`Input.dispatchMouseEvent`, `dispatchKeyEvent`), when
  agents act, as a faster path with the same outcome as the page script's,
  which the suite's action tests (next) hold both to.

## Element targeting for agents

`internal/webtarget` works on any `browse.Page`, through one page script,
`snapshot.js`, which behaves the same on every engine.

- `webtarget.Take(ctx, page, opts)` snapshots the page as lines of roles
  and names, the shape of an accessibility tree (and of Playwright's AI
  snapshots), giving each element an agent can act on a ref:

  ```
  - heading "Go (programming language)" [level=1]
  - button "64 languages" [ref=s67075e8]
  - link "Talk" [ref=s67075e10]
  - link [url=/wiki/Main_Page] [ref=s67075e3]
  ```

  By default it lists the elements to act on (buttons, links, text boxes,
  checkboxes, selects, tabs, sliders, tree and menu items and the other
  interactive roles), headings, and a dropdown's options, without refs, as
  they have no box until it opens (an agent will pick one through the
  dropdown). `Full` adds the structure around them (landmarks, lists,
  groups, forms, tables), indented. A tree or menu item is named by its own
  text, not its children's.
- It skips what has no box (`display: none`, a closed `<details>`, light
  DOM no slot takes, `content-visibility: hidden`), `hidden` and
  `aria-hidden`, and `visibility: hidden` but for what shows inside;
  `display: contents` shows what is in it. It goes into open shadow roots
  and same-origin frames, marks cross-origin frames as not reached and
  frames still loading as such, and stops at 2000 refs or 50 000
  characters, saying so.
- **Refs** are `s<epoch>e<n>`: the snapshot's epoch, then the element's
  number in it. Epochs count up from a random start in each run of the
  app. Only the page's latest snapshot resolves: a ref from an earlier one,
  from before the page navigated or reloaded, or whose element the page
  removed fails, saying which, and to take a new snapshot.
- `webtarget.Describe(ctx, page, ref)` resolves a ref to its element's
  role, name and box in the top frame's viewport, through the frames it is
  in. `Snapshot.Verify(ref, element)` checks it is still what the snapshot
  named, as an action will before it acts.
- **What a page can do to refs:** the script keeps its state private to
  functions it installs, frozen, under a property named at random in each
  run of the app (`__glitched_<random>`), so a page can neither plant a
  fake before the first snapshot nor replace it after. That is all it
  guards. It runs in the page's own world, so a page that changed the
  built-ins it uses (`Object.defineProperty`, `WeakRef`) before the first
  snapshot, or that finds the property and calls it itself, can still send
  a ref to an element of its choosing, and `Verify` can't tell, as the page
  answers both. Before agents act on pages, the script must run in an
  isolated world: WKWebView's content worlds, and DevTools'
  `Page.createIsolatedWorld` on WebView2 and CEF.

### What came from the automation engine

Cahil's automation engine (`U:\engine-temp`, the ChaosPickle browser MCP)
is Rust driving CEF over DevTools, and several of its files note code of
GPL lineage, so no code was copied. Glitched takes its design:

- The snapshot format: role, quoted name, `[level=N]` and state flags,
  `[ref=…]`, the interactive mode by default with a full mode, the budget
  of refs and characters with a marker where it was cut.
- Refs carrying a snapshot epoch seeded at random in each run, only the
  latest snapshot's resolving, and stale refs failing with the reason
  (navigated, a newer snapshot, re-rendered) instead of finding another
  element.
- The interactive role list, and its injected-JS fallback for engines
  without an accessibility tree, which Glitched's script grows into.
- Its tool surface (`browser_snapshot`, `browser_click`, `browser_type`,
  `browser_tabs`, …) as the plan for Glitched's.

Not taken: recording and code generation (left out, as asked), the TCP
bridge and its process model. Its CEF host is the model for a CEF adapter.

## Next: agents driving the browser

1. **Tools**, by tab id: `browser_tabs` (list, new, select, close),
   `browser_navigate`/`back`/`forward`/`reload`, `browser_snapshot`,
   `browser_click`, `browser_type`, `browser_select`, `browser_scroll`,
   `browser_wait_for`, `browser_screenshot`. They act on the user's tabs,
   so the user sees what the agent does, and go through the thread's
   approval policy.
2. **Actions** in the page script, the same on every engine: resolve the
   ref, `Verify` it, check it is visible and enabled, scroll it into view,
   then act with DOM events (click; type with the native value setter, so
   frameworks see it; select an option of a dropdown; scroll), and
   `wait_for` text or a ref by polling in the page. An adapter with trusted
   input (WebView2 and CEF through DevTools) may click and type that way
   instead; the conformance suite gains action tests both paths pass.
3. **Isolation** of the script in its own world, as above: required before
   agents act.
4. **Single-page apps:** MyGo reports no `history.pushState` or fragment
   navigation, so the address goes stale in a Vite app moving between
   routes; a script reporting them, or MyGo events, would fix it.
5. **Cross-origin frames**, through the engine's frame tree where it has
   one.

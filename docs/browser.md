# The browser

The preview pane (⇧⌘P, the globe in the chat's header) is a browser beside
the chat, for the user and for agents (Agents, below). Its first tab is the
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
  http and https. The tab shows if its page's did; one opened from a tab in
  the background stays there. The pages opened from one of the user's
  tabs, and those they open in turn, are one line: it opens at most 5 tabs
  within 10 seconds, and has at most 10 open at once (closing one makes
  room); what it asks for beyond that opens nothing, as a page opening
  windows, or itself, in a loop would (`openBurst`, `openWindow`,
  `maxOpenedTabs` in `browser.go`).
- **Keys while the pane shows:** ⌘L takes the address, ⌘R reloads, a page
  loading or not. While the terminal has the keyboard it keeps them (on
  Windows and Linux they are Ctrl+T, L and R, the shell's); while a page
  has it, its web view takes the keys first.
- **Width:** the pane's edge against the chat drags it wider and narrower
  (the resize cursor, and the accent as it's pointed at), both sides
  reflowing as it moves: the page follows its bounds each frame, and the
  chat, composer included, lays out as a window that narrow would,
  without easing after the edge. The pane is 360 DIPs at least and the
  chat 420; where the window has room for neither, the pane gives way
  first. A double click on the edge gives the pane back its own width
  (45% of the room, 400 to 640). Where a drag ends it stays, across
  launches, in `layout.json` in the state directory. The edge is Nelson's
  sidebar edge (`dragEdge`), which both use.
- **Pages:** each tab has a page of its own, made the first time the tab
  shows and kept while another shows, so a tab comes back where it was. A
  page that fails to load says why, in the app's colors (for the dev
  server, that it isn't running); reload tries its address again.

## The seam: engines behind one interface

```
 browser.go           tabs, bar, keys               ┐
 internal/webtarget   page script: snapshot, refs   │ the same on every engine
 agentbrowser.go      agents' tools                 ┘
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
  bordered, padded iframe, an iframe's body made editable by
  `contenteditable` and one in design mode.
- **Limits:** the ref and character budgets, and every ref shown counted.
- **Refs:** a ref resolves to its element, at its place in the top
  viewport through frames, and still does after the page fires a
  `pagehide` of its own; a newer snapshot, the element removed, its frame
  navigated, or a reload make it fail, saying which.
- **PageCache:** going back to a page the back/forward cache restores
  makes a ref from before it left fail too. On an engine that loads the
  page again instead, it is skipped, saying so.
- **Tampering:** each on a page of its own, a page replacing or deleting
  the script's state, or patching after it is installed what it goes
  through (DOM methods and getters, `WeakRef`, `RegExp.prototype.exec`, an
  index setter on `Array.prototype`, `Set`, `Map`, `String` and `Array`
  methods, `then` on `Object.prototype`), changes nothing refs find or
  what they say, from a snapshot taken before or after (what this guards,
  and what not, is below).
- **Scaled frames:** a ref's box through a frame its transform scales,
  each axis on its own, one in it, and one an ancestor's transform scales.
- **Fractional frames:** a ref's box through frames whose layout size is
  a fraction of a pixel: the same box as with no scale for frames no
  transform scales, and one whose centre is on its element for one that
  is scaled.

An adapter runs it from a test with a `Harness`: its `Opener`, a `Do` that
runs a func where the view builds, a `Show` for engines that load only
what shows, and `Known`: tests the engine fails for a known reason, a bug
of its own to fix upstream, which are skipped saying why. sysweb's opens a
MyGo window, so it runs on a desktop only:

```sh
GLITCHED_ENGINE_TESTS=1 go test -v ./internal/browse/sysweb
```

It passes on WebView2 (Windows 11):
[the log](https://github.com/bigx333/glitched/blob/media/browser/conformance-windows.log).
On macOS the same command runs it on WKWebView.

MyGo v0.5.5 has two WebView2 bugs sysweb works around where it can; both
are for upstream, in MyGo's `internal/windows/webview.go` (not filed):

- It takes `HOST_NAME_NOT_RESOLVED` (13) for `OPERATION_CANCELED` (14) and
  drops it, so a mistyped host fails with no event on Windows (the tab
  keeps its old page): sysweb's one known failure of the suite. sysweb
  takes 14 as cancelled itself, so a load given up for another doesn't
  show as failed.
- It reports a frame's process failing (`ProcessFailed` kind 3) as the
  page's crash, so sysweb reloads the page for it, as for a real crash.
  sysweb doesn't reload a page that is only unresponsive (kind 2), which
  may come back with its state. A crashed page reloads once for each
  navigation: crashing again before another (the app's `LoadURL`,
  `Reload`, back or forward, or the page going to another address), it
  shows the page saying it failed (`Failed`) instead of reloading again.

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
  - link [url="/wiki/Main_Page"] [ref=s67075e3]
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
  and same-origin frames once they show (not hidden by `visibility`) and
  their page is there (past `loading`; a frame still at `about:blank` with
  an address of its own is on its way, one with none is a page a script
  fills, as an editor's), marks cross-origin frames as not reached and
  frames still loading as such, and stops at 2000 refs or 50 000
  characters, saying so. A body edited whole, as an editor's frame's in
  design mode or `contenteditable`, is a text box, with a ref. A link with
  no name says where it goes, quoted: `[url="/wiki/Main_Page"]`.
- The top document's body is visited as an element, as a frame's is, not
  only walked: a `<body hidden>` or `<body aria-hidden="true">` gives an
  empty snapshot, and a `<body role=…>` is a line of its own in `Full`,
  with all else indented a level under it.
- **Refs** are `s<epoch>e<n>`: the snapshot's epoch, then the element's
  number in it. Epochs count up from a random start in each run of the
  app. Only the page's latest snapshot resolves: a ref from an earlier one,
  from before the page or the frame it is in navigated or reloaded, or
  whose element the page removed fails, saying which, and to take a new
  snapshot. A page that leaves drops its refs, on the `pagehide` the
  engine fires, not one the page fires itself, so a page the back/forward
  cache brings back has none until its next snapshot. A ref holds the
  frames it is in, each with the document it showed, and resolves while
  each still shows it: an element of a document its frame left still
  reads as connected, in that document.
- `webtarget.Describe(ctx, page, ref)` resolves a ref to its element's
  role, name and box in the top frame's viewport, through the frames it is
  in: past each frame's border and padding, at the scale it shows at, which
  its transforms and its ancestors' give it (`transform: scale()`,
  `scale`), each axis on its own. A frame turned, skewed or flipped
  (`rotate`, `skew`, a negative scale) is not mapped: Describe scales it
  to its bounding box, so the box is off, and a click at its centre can
  miss. `Snapshot.Verify(ref, element)` checks it is still what the
  snapshot named, as an action will before it acts.
- **What a page can do to refs:** the script keeps its state private to
  functions it installs, frozen, under a property named at random in each
  run of the app (`__glitched_<random>`). Once installed in a document, it
  calls no built-in, and reads no prototype, that the page can change:
  every function and getter it uses is the one it found as it was
  installed, called through the `Reflect.apply` it found. That is the
  DOM's (`getAttribute`, `matches`, `querySelector`, `getRootNode`,
  `getElementById`, `addEventListener`, `textContent`, `innerText`,
  `localName`, `labels`, `isConnected`, `childNodes`, `contentDocument`,
  `getBoundingClientRect` and a rect's `x`, `y`, `width` and `height`,
  and the like),
  `getComputedStyle` and `getPropertyValue`, `WeakRef` and its `deref`,
  `String`'s `slice`, `toUpperCase`, `toLowerCase` and `repeat`, `Array`'s
  `join`, `JSON.stringify` and `parseFloat`. It parses refs, and splits
  and collapses white space, with no regular expression; it writes its
  arrays with the `Object.defineProperty` it found, so a setter on
  `Array.prototype` sees nothing; it keeps its refs and tables in objects
  with no prototype; and it answers with objects with none, so the
  promise its answer resolves looks up no `then`, and its list of
  elements is an array with no prototype either, so `JSON.stringify`
  finds no `toJSON` on `Array.prototype`. So a page can't replace
  it, or patch any of those, to send a ref elsewhere or change what it
  says of one. Go checks what comes back (`Take`, `Describe`): a snapshot of
  another epoch than the one asked for, refs other than that epoch's
  `s<epoch>e1`, `e2`, … in order, or more than the budgets (refs, the
  text's characters, a title of 300 characters and a URL of 2048, which
  the script clips to, a role of 80 and a name of 120) fails, saying why.
  That bounds what a fake can answer, not whether it lies. That is all it
  guards. It still runs in the page's own world, so a page can still:
  - learn the property's name, which hides from `for…in` and
    `Object.keys` but not `Object.getOwnPropertyNames(window)`, and, as
    the name lasts the run, plant a fake under it in a later document (one
    it navigates to, or another tab's at a site it controls) before that
    document's first snapshot;
  - change any built-in before the first snapshot in it (the script
    captures what it finds), `Reflect.apply` included;
  - add a capturing `pagehide` listener on `window` before the first
    snapshot in it, which runs before the script's and can call
    `stopImmediatePropagation()`: the page then keeps its refs when the
    back/forward cache brings it back, each finding the element it did, in
    the same document ([#35](https://github.com/bigx333/glitched/issues/35));
  - change what MyGo's eval wrapper (`evalBody`, in its `bridge.go`), which
    runs around the script, goes through: `JSON.stringify`, which it
    returns the answer through, and the `toJSON` that looks up on the
    answer's outer object; its `await`, which a page changing
    `Promise.prototype.constructor`, with a `then` of its own, can make
    resolve to another answer; and, on its catch path, the global
    `String` it turns an error's message into a string with;
  - find the property and call the script itself, as a snapshot of its
    own that moves the refs.
  Each but the `pagehide` listener can make a ref find an element of the
  page's choosing and `Verify` pass, as the page answers both. The script,
  and the wrapper `EvalContext` runs it in, should run in an isolated
  world: WKWebView's content worlds, and DevTools'
  `Page.createIsolatedWorld` on WebView2 and CEF (Next, 2; agents act
  before it, Agents).
- **Single-page routes:** a ref survives `history.pushState`,
  `replaceState` and fragment changes, which aren't navigations: a node a
  framework reuses on the new route, with the same role and name, still
  passes `Verify` (Next, 3).

### Where the design comes from

The targeting, and the tools to come, follow the design of a prior
internal browser automation engine, which drives CEF over DevTools. No
code was copied from it. What Glitched takes:

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

## Known issues

- **`isLocal` (Nelson's, `preview.go`), older than the browser and not
  ours to fix:** it tells local addresses by their prefixes, and reads the
  host without taking a userinfo off it, so public hosts count as local:
  names such as `10.com`, `192.168.com` and `127.evil.example`, and an
  address such as `127.0.0.1@example.com`, whose host is `example.com`.
  Typed without a scheme, each loads over plain http, to a public host
  (and shows the dev server's "Nothing running" page when it fails).
  Meanwhile `172.16.0.0/12` and link-local addresses don't count, and it
  doesn't normalise a host's case or trailing dot. For Nelson; the browser
  leaves it as it is.

## Agents

Claude Code runs in threads get the browser as an MCP server named
`glitched`, all in Go:

```
 claude ──stdio MCP──▶ glitched browser-mcp -dir D ──browser.sock──▶ the app (agentbrowser.go)
 (started by the engine,                (internal/browsermcp)          tabs, pages, webtarget
  --mcp-config, one per run)
```

- **The engine**, `glitched serve` or the app's own, running Claude Code
  (`daemon.Config.WithClaude`), writes `browser-mcp.json` in its state
  directory as it starts, `{"mcpServers":{"glitched":{"type":"stdio",
  "command":"<the program running>","args":["browser-mcp","-dir","<the
  directory>"]}}}`, and gives each run `--mcp-config` that file, beside
  the user's own servers (`claude.Adapter.MCPConfig`). A file, not JSON on
  the command line, which a `claude.cmd` shim on Windows would hand to
  cmd.exe to unquote. Both the CLI and the app answer `browser-mcp`; the
  app does so with no window.
- **`glitched browser-mcp`** speaks MCP on stdio (initialize, tools/list,
  tools/call, ping) and passes each call to the app on the state
  directory's `browser.sock`, a line of JSON each way. It knows nothing of
  tabs. A call the client cancels (`notifications/cancelled`), or one
  still running as the client goes, ends in the app too, and a call the
  view hasn't run yet does nothing. With no app open the tools are still listed, and each says the
  app isn't open; a socket it may not use says so instead.
- **The app** listens on `browser.sock` as its window opens; nothing
  authenticates a caller, so the state directory's mode (0700) is the
  gate, as for `engine.sock`. A socket a dead app left is replaced; one
  another app answers on is left alone.
- **Tools:** `browser_tabs` (list, new, select, close),
  `browser_navigate` (an http or https address, or back, forward,
  reload), `browser_snapshot` (`full` for structure), `browser_click` and
  `browser_type` (`submit` presses Enter) by ref. A ref names its tab by
  its snapshot's epoch, so the act tools take no tab. Each tool that loads
  or acts shows the pane and its tab, so the user sees what the agent
  does, waits for the page to load (15 seconds at most), and answers with
  the page's next snapshot. They go through Claude Code's permissions, as
  `mcp__glitched__browser_*`: in Ask mode, the thread's approval card.
- **Actions** run in the page script (`act`), the same on every engine.
  It resolves the ref and checks the element is still the role and name
  the snapshot gave it, shown and enabled, and not what a user couldn't
  use: an `inert` element, or one behind a modal `<dialog>` (the topmost
  modal escapes the inertness around it), in its own document or around a
  frame it is in. Then:
  - **Click:** it scrolls the element to the middle of the
    view at once, smooth scrolling or not, and aims at the middle, then
    the quarters, of each of its boxes, those in view first (a link
    wrapping onto two lines has two, a ring a hole; an element with no box
    of its own, what is in it), then its labels (a field hidden for a
    styled one is clicked through its label), then near its boxes' edges
    (one mostly clipped). The first point where what shows is the
    element, what is in it, or its label, takes the pointer and mouse
    events (down, up, click), sent to what shows there, as a user's
    would be. Where none is, it says what covers it (a banner, a dialog's
    backdrop), or that it takes no clicks where it shows
    (`pointer-events: none`, an ancestor's overlay). The focus stays where
    it is if the page takes `mousedown`. The events come once the script
    has answered, as a message, which a page out of sight doesn't hold
    back as it does timers, so a page leaving or a dialog doesn't take the
    answer with it.
  - **Type:** the prototype's value setter, then `input` and `change`, so
    frameworks tracking the value on the element, as React does, see it;
    an editor through `insertText`. A read-only field takes nothing; one
    that keeps only some values, as a date's, says what it took and
    keeps what it had;
    dropdowns, file, colour and other pickers say the tools can't fill
    them yet.
  - **Enter** (`submit`): `keydown`, `keypress` and `keyup`; unless the
    page takes one of the first two, the form submits as a browser's
    would, once the script has answered: its first default button, image
    or not, clicked, or with none, the form submitted if the field is its
    only one Enter submits from.
  Its events are the script's: `isTrusted` is false. A page that stops
  answering, as one showing `alert` or `confirm`, is said to be maybe
  showing a dialog, and the tabs a page opens as it acts are named in the
  answer.
- **Not yet isolated** (Next, 2): the script runs in the page's world, so a
  page that set out to can still move where an action lands, within
  itself. What it can't do is reach past itself, as each action acts on
  the page's own elements; and what a page says reaches the agent as a
  tool result, which can be text written to steer it.

## Next

1. **More tools:** `browser_select`, `browser_scroll`, `browser_wait_for`,
   `browser_screenshot`; a tab leased to the thread that opened it, as
   T3 Code's preview automation pins a session to its host; trusted input
   on WebView2 and CEF through DevTools.
2. **Isolation** of the script and of the wrapper `EvalContext` runs it in,
   in a world of their own, as above, which agents act without for now.
   MyGo needs a hook for it (WKWebView's content worlds, WebView2's DevTools).
3. **Single-page apps:** MyGo reports no `history.pushState`,
   `replaceState` or fragment navigation, so the address goes stale in a
   Vite app moving between routes, and refs survive them (above). A
   document-start script reporting them, or MyGo events, fixes both: the
   tab hears of the route, and the page script drops its refs.
4. **Cross-origin frames**, through the engine's frame tree where it has
   one.

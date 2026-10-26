// The page side of webtarget, the same on every engine: a snapshot of what
// a page shows, as lines of roles and names, each element an agent can act
// on with a ref ("s<epoch>e<n>") that resolve finds again while the
// snapshot is the page's latest. Evaluated as the body of an async
// function, with args, {key, op, epoch, interactive, maxRefs, maxChars,
// ref}, prepended by Go.
//
// Its state is private to the functions installed under args.key, a name
// random to each run of the app, frozen. That keeps a page from planting a
// fake before the first snapshot or replacing it after, but it runs in the
// page's own world: a page that changed the built-ins it uses, or calls it
// itself, can still mislead it. Isolated worlds will close that before
// agents act on pages (docs/browser.md).
const api = window[args.key] || install(args.key);
if (args.op === "snapshot") return api.snapshot(args);
if (args.op === "describe") return api.describe(args.ref);
throw new Error(`webtarget: unknown op ${args.op}`);

function install(key) {
  let epoch = 0;
  let refs = [];

  const INTERACTIVE = new Set([
    "button", "link", "textbox", "searchbox", "checkbox", "radio", "switch",
    "combobox", "listbox", "option", "menuitem", "menuitemcheckbox",
    "menuitemradio", "tab", "slider", "spinbutton", "treeitem",
  ]);
  // Roles named by their content, as ARIA's "name from content".
  const FROM_CONTENT = new Set([
    "button", "link", "heading", "option", "menuitem", "menuitemcheckbox",
    "menuitemradio", "tab", "treeitem", "cell", "columnheader", "rowheader",
    "listitem", "switch", "checkbox", "radio",
  ]);
  const LANDMARK = { NAV: "navigation", MAIN: "main", ASIDE: "complementary", FORM: "form", DIALOG: "dialog", TABLE: "table", UL: "list", OL: "list", LI: "listitem" };

  function role(el) {
    const explicit = el.getAttribute("role");
    if (explicit) {
      const r = explicit.trim().split(/\s+/)[0];
      return r === "presentation" || r === "none" ? null : r;
    }
    // SVG's tag names are lower case.
    const tag = el.tagName.toUpperCase();
    switch (tag) {
      case "A": return el.hasAttribute("href") || el.hasAttribute("xlink:href") ? "link" : null;
      case "BUTTON": case "SUMMARY": return "button";
      case "TEXTAREA": return "textbox";
      case "SELECT": return el.multiple || el.size > 1 ? "listbox" : "combobox";
      case "OPTION": return "option";
      case "IMG": return el.getAttribute("alt") ? "img" : null;
      case "H1": case "H2": case "H3": case "H4": case "H5": case "H6": return "heading";
      case "INPUT":
        switch ((el.type || "text").toLowerCase()) {
          case "hidden": return null;
          case "button": case "submit": case "reset": case "image": return "button";
          case "checkbox": return "checkbox";
          case "radio": return "radio";
          case "range": return "slider";
          case "number": return "spinbutton";
          case "search": return "searchbox";
          default: return "textbox";
        }
    }
    if (el.isContentEditable && el.parentElement && !el.parentElement.isContentEditable) return "textbox";
    return LANDMARK[tag] || null;
  }

  function clean(s, n = 80) {
    s = (s || "").replace(/\s+/g, " ").trim();
    return s.length > n ? s.slice(0, n - 1) + "…" : s;
  }

  function name(el, r) {
    const by = el.getAttribute("aria-labelledby");
    if (by) {
      // Ids are scoped to the shadow root the element is in.
      const root = el.getRootNode();
      const find = (id) => (root.getElementById ? root.getElementById(id) : null) || el.ownerDocument.getElementById(id);
      const s = by.split(/\s+/).map((id) => find(id)?.textContent || "").join(" ");
      if (clean(s)) return clean(s);
    }
    const label = el.getAttribute("aria-label");
    if (clean(label)) return clean(label);
    if (el.labels && el.labels.length) return clean([...el.labels].map((l) => l.textContent).join(" "));
    const tag = el.tagName.toUpperCase();
    if (tag === "IMG" || (tag === "INPUT" && el.type === "image")) return clean(el.getAttribute("alt"));
    if (tag === "INPUT" && /^(button|submit|reset)$/i.test(el.type)) return clean(el.value || el.type);
    if (FROM_CONTENT.has(r)) {
      const nests = r === "treeitem" || r === "listitem" || r.startsWith("menuitem");
      const s = clean(nests ? ownText(el) : el.innerText ?? el.textContent);
      if (s) return s;
      // An icon's: an image's alt text, or a label inside.
      const inner = el.querySelector("img[alt], [aria-label], svg title");
      const t = inner && clean(inner.getAttribute("alt") || inner.getAttribute("aria-label") || inner.textContent);
      if (t) return t;
    }
    return clean(el.getAttribute("title") || el.getAttribute("placeholder"));
  }

  // shown says how an element shows: GONE, with nothing in it; THROUGH,
  // as display: contents, with no box of its own but what is in it; or
  // SHOWN. What has no box is gone: display: none, a closed <details>'s
  // content, light DOM no slot takes, content-visibility: hidden.
  const GONE = 0, THROUGH = 1, SHOWN = 2;
  function shown(el, style, inSelect) {
    if (el.hidden || el.getAttribute("aria-hidden") === "true") return GONE;
    // A dropdown's options have no box until it opens.
    if (inSelect) return SHOWN;
    if (style.display === "contents") return THROUGH;
    if (el.checkVisibility) return el.checkVisibility() ? SHOWN : GONE;
    return style.display === "none" ? GONE : SHOWN;
  }

  // ownText is an element's text without what's in the groups, menus and
  // controls inside it, as a tree item's own name, not its children's.
  function ownText(el) {
    let s = "";
    for (const n of el.childNodes) {
      if (n.nodeType === 3) s += n.textContent;
      else if (n.nodeType === 1) {
        const r = role(n);
        if (r && (INTERACTIVE.has(r) || r === "group" || r === "menu" || r === "tree" || r === "list")) continue;
        s += " " + ownText(n);
      }
    }
    return s;
  }
  function level(el) {
    const m = /^H([1-6])$/.exec(el.tagName.toUpperCase());
    return el.getAttribute("aria-level") || (m ? m[1] : "2");
  }

  function attrs(el, r) {
    const out = [];
    if (r === "heading") out.push(`level=${level(el)}`);
    if (el.getAttribute("aria-disabled") === "true" || el.matches(":disabled")) out.push("disabled");
    if (el.checked || el.getAttribute("aria-checked") === "true") out.push("checked");
    if (el.getAttribute("aria-expanded") === "true") out.push("expanded");
    if (el.getAttribute("aria-selected") === "true" || (el.tagName.toUpperCase() === "OPTION" && el.selected)) out.push("selected");
    return out.map((a) => ` [${a}]`).join("");
  }

  function snapshot(args) {
    epoch = args.epoch;
    refs = [];
    const lines = [], elements = [];
    let chars = 0, truncated = "";
    const fits = (depth, line) => chars + 2 * depth + line.length + 1 <= args.maxChars;
    const cut = () => {
      truncated = `- … truncated at ${args.maxChars} characters; snapshot a smaller part of the page`;
    };
    const emit = (depth, line) => {
      lines.push("  ".repeat(depth) + line);
      chars += 2 * depth + line.length + 1;
    };
    // inSelect is set inside a dropdown, whose options show without refs:
    // they have no box to act on until it opens; an agent picks one
    // through the dropdown.
    const walk = (node, depth, inSelect) => {
      for (let el = node.firstElementChild; el && !truncated; el = el.nextElementSibling) {
        if (/^(SCRIPT|STYLE|NOSCRIPT|TEMPLATE|HEAD)$/i.test(el.tagName)) continue;
        const style = getComputedStyle(el);
        const how = shown(el, style, inSelect);
        if (how === GONE) continue;
        // Hidden by visibility, an element may show what's in it.
        const r = how === SHOWN && (style.visibility === "visible" || inSelect) ? role(el) : null;
        let d = depth;
        const acts = r && INTERACTIVE.has(r) && !inSelect;
        if (r && (acts || !args.interactive || r === "heading" || inSelect)) {
          let line = `- ${r}`;
          const n = name(el, r);
          if (n) line += ` ${JSON.stringify(n)}`;
          // A link with no name says where it goes.
          else if (r === "link") line += ` [url=${clean(el.getAttribute("href") || el.getAttribute("xlink:href"), 120)}]`;
          line += attrs(el, r);
          if (acts) {
            if (refs.length >= args.maxRefs) {
              truncated = `- … truncated at ${args.maxRefs} refs; snapshot a smaller part of the page`;
              return;
            }
            line += ` [ref=s${epoch}e${refs.length + 1}]`;
          }
          if (!fits(depth, line)) return cut();
          if (acts) {
            refs.push(new WeakRef(el));
            elements.push({ ref: `s${epoch}e${refs.length}`, role: r, name: n });
          }
          emit(depth, line);
          if (!args.interactive || inSelect) d = depth + 1;
        }
        if (el.tagName.toUpperCase() === "IFRAME") {
          let doc = null, reached = true;
          try { doc = el.contentDocument; } catch { reached = false; }
          if (doc?.body) walk(doc.body, d, false);
          else {
            const line = reached && doc ? "- iframe [loading]" : "- iframe [cross-origin, not reached]";
            if (!fits(d, line)) return cut();
            emit(d, line);
          }
          continue;
        }
        const select = inSelect || r === "combobox" && el.tagName.toUpperCase() === "SELECT";
        if (el.shadowRoot) walk(el.shadowRoot, d, select);
        // What's in an element to act on, as a tree's or a menu's items.
        walk(el, d, select);
      }
    };
    walk(document.body || document.documentElement, 0, false);
    if (truncated) lines.push(truncated);
    return { epoch, url: location.href, title: document.title, refs: refs.length, truncated: !!truncated, text: lines.join("\n"), elements };
  }

  // resolve finds a ref's element, or says why it can't, as the engine does.
  function resolve(ref) {
    const m = /^s(\d+)e(\d+)$/.exec(ref);
    if (!m) throw new Error(`${ref} is not a ref: refs look like s12e3`);
    if (!epoch) throw new Error(`${ref} is from before the page navigated or reloaded; take a new snapshot`);
    if (+m[1] !== epoch) throw new Error(`${ref} is from snapshot s${m[1]}, not the latest, s${epoch}; take a new snapshot`);
    const el = refs[+m[2] - 1]?.deref();
    if (!el || !el.isConnected) throw new Error(`${ref} is gone: the page re-rendered it; take a new snapshot`);
    return el;
  }

  // describe says what a ref's element is, and where it shows in the top
  // frame's viewport, through the frames it is in.
  function describe(ref) {
    const el = resolve(ref);
    const r = role(el);
    const b = el.getBoundingClientRect();
    let x = b.x, y = b.y;
    for (let w = el.ownerDocument.defaultView; w && w !== window && w.frameElement; w = w.frameElement.ownerDocument.defaultView) {
      const f = w.frameElement, fb = f.getBoundingClientRect(), s = getComputedStyle(f);
      x += fb.x + f.clientLeft + parseFloat(s.paddingLeft);
      y += fb.y + f.clientTop + parseFloat(s.paddingTop);
    }
    return { role: r || "", name: name(el, r), box: { x, y, width: b.width, height: b.height } };
  }

  const api = Object.freeze({ snapshot, describe });
  Object.defineProperty(window, key, { value: api, writable: false, configurable: false, enumerable: false });
  return api;
}

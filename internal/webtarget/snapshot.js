// The page side of webtarget, the same on every engine: a snapshot of what
// a page shows, as lines of roles and names, each element an agent can act
// on with a ref ("s<epoch>e<n>") that resolve finds again while the
// snapshot is the page's latest. Evaluated as the body of an async
// function, with args, {key, op, epoch, interactive, maxRefs, maxChars,
// maxTitle, maxURL, ref}, prepended by Go.
//
// Its state is private to the functions installed under args.key, a name
// random to each run of the app, frozen. Once installed, it calls no
// built-in and reads no prototype the page can change: every function and
// getter it uses is the one it found as it was installed, called through
// the Reflect.apply it found; it parses with no regular expression,
// writes its arrays with the Object.defineProperty it found and keeps its
// refs and tables in objects with no prototype. That keeps a page from
// replacing it after the first snapshot, or patching a built-in after it
// (RegExp.prototype.exec, an index setter on Array.prototype, a getter of
// Node.prototype, …) to redirect a ref or change what it says. The name
// is no secret from a page that looks (Object.getOwnPropertyNames(window)
// lists it), and lasts the run, so a page that learned it can plant a
// fake in a later document before that document's first snapshot. It runs
// in the page's own world, which a page could have changed before the
// first snapshot, and where it can call it itself; isolated worlds will
// close that before agents act (docs/browser.md).
const api = window[args.key] || install(args.key);
if (args.op === "snapshot") return api.snapshot(args);
if (args.op === "describe") return api.describe(args.ref);
throw new Error(`webtarget: unknown op ${args.op}`);

function install(key) {
  let epoch = 0;
  // refs are the latest snapshot's, by number from 0: each its element,
  // and the frames it is in, from the top, each with the document it
  // showed (snapshot); count is how many.
  let refs = null, count = 0;

  // The built-ins as they are now, which the page patching later can't
  // change for the script: functions and getters, each called through
  // apply, never looked up again.
  const apply = Reflect.apply;
  const create = Object.create;
  const defineProperty = Object.defineProperty;
  const setPrototypeOf = Object.setPrototypeOf;
  const ownDescriptor = Object.getOwnPropertyDescriptor;
  const freeze = Object.freeze;
  const Err = Error;
  const WR = WeakRef;
  const num = parseFloat;
  const json = JSON, stringify = JSON.stringify;
  const fn = (o, k) => o && o.prototype[k];
  const getter = (o, k) => {
    const d = o && ownDescriptor(o.prototype, k);
    return d && d.get;
  };
  const B = {
    __proto__: null,
    deref: fn(WeakRef, "deref"),
    attr: fn(Element, "getAttribute"),
    hasAttr: fn(Element, "hasAttribute"),
    matches: fn(Element, "matches"),
    rect: fn(Element, "getBoundingClientRect"),
    query: fn(Element, "querySelector"),
    checkVisibility: fn(Element, "checkVisibility"),
    localName: getter(Element, "localName"),
    namespace: getter(Element, "namespaceURI"),
    shadowRoot: getter(Element, "shadowRoot"),
    firstChild: getter(Element, "firstElementChild"),
    fragmentFirstChild: getter(DocumentFragment, "firstElementChild"),
    nextSibling: getter(Element, "nextElementSibling"),
    clientLeft: getter(Element, "clientLeft"),
    clientTop: getter(Element, "clientTop"),
    connected: getter(Node, "isConnected"),
    ownerDocument: getter(Node, "ownerDocument"),
    parent: getter(Node, "parentElement"),
    childNodes: getter(Node, "childNodes"),
    nodeType: getter(Node, "nodeType"),
    text: getter(Node, "textContent"),
    rootNode: fn(Node, "getRootNode"),
    hidden: getter(HTMLElement, "hidden"),
    editable: getter(HTMLElement, "isContentEditable"),
    innerText: getter(HTMLElement, "innerText"),
    inputType: getter(HTMLInputElement, "type"),
    inputValue: getter(HTMLInputElement, "value"),
    checked: getter(HTMLInputElement, "checked"),
    multiple: getter(HTMLSelectElement, "multiple"),
    size: getter(HTMLSelectElement, "size"),
    selected: getter(HTMLOptionElement, "selected"),
    contentDocument: getter(HTMLIFrameElement, "contentDocument"),
    body: getter(Document, "body"),
    documentElement: getter(Document, "documentElement"),
    url: getter(Document, "URL"),
    readyState: getter(Document, "readyState"),
    designMode: getter(Document, "designMode"),
    title: getter(Document, "title"),
    docById: fn(Document, "getElementById"),
    fragmentById: fn(DocumentFragment, "getElementById"),
    listLength: getter(NodeList, "length"),
    item: fn(NodeList, "item"),
    prop: fn(CSSStyleDeclaration, "getPropertyValue"),
    x: getter(DOMRectReadOnly, "x"),
    y: getter(DOMRectReadOnly, "y"),
    width: getter(DOMRectReadOnly, "width"),
    height: getter(DOMRectReadOnly, "height"),
    style: window.getComputedStyle,
    join: fn(Array, "join"),
    slice: fn(String, "slice"),
    upper: fn(String, "toUpperCase"),
    lower: fn(String, "toLowerCase"),
    repeat: fn(String, "repeat"),
  };
  // Where each labelable element keeps its labels.
  const LABELS = {
    __proto__: null,
    INPUT: getter(HTMLInputElement, "labels"),
    SELECT: getter(HTMLSelectElement, "labels"),
    TEXTAREA: getter(HTMLTextAreaElement, "labels"),
    BUTTON: getter(HTMLButtonElement, "labels"),
    METER: getter(window.HTMLMeterElement, "labels"),
    OUTPUT: getter(window.HTMLOutputElement, "labels"),
    PROGRESS: getter(window.HTMLProgressElement, "labels"),
  };
  const get = (g, o) => apply(g, o, []);
  // opt is what a getter an engine may lack returns, undefined without it.
  const opt = (g, o) => (g ? apply(g, o, []) : undefined);
  const deref = (w) => apply(B.deref, w, []);
  const attr = (el, n) => apply(B.attr, el, [n]);
  const hasAttr = (el, n) => apply(B.hasAttr, el, [n]);
  const matches = (el, q) => apply(B.matches, el, [q]);
  const rect = (el) => apply(B.rect, el, []);
  const connected = (el) => get(B.connected, el);
  const styleOf = (el) => apply(B.style, window, [el]);
  const prop = (style, k) => apply(B.prop, style, [k]);
  const text = (n) => get(B.text, n);
  const isHTML = (el) => get(B.namespace, el) === "http://www.w3.org/1999/xhtml";
  // tagOf is an element's local name, upper case, as the tables are keyed.
  // An HTML element's interface follows its local name, case and all: an
  // XHTML <Input/> is no input, and none of its getters work on it, so
  // its tag is "" unless it is the lower case of the tag.
  const tagOf = (el) => {
    const n = get(B.localName, el), tag = apply(B.upper, n, []);
    return isHTML(el) && lower(tag) !== n ? "" : tag;
  };
  const slice = (s, a, b) => apply(B.slice, s, [a, b]);
  const lower = (s) => apply(B.lower, s, []);
  const quote = (s) => apply(stringify, json, [s]);
  // define sets o[k] as its own property, past any setter a page put on a
  // prototype; put appends to an array so.
  const define = (o, k, v) => defineProperty(o, k, { __proto__: null, value: v, writable: true, enumerable: true, configurable: true });
  const put = (a, v) => define(a, a.length, v);
  const item = (list, i) => apply(B.item, list, [i]);
  const listLength = (list) => get(B.listLength, list);

  const INTERACTIVE = {
    __proto__: null, button: true, link: true, textbox: true, searchbox: true,
    checkbox: true, radio: true, switch: true, combobox: true, listbox: true,
    option: true, menuitem: true, menuitemcheckbox: true, menuitemradio: true,
    tab: true, slider: true, spinbutton: true, treeitem: true,
  };
  // Roles named by their content, as ARIA's "name from content".
  const FROM_CONTENT = {
    __proto__: null, button: true, link: true, heading: true, option: true,
    menuitem: true, menuitemcheckbox: true, menuitemradio: true, tab: true,
    treeitem: true, cell: true, columnheader: true, rowheader: true,
    listitem: true, switch: true, checkbox: true, radio: true,
  };
  const LANDMARK = { __proto__: null, NAV: "navigation", MAIN: "main", ASIDE: "complementary", FORM: "form", DIALOG: "dialog", TABLE: "table", UL: "list", OL: "list", LI: "listitem" };
  const SKIP = { __proto__: null, SCRIPT: true, STYLE: true, NOSCRIPT: true, TEMPLATE: true, HEAD: true };
  const LEVEL = { __proto__: null, H1: "1", H2: "2", H3: "3", H4: "4", H5: "5", H6: "6" };
  const NAMED_BY_VALUE = { __proto__: null, button: true, submit: true, reset: true };
  // What a regular expression's \s matches, by character.
  const SPACE = create(null);
  for (const c of [9, 10, 11, 12, 13, 32, 0xa0, 0x1680, 0x2000, 0x2001, 0x2002, 0x2003, 0x2004, 0x2005, 0x2006,
    0x2007, 0x2008, 0x2009, 0x200a, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000, 0xfeff]) {
    SPACE[String.fromCharCode(c)] = true;
  }

  // words splits s at white space.
  function words(s) {
    const out = [];
    let w = "";
    for (let i = 0; i < s.length; i++) {
      if (SPACE[s[i]] === true) {
        if (w) put(out, w);
        w = "";
      } else w += s[i];
    }
    if (w) put(out, w);
    return out;
  }

  function role(el) {
    const explicit = attr(el, "role");
    if (explicit) {
      // Clipped, as Go takes no longer role (webtarget.go).
      const ws = words(explicit);
      const r = ws.length ? slice(ws[0], 0, 80) : "";
      return r === "presentation" || r === "none" ? null : r;
    }
    // SVG's tag names are lower case.
    const tag = tagOf(el), html = isHTML(el);
    switch (tag) {
      case "A": return hasAttr(el, "href") || hasAttr(el, "xlink:href") ? "link" : null;
      case "BUTTON": case "SUMMARY": return "button";
      case "TEXTAREA": return "textbox";
      case "SELECT":
        if (html) return get(B.multiple, el) || get(B.size, el) > 1 ? "listbox" : "combobox";
        break;
      case "OPTION": return "option";
      case "IMG": return attr(el, "alt") ? "img" : null;
      case "H1": case "H2": case "H3": case "H4": case "H5": case "H6": return "heading";
      case "INPUT":
        if (!html) break;
        switch (lower(get(B.inputType, el) || "text")) {
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
    // A document's body edited whole, as an editor's frame's in design
    // mode, or contenteditable, is a text box itself.
    if (html && tag === "BODY" && (opt(B.editable, el) || opt(B.designMode, get(B.ownerDocument, el)) === "on")) return "textbox";
    if (html && opt(B.editable, el)) {
      const p = get(B.parent, el);
      if (!(p && isHTML(p) && opt(B.editable, p))) return "textbox";
    }
    return LANDMARK[tag] || null;
  }

  // clean collapses white space, as a regular expression's \s+ would, trims,
  // and clips to n.
  function clean(s, n = 80) {
    s = s == null ? "" : "" + s;
    let out = "", gap = false;
    for (let i = 0; i < s.length && out.length <= n; i++) {
      if (SPACE[s[i]] === true) gap = out.length > 0;
      else {
        if (gap) out += " ";
        out += s[i];
        gap = false;
      }
    }
    return out.length > n ? slice(out, 0, n - 1) + "…" : out;
  }

  // byID finds an element by id in a document or a shadow root.
  function byID(root, id) {
    switch (get(B.nodeType, root)) {
      case 9: return apply(B.docById, root, [id]);
      case 11: return B.fragmentById ? apply(B.fragmentById, root, [id]) : null;
    }
    return null;
  }

  function name(el, r) {
    const by = attr(el, "aria-labelledby");
    if (by) {
      // Ids are scoped to the shadow root the element is in.
      const root = apply(B.rootNode, el, []), doc = get(B.ownerDocument, el);
      const ids = words(by);
      let s = "";
      for (let i = 0; i < ids.length; i++) {
        const found = byID(root, ids[i]) || byID(doc, ids[i]);
        s += " " + (found ? text(found) || "" : "");
      }
      if (clean(s)) return clean(s);
    }
    const label = attr(el, "aria-label");
    if (clean(label)) return clean(label);
    const tag = tagOf(el), html = isHTML(el);
    const labels = html && LABELS[tag] ? get(LABELS[tag], el) : null;
    if (labels && listLength(labels)) {
      let s = "";
      for (let i = 0, n = listLength(labels); i < n; i++) s += " " + text(item(labels, i));
      return clean(s);
    }
    const type = html && tag === "INPUT" ? lower(get(B.inputType, el)) : "";
    if (tag === "IMG" || type === "image") return clean(attr(el, "alt"));
    if (NAMED_BY_VALUE[type] === true) return clean(get(B.inputValue, el) || type);
    if (FROM_CONTENT[r] === true) {
      const nests = r === "treeitem" || r === "listitem" || slice(r, 0, 8) === "menuitem";
      const s = clean(nests ? ownText(el) : html && B.innerText ? get(B.innerText, el) : text(el));
      if (s) return s;
      // An icon's: an image's alt text, or a label inside.
      const inner = apply(B.query, el, ["img[alt], [aria-label], svg title"]);
      const t = inner && clean(attr(inner, "alt") || attr(inner, "aria-label") || text(inner));
      if (t) return t;
    }
    return clean(attr(el, "title") || attr(el, "placeholder"));
  }

  // shown says how an element shows: GONE, with nothing in it; THROUGH,
  // as display: contents, with no box of its own but what is in it; or
  // SHOWN. What has no box is gone: display: none, a closed <details>'s
  // content, light DOM no slot takes, content-visibility: hidden.
  const GONE = 0, THROUGH = 1, SHOWN = 2;
  function shown(el, style, inSelect) {
    if ((isHTML(el) && get(B.hidden, el)) || attr(el, "aria-hidden") === "true") return GONE;
    // A dropdown's options have no box until it opens.
    if (inSelect) return SHOWN;
    const display = prop(style, "display");
    if (display === "contents") return THROUGH;
    if (B.checkVisibility) return apply(B.checkVisibility, el, []) ? SHOWN : GONE;
    return display === "none" ? GONE : SHOWN;
  }

  // ownText is an element's text without what's in the groups, menus and
  // controls inside it, as a tree item's own name, not its children's.
  function ownText(el) {
    let s = "";
    const nodes = get(B.childNodes, el);
    for (let i = 0, n = listLength(nodes); i < n; i++) {
      const node = item(nodes, i);
      const type = get(B.nodeType, node);
      if (type === 3) s += text(node);
      else if (type === 1) {
        const r = role(node);
        if (r && (INTERACTIVE[r] === true || r === "group" || r === "menu" || r === "tree" || r === "list")) continue;
        s += " " + ownText(node);
      }
    }
    return s;
  }
  function level(el) {
    return attr(el, "aria-level") || LEVEL[tagOf(el)] || "2";
  }

  function attrs(el, r) {
    const tag = tagOf(el), html = isHTML(el);
    let out = "";
    if (r === "heading") out += ` [level=${level(el)}]`;
    if (attr(el, "aria-disabled") === "true" || matches(el, ":disabled")) out += " [disabled]";
    if ((html && tag === "INPUT" && get(B.checked, el)) || attr(el, "aria-checked") === "true") out += " [checked]";
    if (attr(el, "aria-expanded") === "true") out += " [expanded]";
    if (attr(el, "aria-selected") === "true" || (html && tag === "OPTION" && get(B.selected, el))) out += " [selected]";
    return out;
  }

  function snapshot(args) {
    epoch = args.epoch;
    refs = create(null);
    count = 0;
    // elements, which it answers with, has no prototype either, so the
    // wrapper's JSON.stringify finds no toJSON on Array.prototype; it is
    // an array still, which JSON.stringify writes as one.
    const lines = [], elements = setPrototypeOf([], null);
    let chars = 0, truncated = "";
    const fits = (depth, line) => chars + 2 * depth + line.length + 1 <= args.maxChars;
    const cut = () => {
      truncated = `- … truncated at ${args.maxChars} characters; snapshot a smaller part of the page`;
    };
    const emit = (depth, line) => {
      put(lines, apply(B.repeat, "  ", [depth]) + line);
      chars += 2 * depth + line.length + 1;
    };
    // walk visits what is in node, an element or a shadow root, in the
    // frames of chain, the iframes from the top it is in, each with the
    // document it shows.
    const walk = (node, depth, inSelect, chain) => {
      const first = get(B.nodeType, node) === 11 ? B.fragmentFirstChild : B.firstChild;
      for (let el = get(first, node); el && !truncated; el = get(B.nextSibling, el)) visit(el, depth, inSelect, chain);
    };
    // inSelect is set inside a dropdown, whose options show without refs:
    // they have no box to act on until it opens; an agent picks one
    // through the dropdown.
    const visit = (el, depth, inSelect, chain) => {
      const tag = tagOf(el), html = isHTML(el);
      if (SKIP[tag] === true) return;
      const style = styleOf(el);
      const how = shown(el, style, inSelect);
      if (how === GONE) return;
      // Hidden by visibility, an element may show what's in it.
      const visible = prop(style, "visibility") === "visible";
      const r = how === SHOWN && (visible || inSelect) ? role(el) : null;
      let d = depth;
      const acts = r && INTERACTIVE[r] === true && !inSelect;
      if (r && (acts || !args.interactive || r === "heading" || inSelect)) {
        let line = `- ${r}`;
        const n = name(el, r);
        if (n) line += ` ${quote(n)}`;
        // A link with no name says where it goes.
        else if (r === "link") line += ` [url=${quote(clean(attr(el, "href") || attr(el, "xlink:href"), 120))}]`;
        line += attrs(el, r);
        if (acts) {
          if (count >= args.maxRefs) {
            truncated = `- … truncated at ${args.maxRefs} refs; snapshot a smaller part of the page`;
            return;
          }
          line += ` [ref=s${epoch}e${count + 1}]`;
        }
        if (!fits(depth, line)) return cut();
        if (acts) {
          define(refs, count, { __proto__: null, el: new WR(el), chain });
          count++;
          put(elements, { __proto__: null, ref: `s${epoch}e${count}`, role: r, name: n });
        }
        emit(depth, line);
        if (!args.interactive || inSelect) d = depth + 1;
      }
      if (html && tag === "IFRAME") {
        // A frame shows its page once it is there: not while hidden by
        // visibility, nor while its first page loads, at about:blank.
        if (!visible) return;
        let doc = null, reached = true;
        try { doc = get(B.contentDocument, el); } catch { reached = false; }
        // Still at about:blank, a frame with an address of its own is on its
        // way; one with none is a page a script fills, as an editor's.
        const src = attr(el, "src");
        const coming = !!doc && get(B.url, doc) === "about:blank" && !!src && src !== "about:blank";
        const body = doc && get(B.body, doc);
        if (body && get(B.readyState, doc) !== "loading" && !coming) {
          const inner = [];
          for (let i = 0; i < chain.length; i++) put(inner, chain[i]);
          put(inner, { __proto__: null, frame: new WR(el), doc: new WR(doc) });
          visit(body, d, false, inner);
        } else {
          const line = reached && doc ? "- iframe [loading]" : "- iframe [cross-origin, not reached]";
          if (!fits(d, line)) return cut();
          emit(d, line);
        }
        return;
      }
      const select = inSelect || r === "combobox" && tag === "SELECT";
      const shadow = get(B.shadowRoot, el);
      if (shadow) walk(shadow, d, select, chain);
      // What's in an element to act on, as a tree's or a menu's items.
      walk(el, d, select, chain);
    };
    // A document's body is visited as an element, which an editable one is.
    visit(get(B.body, document) || get(B.documentElement, document), 0, false, []);
    if (truncated) put(lines, truncated);
    // The title and the URL are clipped beside the budget of characters,
    // which counts the lines alone.
    const clip = (s, n) => (s.length > n ? slice(s, 0, n - 1) + "…" : s);
    const url = clip(get(B.url, document), args.maxURL), title = clip(get(B.title, document), args.maxTitle);
    // With no prototype, so the promise it resolves looks up no then.
    return { __proto__: null, epoch, url, title, refs: count, truncated: !!truncated, text: apply(B.join, lines, ["\n"]), elements };
  }

  // parse reads a ref's epoch and number, as s12e3, or returns null:
  // character by character, as a regular expression's exec is the page's
  // to patch.
  function parse(ref) {
    const digit = (c) => c >= "0" && c <= "9";
    if (typeof ref !== "string" || !ref.length || ref[0] !== "s") return null;
    let i = 1, e = "", n = "";
    for (; i < ref.length && digit(ref[i]); i++) e += ref[i];
    if (!e || i >= ref.length || ref[i] !== "e") return null;
    for (i++; i < ref.length; i++) {
      if (!digit(ref[i])) return null;
      n += ref[i];
    }
    return n ? { __proto__: null, epoch: e, n: +n } : null;
  }

  const gone = (ref) => new Err(`${ref} is gone: the page re-rendered it; take a new snapshot`);

  // resolve finds a ref's element, and the frames it is in, or says why it
  // can't, as the engine does. Each frame must still show the document it
  // showed, and the element be in the innermost's: an element of a
  // document its frame navigated away from still reads as connected, in
  // that document.
  function resolve(ref) {
    const p = parse(ref);
    if (!p) throw new Err(`${ref} is not a ref: refs look like s12e3`);
    if (!epoch) throw new Err(`${ref} is from before the page navigated or reloaded; take a new snapshot`);
    if (+p.epoch !== epoch) throw new Err(`${ref} is from snapshot s${p.epoch}, not the latest, s${epoch}; take a new snapshot`);
    const rec = p.n >= 1 && p.n <= count ? refs[p.n - 1] : null;
    if (!rec) throw gone(ref);
    let doc = document;
    for (let i = 0; i < rec.chain.length; i++) {
      const f = deref(rec.chain[i].frame), d = deref(rec.chain[i].doc);
      if (!f || !connected(f) || get(B.ownerDocument, f) !== doc) throw gone(ref);
      // A frame gone to another origin's page has no document to read
      // (null, or a throw in an engine that does): it navigated too.
      let now = null;
      try { now = get(B.contentDocument, f); } catch {}
      if (!d || now !== d) {
        throw new Err(`${ref} is from before its frame navigated or reloaded; take a new snapshot`);
      }
      doc = d;
    }
    const el = deref(rec.el);
    if (!el || !connected(el) || get(B.ownerDocument, el) !== doc) throw gone(ref);
    return { __proto__: null, el, chain: rec.chain };
  }

  // scale is the scale a frame shows at on one axis: its box on screen,
  // shown, against its layout size, from its computed style s, which
  // keeps the fractions of a pixel offsetWidth rounds off. An engine gives
  // that size to six significant figures, so a scale that near 1 is 1.
  function scale(shown, s, size, start, end) {
    let laid = num(prop(s, size));
    if (prop(s, "box-sizing") !== "border-box")
      laid +=
        num(prop(s, "padding-" + start)) + num(prop(s, "padding-" + end)) +
        num(prop(s, "border-" + start + "-width")) + num(prop(s, "border-" + end + "-width"));
    const k = laid ? shown / laid : 0;
    return k > 0.99999 && k < 1.00001 ? 1 : k;
  }

  // describe says what a ref's element is, and where it shows in the top
  // frame's viewport, through the frames it is in, from the innermost out:
  // past each frame's border and padding, at the scale its box shows at
  // against its layout size, which its transforms and its ancestors' give
  // it. A frame turned, skewed or flipped is not mapped: its scale comes
  // from its bounding box, so the box is off (docs/browser.md).
  function describe(ref) {
    const found = resolve(ref), el = found.el, chain = found.chain;
    const r = role(el);
    const b = rect(el);
    let x = get(B.x, b), y = get(B.y, b), width = get(B.width, b), height = get(B.height, b);
    for (let i = chain.length - 1; i >= 0; i--) {
      const f = deref(chain[i].frame);
      if (!f) throw gone(ref);
      const fb = rect(f), s = styleOf(f);
      const sx = scale(get(B.width, fb), s, "width", "left", "right");
      const sy = scale(get(B.height, fb), s, "height", "top", "bottom");
      x = get(B.x, fb) + sx * (get(B.clientLeft, f) + num(prop(s, "padding-left")) + x);
      y = get(B.y, fb) + sy * (get(B.clientTop, f) + num(prop(s, "padding-top")) + y);
      width *= sx;
      height *= sy;
    }
    const box = { __proto__: null, x, y, width, height };
    return { __proto__: null, role: r || "", name: name(el, r), box };
  }

  // A page leaving drops its refs: the back/forward cache can bring this
  // document back, state and all, after a snapshot of another. Capturing
  // on window, it runs before any listener the page adds after it, so
  // none of those can stop it; a capturing one the page added before the
  // first snapshot can (#35). isTrusted is each event's own unforgeable
  // property, so a pagehide the page fires itself drops nothing.
  apply(fn(EventTarget, "addEventListener"), window, ["pagehide", (e) => {
    if (!e.isTrusted) return;
    epoch = 0;
    refs = null;
    count = 0;
  }, true]);

  const api = freeze({ __proto__: null, snapshot, describe });
  defineProperty(window, key, { __proto__: null, value: api, writable: false, configurable: false, enumerable: false });
  return api;
}

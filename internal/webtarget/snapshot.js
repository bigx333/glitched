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
if (args.op === "act") return api.act(args);
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
  const setter = (o, k) => {
    const d = o && ownDescriptor(o.prototype, k);
    return d && d.set;
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
    // What an agent's actions go through (act).
    dispatch: fn(EventTarget, "dispatchEvent"),
    defaultPrevented: getter(Event, "defaultPrevented"),
    focus: fn(HTMLElement, "focus"),
    scrollIntoView: fn(Element, "scrollIntoView"),
    defaultView: getter(Document, "defaultView"),
    setInputValue: setter(HTMLInputElement, "value"),
    setTextareaValue: setter(HTMLTextAreaElement, "value"),
    inputForm: getter(HTMLInputElement, "form"),
    buttonType: getter(HTMLButtonElement, "type"),
    formElements: getter(window.HTMLFormElement, "elements"),
    collectionLength: getter(HTMLCollection, "length"),
    collectionItem: fn(HTMLCollection, "item"),
    requestSubmit: fn(window.HTMLFormElement, "requestSubmit"),
    click: fn(window.HTMLElement, "click"),
    getSelection: fn(Document, "getSelection"),
    selectAllChildren: fn(window.Selection, "selectAllChildren"),
    execCommand: fn(Document, "execCommand"),
    later: window.setTimeout,
    port1: getter(window.MessageChannel, "port1"),
    port2: getter(window.MessageChannel, "port2"),
    onmessage: setter(window.MessagePort, "onmessage"),
    postMessage: fn(window.MessagePort, "postMessage"),
    clientRects: fn(Element, "getClientRects"),
    rectsLength: getter(window.DOMRectList, "length"),
    rectsItem: fn(window.DOMRectList, "item"),
    clientWidth: getter(Element, "clientWidth"),
    clientHeight: getter(Element, "clientHeight"),
    position: fn(Node, "compareDocumentPosition"),
    elementFromPoint: fn(Document, "elementFromPoint"),
    shadowFromPoint: fn(window.ShadowRoot, "elementFromPoint"),
    shadowHost: getter(window.ShadowRoot, "host"),
    activeElement: getter(Document, "activeElement"),
    inputMaxLength: getter(window.HTMLInputElement, "maxLength"),
    textareaMaxLength: getter(window.HTMLTextAreaElement, "maxLength"),
    shadowActive: getter(window.ShadowRoot, "activeElement"),
  };
  // The events actions fire, made with the constructors found as the
  // script was installed.
  const MC = window.MessageChannel;
  // soon runs f once the script has answered, as a message, which a page
  // out of sight doesn't hold back as it does its timers.
  const soon = (f) => {
    if (!MC || !B.onmessage) return apply(B.later, window, [f, 0]);
    const ch = new MC();
    apply(B.onmessage, get(B.port1, ch), [() => f()]);
    apply(B.postMessage, get(B.port2, ch), [0]);
  };
  const EV = {
    __proto__: null,
    Event, Mouse: MouseEvent, Keyboard: KeyboardEvent,
    Input: window.InputEvent || Event, Pointer: window.PointerEvent || null,
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

  // outward is where x, y of frame f's viewport shows in the viewport of
  // the document f is in, and the scale f shows at: past f's border and
  // padding, at the scale its box shows at against its layout size.
  function outward(f, x, y) {
    const fb = rect(f), s = styleOf(f);
    const sx = scale(get(B.width, fb), s, "width", "left", "right");
    const sy = scale(get(B.height, fb), s, "height", "top", "bottom");
    return {
      __proto__: null, sx, sy,
      x: get(B.x, fb) + sx * (get(B.clientLeft, f) + num(prop(s, "padding-left")) + x),
      y: get(B.y, fb) + sy * (get(B.clientTop, f) + num(prop(s, "padding-top")) + y),
    };
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
      const o = outward(f, x, y);
      x = o.x;
      y = o.y;
      width *= o.sx;
      height *= o.sy;
    }
    const box = { __proto__: null, x, y, width, height };
    return { __proto__: null, role: r || "", name: name(el, r), box };
  }

  // What act may type into: text boxes, and editors.
  const TYPEABLE = { __proto__: null, textbox: true, searchbox: true, spinbutton: true, combobox: true };

  // Fields whose Enter submits their form (HTML's implicit submission).
  const BLOCKS_IMPLICIT = {
    __proto__: null, text: true, search: true, url: true, tel: true, email: true, password: true,
    date: true, month: true, week: true, time: true, "datetime-local": true, number: true,
  };

  // press clicks a form's default button as implicit submission does: a
  // click with no place, which an image button sends as 0, 0. A click
  // made at its corner is not: WebKit takes the page's scroll off it.
  function press(button) {
    apply(B.click, button, []);
  }

  // implicit is how Enter in one of form's fields submits it, or null: a
  // click on its default button, the first that submits, unless that is
  // disabled; with none, the form submitted, if it has no other field
  // whose Enter would submit it (HTML's implicit submission).
  function implicit(form) {
    const els = get(B.formElements, form);
    let fields = 0, button = null;
    for (let i = 0, n = get(B.collectionLength, els); i < n; i++) {
      const c = apply(B.collectionItem, els, [i]), t = tagOf(c);
      const type = t === "INPUT" ? lower(get(B.inputType, c)) : t === "BUTTON" ? lower(get(B.buttonType, c)) : "";
      if (!button && ((t === "BUTTON" && type === "submit") || (t === "INPUT" && (type === "submit" || type === "image")))) button = c;
      if (t === "INPUT" && BLOCKS_IMPLICIT[type] === true) fields++;
    }
    // An image button isn't among a form's elements, but is its default
    // button where it comes first.
    const image = apply(B.query, form, ["input[type=image i]"]);
    if (image && (!button || (apply(B.position, button, [image]) & 2) !== 0)) button = image;
    if (button) {
      if (matches(button, ":disabled")) return null;
      return () => press(button);
    }
    return fields === 1 && B.requestSubmit ? () => apply(B.requestSubmit, form, []) : null;
  }

  // reveal scrolls el into the middle of the view, at once: a page
  // scrolling smoothly would leave it where it was as act reads it.
  function reveal(el) {
    try {
      apply(B.scrollIntoView, el, [{ __proto__: null, block: "center", inline: "center", behavior: "instant" }]);
    } catch {
      apply(B.scrollIntoView, el, [{ __proto__: null, block: "center", inline: "center" }]);
    }
  }

  // points are where a click on el may land, in its document's viewport:
  // the middle and the quarters of each of its boxes, those in view
  // first, as a link wrapping onto two lines has two boxes, whose middle
  // can be between them, and a ring has a hole. An element with no box of
  // its own, as a link around a block placed elsewhere, lands on what is
  // in it.
  function points(el, doc) {
    const root = get(B.documentElement, doc);
    const w = root ? get(B.clientWidth, root) : 0, h = root ? get(B.clientHeight, root) : 0;
    const inView = [], outside = [];
    const add = (n, edges) => {
      // An engine without DOMRectList (an old WebKitGTK's ClientRectList)
      // has the element's one box.
      const rs = B.rectsLength && B.rectsItem ? apply(B.clientRects, n, []) : null;
      const count = rs ? get(B.rectsLength, rs) : 1;
      for (let i = 0; i < count && inView.length + outside.length < 60; i++) {
        const r = rs ? apply(B.rectsItem, rs, [i]) : rect(n);
        const x = get(B.x, r), y = get(B.y, r), rw = get(B.width, r), rh = get(B.height, r);
        if (rw <= 0 || rh <= 0) continue;
        const at = (fx, fy) => ({ __proto__: null, x: x + rw * fx, y: y + rh * fy });
        const mid = at(0.5, 0.5);
        const list = mid.x >= 0 && mid.y >= 0 && (!w || mid.x <= w) && (!h || mid.y <= h) ? inView : outside;
        const lo = edges ? 0.05 : 0.25, hi = edges ? 0.95 : 0.75;
        if (!edges) put(list, mid);
        put(list, at(lo, lo));
        put(list, at(hi, lo));
        put(list, at(lo, hi));
        put(list, at(hi, hi));
      }
    };
    add(el, false);
    // Breadth first through what is in el, for its first box.
    const queue = [];
    for (let c = get(B.firstChild, el); c; c = get(B.nextSibling, c)) put(queue, c);
    for (let q = 0; !inView.length && !outside.length && q < queue.length && q < 256; q++) {
      add(queue[q], false);
      for (let c = get(B.firstChild, queue[q]); c; c = get(B.nextSibling, c)) put(queue, c);
    }
    // Then its labels, as a field hidden for a styled one (sr-only) is
    // clicked through its label; then near the edges of its boxes, as
    // one mostly clipped shows only there.
    const labels = isHTML(el) && LABELS[tagOf(el)] ? get(LABELS[tagOf(el)], el) : null;
    for (let j = 0, k = labels ? listLength(labels) : 0; j < k; j++) add(item(labels, j), false);
    add(el, true);
    for (let i = 0; i < outside.length; i++) put(inView, outside[i]);
    return inView;
  }

  // up is n's parent, through a shadow root to its host.
  function up(n) {
    const p = get(B.parent, n);
    if (p) return p;
    const root = apply(B.rootNode, n, []);
    return root && get(B.nodeType, root) === 11 ? get(B.shadowHost, root) : null;
  }

  // hitAt is what a click at x, y of doc would hit, through shadow roots.
  function hitAt(doc, x, y) {
    let hit = apply(B.elementFromPoint, doc, [x, y]);
    for (let i = 0; hit && i < 32; i++) {
      const sr = get(B.shadowRoot, hit);
      const inner = sr && B.shadowFromPoint ? apply(B.shadowFromPoint, sr, [x, y]) : null;
      if (!inner || inner === hit) break;
      hit = inner;
    }
    return hit;
  }

  // own reports whether a click hitting hit is a click on el: hit is el,
  // in it, through shadow roots, or in one of its labels.
  function own(el, hit) {
    const labels = isHTML(el) && LABELS[tagOf(el)] ? get(LABELS[tagOf(el)], el) : null;
    for (let n = hit, i = 0; n && i < 4096; i++, n = up(n)) {
      if (n === el) return true;
      if (labels) for (let j = 0, k = listLength(labels); j < k; j++) if (item(labels, j) === n) return true;
    }
    return false;
  }

  // covering is what a click at x, y of the innermost frame's viewport
  // would hit instead of a frame it is in, in the documents around the
  // frames (chain, from the top frame in), with the frame it misses; null
  // where each document shows the frame there, or nothing at all, as at a
  // point out of its view.
  function covering(ref, chain, x, y) {
    for (let i = chain.length - 1; i >= 0; i--) {
      const f = deref(chain[i].frame);
      if (!f) throw gone(ref);
      const o = outward(f, x, y);
      x = o.x;
      y = o.y;
      const hit = hitAt(get(B.ownerDocument, f), x, y);
      if (hit && hit !== f) return { __proto__: null, hit, under: f };
    }
    return null;
  }

  // aim is where a click on el lands, and what it hits there: the first
  // of its points where what shows is el's own, in its document and, at
  // the frames el is in, in each document around them. Where none is, it
  // says what shows instead; where nothing shows at any, as no point is
  // in view, el at its first.
  function aim(ref, el, doc, chain) {
    const ps = points(el, doc);
    // other is what shows instead, and under is what it hides: el, or a
    // frame el is in.
    let other = null, under = el;
    for (let i = 0; i < ps.length; i++) {
      const hit = hitAt(doc, ps[i].x, ps[i].y);
      if (!hit) continue;
      if (own(el, hit)) {
        const c = covering(ref, chain, ps[i].x, ps[i].y);
        if (!c) return { __proto__: null, x: ps[i].x, y: ps[i].y, target: hit };
        if (!other) other = c.hit, under = c.under;
      } else if (!other) other = hit;
    }
    if (!other) {
      const b = rect(el);
      const at = ps.length ? ps[0] : { __proto__: null, x: get(B.x, b) + get(B.width, b) / 2, y: get(B.y, b) + get(B.height, b) / 2 };
      return { __proto__: null, x: at.x, y: at.y, target: el };
    }
    // What contains el, or its frame, showing where it is: it takes no
    // pointer there, as under pointer-events: none, or a box of its
    // ancestor's own covers it.
    for (let n = up(under), i = 0; n && i < 4096; i++, n = up(n)) {
      if (n === other) throw new Err(`${ref} takes no clicks where it shows`);
    }
    const or = role(other), on = name(other, or);
    throw new Err(`${ref} is covered by ${or || lower(tagOf(other)) || "an element"}${on ? " " + quote(on) : ""}, which a click there would hit`);
  }

  // act does what an agent asks of a ref's element (args.action): "click"
  // clicks it; "type" replaces its text with args.text and, with
  // args.submit, presses Enter, which submits its form as a browser
  // would, unless the page takes the key. It acts only once the element is still what the
  // snapshot named (args.role, args.name), shows and is enabled, scrolled
  // into view. Its events are the script's, not the user's: a page sees
  // isTrusted false on each.
  function act(args) {
    const ref = args.ref, found = resolve(ref), el = found.el;
    const r = role(el) || "", n = name(el, r);
    if (r !== args.role || n !== args.name)
      throw new Err(`${ref} was ${args.role} ${quote(args.name)}, now ${r} ${quote(n)}; take a new snapshot`);
    if (attr(el, "aria-disabled") === "true" || matches(el, ":disabled")) throw new Err(`${ref} is disabled`);
    if (shown(el, styleOf(el), false) !== SHOWN) throw new Err(`${ref} is not shown`);
    const doc = get(B.ownerDocument, el), view = get(B.defaultView, doc), html = isHTML(el);
    const fire = (e) => apply(B.dispatch, el, [e]);
    const focus = () => { if (html) apply(B.focus, el, []); };
    // What a user can't use, the agent doesn't either: an element inert,
    // or behind a modal dialog, in its own document or around a frame
    // it is in. A modal dialog escapes the inertness around it.
    const usable = (node) => {
      const d = get(B.ownerDocument, node);
      const modalAt = (n) => { try { return matches(n, "dialog:modal"); } catch { return false; } };
      let inModal = false;
      for (let n = node, i = 0; n && i < 4096; i++, n = up(n)) {
        if (modalAt(n)) { inModal = true; break; }
        if (hasAttr(n, "inert")) throw new Err(`${ref} is inert: the page lets nothing use it now`);
      }
      let modal = null;
      if (!inModal) try { modal = apply(B.query, get(B.documentElement, d), ["dialog:modal"]); } catch {}
      if (modal) throw new Err(`${ref} is behind a modal dialog`);
    };
    usable(el);
    for (let i = 0; i < found.chain.length; i++) {
      const f = deref(found.chain[i].frame);
      if (!f) throw gone(ref);
      usable(f);
    }
    if (args.action === "click") {
      reveal(el);
      // The events go to what the click hits, el or what is in it, or
      // its label, as a user's would.
      const aimed = aim(ref, el, doc, found.chain), x = aimed.x, y = aimed.y, target = aimed.target;
      const send = (e) => apply(B.dispatch, target, [e]);
      const at = (buttons) => ({ __proto__: null, bubbles: true, cancelable: true, composed: true, view,
        clientX: x, clientY: y, button: 0, buttons, detail: 1 });
      const pointer = (type, buttons) => {
        if (!EV.Pointer) return;
        const init = at(buttons);
        define(init, "pointerId", 1);
        define(init, "pointerType", "mouse");
        define(init, "isPrimary", true);
        send(new EV.Pointer(type, init));
      };
      // The click comes once the script has answered: what it sets off,
      // a page leaving or a dialog holding the page, would take the
      // answer with it.
      soon(() => {
        pointer("pointerdown", 1);
        // A page taking mousedown keeps the focus where it is, as a
        // toolbar's button keeps the editor's selection.
        const down = new EV.Mouse("mousedown", at(1));
        send(down);
        if (!get(B.defaultPrevented, down)) focus();
        pointer("pointerup", 0);
        send(new EV.Mouse("mouseup", at(0)));
        send(new EV.Mouse("click", at(0)));
      });
      return { __proto__: null, done: true };
    }
    if (args.action !== "type") throw new Err(`webtarget: unknown action ${args.action}`);
    const tag = tagOf(el);
    const field = html && (tag === "INPUT" || tag === "TEXTAREA");
    const editor = html && !field && (opt(B.editable, el) || (tag === "BODY" && opt(B.designMode, doc) === "on"));
    if (tag === "INPUT" && html && lower(get(B.inputType, el)) === "file") throw new Err(`${ref} is a file field, which the browser's tools can't fill`);
    if (tag === "SELECT") throw new Err(`${ref} is a dropdown, which the browser's tools can't pick from yet`);
    if (TYPEABLE[r] !== true || !(field || editor)) throw new Err(`${ref} is a ${r || "element"}, not a text box`);
    const kind = tag === "INPUT" && html ? lower(get(B.inputType, el)) : "";
    if (tag === "INPUT" && BLOCKS_IMPLICIT[kind] !== true && kind !== "")
      throw new Err(`${ref} is a ${kind} field, which the browser's tools can't fill`);
    if (attr(el, "aria-readonly") === "true" || matches(el, ":read-only")) throw new Err(`${ref} is read-only`);
    // The element the focus is on, through shadow roots.
    const focused = () => {
      let active = opt(B.activeElement, doc);
      for (let i = 0; active && active !== el && i < 64; i++) {
        const root = get(B.shadowRoot, active);
        const inner = root ? opt(B.shadowActive, root) : null;
        if (!inner) break;
        active = inner;
      }
      return active;
    };
    reveal(el);
    const before = html ? focused() : null;
    focus();
    // What the focus can't reach, the user can't type into: an element
    // inert, as one in a modal dialog under another is, leaves the focus
    // where it was. One the page passes the focus on from, as a field
    // opening a picker does, took it.
    if (html) {
      let after = focused();
      // A text box within an editor takes no focus of its own: the
      // editor's host takes it.
      if (after !== el && after === before && editor) {
        let host = el;
        for (let n = up(host), i = 0; n && i < 4096 && isHTML(n) && opt(B.editable, n); i++, n = up(n)) host = n;
        if (host !== el) {
          apply(B.focus, host, []);
          after = focused();
          if (after === host) after = el;
        }
      }
      if (after !== el && after === before) throw new Err(`${ref} is inert: the page lets nothing use it now`);
    }
    if (field) {
      // A field takes no more than a user could type into it.
      const most = get(tag === "INPUT" ? B.inputMaxLength : B.textareaMaxLength, el);
      if (most >= 0 && args.text.length > most) throw new Err(`${ref} takes at most ${most} characters`);
      const old = tag === "INPUT" ? get(B.inputValue, el) : "";
      apply(tag === "INPUT" ? B.setInputValue : B.setTextareaValue, el, [args.text]);
      // A field that takes only some values, as a date's, keeps none
      // other: the agent hears what it took, and the field keeps what it
      // had, which the page, hearing no input, still thinks it has.
      const took = tag === "INPUT" ? get(B.inputValue, el) : args.text;
      if (took !== args.text) {
        apply(B.setInputValue, el, [old]);
        throw new Err(`${ref} took ${quote(took)}, not ${quote(args.text)}`);
      }
      fire(new EV.Input("input", { __proto__: null, bubbles: true, composed: true, inputType: "insertText", data: args.text }));
      fire(new EV.Event("change", { __proto__: null, bubbles: true }));
    } else {
      apply(B.selectAllChildren, apply(B.getSelection, doc, []), [el]);
      if (!apply(B.execCommand, doc, ["insertText", false, args.text])) throw new Err(`${ref} didn't take the text`);
    }
    if (args.submit) {
      const key = (type) => {
        const e = new EV.Keyboard(type, { __proto__: null, bubbles: true, cancelable: true, composed: true, view,
          key: "Enter", code: "Enter", keyCode: 13, which: 13, charCode: type === "keypress" ? 13 : 0 });
        fire(e);
        return get(B.defaultPrevented, e);
      };
      // As a browser does: keydown, then keypress unless the keydown was
      // taken, implicit submission unless either was, then keyup.
      const taken = key("keydown") || key("keypress");
      const form = !taken && tag === "INPUT" ? get(B.inputForm, el) : null;
      key("keyup");
      // The form submits once the script has answered: a page leaving
      // as it runs would take its answer with it.
      const submit = form && implicit(form);
      if (submit) soon(submit);
    }
    return { __proto__: null, done: true };
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

  const api = freeze({ __proto__: null, snapshot, describe, act });
  defineProperty(window, key, { __proto__: null, value: api, writable: false, configurable: false, enumerable: false });
  return api;
}

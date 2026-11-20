// The command palette, as Glitched draws it by default: a search field,
// the actions and threads it finds, and the keys that drive it. Glitched
// keeps the dialog of glass it sits in, and draws only the rows in view.
// Copy it in Settings › Plugins, and the copy draws the palette in its
// place.

const ROW = 34;
const HEADER = 28;
const MAX_HEIGHT = 380;

// Each opening of the palette is a copy of its own, by its key: the state
// is the query and the choice of the one open now.
export function init() {
  return { state: { key: "", query: "", chosen: "" } };
}

function opening(state, key, ctx) {
  return state.key === key ? state : { key, query: ctx.query, chosen: "" };
}

function matches(s, q) {
  return q === "" || s.toLowerCase().includes(q.toLowerCase());
}

// alsoMatches is whether each word of the query is in the action's label
// or is one of its unshown words, whole.
function alsoMatches(action, query) {
  const words = query.toLowerCase().split(/\s+/).filter(Boolean);
  if (!action.also || words.length === 0) return false;
  const also = action.also.split(/\s+/).filter(Boolean);
  return words.every((w) => also.includes(w) || matches(action.label, w));
}

// found are what the palette offers for the query: the actions, then the
// threads. A query starting with > keeps to the actions.
function found(ctx, query) {
  const actionsOnly = query.startsWith(">");
  const q = (actionsOnly ? query.slice(1) : query).trim();
  const out = [];
  for (const a of ctx.actions) {
    if (matches(a.label, q) || matches(a.hint ?? "", q) || alsoMatches(a, q)) {
      out.push({ key: "a:" + a.id, section: a.section, label: [a.label, a.hint].filter(Boolean).join(" ") });
    }
  }
  if (!actionsOnly) {
    for (const t of ctx.threads) {
      if (matches(t.title, q) || matches(t.project, q)) {
        out.push({ key: "t:" + t.id, section: "Threads", label: [t.title, t.project, t.ago].filter(Boolean).join(" "), thread: t.id });
      }
    }
  }
  return out;
}

// results are the list's items, a header over each section, and its
// height: as tall as Glitched's own palette makes them, up to 380.
function results(cmds) {
  if (cmds.length === 0) {
    return { items: [{ key: "none", label: "No results", header: true, height: 66 }], height: 72 };
  }
  const items = [];
  let height = 12;
  cmds.forEach((cmd, i) => {
    if (i === 0 || cmds[i - 1].section !== cmd.section) {
      items.push({ key: "h:" + cmd.section, label: cmd.section, header: true, height: HEADER });
      height += 26;
    }
    items.push({ key: cmd.key, label: cmd.label, thread: cmd.thread });
    height += ROW;
  });
  return { items, height: Math.min(height, MAX_HEIGHT) };
}

function hint(key, what) {
  return {
    type: "row",
    gap: 5,
    children: [
      { type: "keys", keys: [key] },
      { type: "text", text: what, fontSize: 11, style: "muted" },
    ],
  };
}

// choice is the result chosen: the one the user chose while the query
// finds it, else the first.
function choice(cmds, s) {
  return cmds.some((cmd) => cmd.key === s.chosen) ? s.chosen : (cmds[0]?.key ?? "");
}

function palette(ctx, s) {
  const cmds = found(ctx, s.query);
  const { items, height } = results(cmds);
  const chosen = choice(cmds, s);
  return {
    type: "column",
    children: [
      {
        type: "row",
        gap: 10,
        padding: [14, 16],
        children: [
          { type: "icon", name: "magnifer", fontSize: 16, color: "$text.muted" },
          { type: "input", id: "q", style: "bare", placeholder: "Search threads and commands…", value: s.query, controlled: true, onChange: "query" },
          { type: "keys", keys: ["esc"] },
        ],
      },
      { type: "divider" },
      {
        type: "list",
        id: "results",
        label: "Results",
        input: "q",
        items,
        height,
        rowHeight: ROW,
        padding: 6,
        value: chosen,
        controlled: true,
        chooseOnHover: true,
        wrapAround: true,
        submitOnClick: true,
        onChange: "chose",
        onSubmit: "submit",
      },
      { type: "divider" },
      {
        type: "row",
        gap: 14,
        padding: [7, 16],
        children: [
          hint("↑↓", "navigate"),
          hint("↩", "open"),
          hint("⌘↩", "open with diff"),
          { type: "spacer" },
          {
            type: "row",
            gap: 5,
            children: [
              { type: "text", text: "Type", fontSize: 11, style: "muted" },
              { type: "keys", keys: [">"] },
              { type: "text", text: "for actions", fontSize: 11, style: "muted" },
            ],
          },
        ],
      },
    ],
  };
}

export function place({ state, items }) {
  return items.map((it) => (it.context ? palette(it.context, opening(state, it.key, it.context)) : null));
}

const faint = (text) => ({ type: "text", text, fontSize: 11.5, color: "$text.faint" });

export function rows({ keys, place }) {
  const ctx = place.context;
  const actions = new Map(ctx.actions.map((a) => ["a:" + a.id, a]));
  const threads = new Map(ctx.threads.map((t) => ["t:" + t.id, t]));
  return keys.map((key) => {
    if (key === "none") {
      return { type: "column", align: "center", children: [{ type: "text", text: "No results", style: "muted", padding: 24 }] };
    }
    if (key.startsWith("h:")) {
      return { type: "text", text: key.slice(2), fontSize: 11, weight: 600, color: "$text.faint", padding: [8, 10, 4, 10] };
    }
    const a = actions.get(key);
    if (a) {
      return {
        type: "row",
        gap: 8,
        padding: [0, 10],
        grow: true,
        children: [{ type: "icon", name: a.icon, fontSize: 14 }, { type: "text", text: a.label, singleLine: true, grow: true }, faint(a.hint ?? "")],
      };
    }
    const t = threads.get(key);
    if (!t) return null;
    return {
      type: "row",
      gap: 8,
      padding: [0, 10],
      grow: true,
      children: [
        { type: "native", name: "thread.status", props: { size: 14 } },
        { type: "text", text: t.title, singleLine: true, grow: true },
        faint(t.project),
        { type: "native", name: "thread.age" },
      ],
    };
  });
}

export async function update({ state, event }) {
  if (event.kind !== "ui" || !event.place?.context) return {};
  const s = opening(state, event.place.key, event.place.context);
  switch (event.handler) {
    case "query":
      return { state: { ...s, query: event.value, chosen: "" } };
    case "chose":
      return { state: { ...s, chosen: event.value } };
    case "submit": {
      // The choice as this state has it, every key typed before Enter
      // taken: the list on screen may not have caught up with them yet.
      const key = choice(found(event.place.context, s.query), s);
      const withDiff = (event.modifiers ?? []).includes("cmd");
      if (key.startsWith("t:")) {
        await glitched.palette.close();
        await glitched.threads.open(key.slice(2));
        if (withDiff) {
          await glitched.panels.toggle("diff", true);
          await glitched.panels.toggle("preview", false);
        }
      } else if (key.startsWith("a:") && !withDiff) {
        await glitched.commands.run(key.slice(2));
      }
      return {};
    }
  }
  return {};
}

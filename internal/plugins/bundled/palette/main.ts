// The command palette, as Glitched draws it by default: a search field,
// the actions, threads and messages it finds, and the keys that drive it.
// Glitched keeps the dialog of glass it sits in, and draws only the rows
// in view.
// Copy it in Settings › Plugins, and the copy draws the palette in its
// place.

const MAX_HEIGHT = 380;

// hits are the messages found for hitsQuery.
type State = { key: string; query: string; chosen: string; hits: SearchHit[]; hitsQuery: string };
type PaletteItem = { key: string; section: string; label: string; thread?: string };

// Each opening of the palette is a copy of its own, by its key: the state
// is the query, the messages it found and the choice of the one open now.
export function init(): Update<State> {
  return { state: { key: "", query: "", chosen: "", hits: [], hitsQuery: "" } };
}

function opening(state: State, key: string, ctx: PaletteContext): State {
  return state.key === key ? state : { key, query: ctx.query, chosen: "", hits: [], hitsQuery: "" };
}

// searched is what the query searches the messages for: nothing for one
// that keeps to the actions, or is under two letters.
function searched(query: string): string {
  const q = query.trim();
  return query.startsWith(">") || q.length < 2 ? "" : q;
}

// lower lowers each letter on its own, to one letter, as Glitched does: a
// final sigma is a sigma, and a dotted capital I an i.
function lower(s: string): string {
  return Array.from(s, (c) => Array.from(c.toLowerCase())[0]).join("");
}

function matches(s: string, q: string): boolean {
  return q === "" || lower(s).includes(lower(q));
}

// alsoMatches is whether each word of the query is in the action's label
// or is one of its unshown words, whole.
function alsoMatches(action: CommandRecord, query: string): boolean {
  const words = lower(query).split(/\s+/).filter(Boolean);
  if (!action.also || words.length === 0) return false;
  const also = action.also.split(/\s+/).filter(Boolean);
  return words.every((w) => also.includes(w) || matches(action.label, w));
}

// found are what the palette offers for the query: the actions, the
// threads, then the messages found for it. A query starting with > keeps
// to the actions.
function found(ctx: PaletteContext, s: State): PaletteItem[] {
  const query = s.query;
  const actionsOnly = query.startsWith(">");
  const q = (actionsOnly ? query.slice(1) : query).trim();
  const out: PaletteItem[] = [];
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
  const sought = searched(query);
  if (sought !== "" && s.hitsQuery === sought) {
    for (const h of s.hits) {
      out.push({ key: hitKey(h), section: "Messages", label: [h.title, h.snippet, h.ago].filter(Boolean).join(" "), thread: h.thread });
    }
  }
  return out;
}

// results are the list's items, a header over each section, its height
// and the room under its rows, as Glitched's own palette makes them: its
// rows as tall as the context says, in the interface font, and the list as
// tall as 34 points a row and 26 a header, up to 380, whatever its rows'.
function results(ctx: PaletteContext, cmds: PaletteItem[]): { items: ListItem[]; height: number; under: number } {
  if (cmds.length === 0) {
    return { items: [{ key: "none", label: "No results", header: true, height: ctx.noneHeight }], height: 72, under: 0 };
  }
  const items: ListItem[] = [];
  let height = 12;
  let rows = 12;
  cmds.forEach((cmd, i) => {
    if (i === 0 || cmds[i - 1].section !== cmd.section) {
      items.push({ key: "h:" + cmd.section, label: cmd.section, header: true, height: ctx.headerHeight });
      height += 26;
      rows += ctx.headerHeight;
    }
    items.push({ key: cmd.key, label: cmd.label, thread: cmd.thread });
    height += 34;
    rows += ctx.rowHeight;
  });
  height = Math.min(height, MAX_HEIGHT);
  return { items, height, under: Math.max(Math.ceil(height - rows), 0) };
}

const hitKey = (h: SearchHit): string => "m:" + h.thread + ":" + h.seq;

function hint(key: string, what: string): Widget {
  return {
    type: "row",
    gap: 5,
    children: [
      { type: "keys", keys: [key] },
      { type: "text", text: what, fontSize: 11, style: "muted" },
    ],
  };
}

function palette(ctx: PaletteContext, s: State): Widget {
  const cmds = found(ctx, s);
  const { items, height, under } = results(ctx, cmds);
  // The one the user chose while the query finds it, else the first.
  const chosen = cmds.some((cmd) => cmd.key === s.chosen) ? s.chosen : (cmds[0]?.key ?? "");
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
        rowHeight: ctx.rowHeight,
        padding: [6, 6, 6 + under, 6],
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

export function place({ state, items }: PlaceArgs<State, PaletteContext>): (Widget | null)[] {
  return items.map((it) => (it.context ? palette(it.context, opening(state, it.key, it.context)) : null));
}

const faint = (text: string): Widget => ({ type: "text", text, fontSize: 11.5, color: "$text.faint" });

export function rows({ keys, place, state }: RowsArgs<State, PaletteContext>): (Widget | null)[] {
  const ctx = place?.context;
  if (!ctx) return keys.map(() => null);
  const actions = new Map(ctx.actions.map((a) => ["a:" + a.id, a]));
  const threads = new Map(ctx.threads.map((t) => ["t:" + t.id, t]));
  const hits = new Map(state.hits.map((h) => [hitKey(h), h]));
  return keys.map((key): Widget | null => {
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
    const h = hits.get(key);
    if (h) {
      return {
        type: "row",
        gap: 8,
        padding: [0, 10],
        grow: true,
        children: [
          { type: "native", name: "thread.status", props: { size: 14 } },
          { type: "text", text: h.title, singleLine: true },
          { type: "text", text: h.snippet, fontSize: 11.5, color: "$text.faint", singleLine: true, grow: true },
          { type: "native", name: "thread.age" },
        ],
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

export async function update({ state, event }: UpdateArgs<State, PaletteContext>): Promise<Update<State>> {
  if (event.kind !== "ui" || !event.place?.context) return {};
  const s = opening(state, event.place.key, event.place.context);
  switch (event.handler) {
    case "query": {
      // A search that fails, as without messages:read, finds no messages.
      const query = String(event.value ?? "");
      const sought = searched(query);
      if (sought === "" || sought === s.hitsQuery) return { state: { ...s, query, chosen: "" } };
      const hits = await glitched.threads.search(sought, { limit: 20 }).catch(() => []);
      return { state: { ...s, query, chosen: "", hits, hitsQuery: sought } };
    }
    case "chose":
      return { state: { ...s, chosen: String(event.value ?? "") } };
    case "submit": {
      // Glitched submits a key pressed once this took every key typed
      // before it, so the key is of the results the user meant. Each
      // closes this opening of the palette alone, not one opened since.
      const key = String(event.value ?? "");
      const withDiff = (event.modifiers ?? []).includes("cmd");
      const hit = s.hitsQuery === searched(s.query) ? s.hits.find((h) => hitKey(h) === key) : undefined;
      if (key.startsWith("t:") || hit) {
        await glitched.palette.close(event.place.key);
        await (hit ? glitched.threads.open(hit.thread, { find: s.hitsQuery, seq: hit.seq }) : glitched.threads.open(key.slice(2)));
        if (withDiff) {
          await glitched.panels.toggle("diff", true);
          await glitched.panels.toggle("preview", false);
        }
      } else if (key.startsWith("a:") && !withDiff) {
        await glitched.commands.run(key.slice(2), event.place.key);
      }
      return {};
    }
  }
  return {};
}

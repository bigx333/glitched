// The sidebar's threads, as Glitched draws them by default: the inbox,
// which grows, and Working, Snoozed and Settled under it, which scroll past
// half the sidebar. Glitched works out which threads go where and draws
// their parts; this lays them out, and draws only the rows in view. Copy it
// in Settings › Plugins, and the copy draws the sidebar's threads in its
// place.

const HEAD = 30;
const ROW = 32;
const CHILD = 28;
const GAP = 2;

export function init(): Update<Record<string, never>> {
  return { state: {} };
}

// items are a copy's list items: each section's header, then its rows, or
// what it says while empty. The sections of the bottom part are 6 apart.
function items(ctx: SidebarBodyContext): ListItem[] {
  const out: ListItem[] = [];
  const seen = new Set<string>();
  ctx.sections.forEach((s, i) => {
    // A section's header is 2 over its rows, and the next section 6 under
    // them, as are the rows' column, empty or not, and the list's end.
    const before = i === 0 ? 0 : 4;
    const after = s.rows.length === 0 && !(s.empty && !s.closed) ? GAP : 0;
    out.push({ key: "h:" + s.id, label: s.title, header: true, height: HEAD + before + after });
    if (s.rows.length === 0 && s.empty && !s.closed) {
      out.push({ key: "e:" + s.id, label: s.empty, header: true, height: ctx.emptyHeight });
    }
    for (const r of s.rows) {
      if (r.more) {
        // A thread's children show twice, as it does, if it's lifted into
        // Working: the depth tells the two apart.
        const key = (r.full ? "f:" : "m:") + (r.parent || s.id) + "#" + (r.depth || 0);
        const height = (r.depth ?? 0) > 0 ? CHILD : ROW;
        if (r.full) {
          // The list holds as many as a list can: the rest are the palette's.
          out.push({ key, label: `${r.more} more not listed: search for them with ⌘K`, header: true, height });
          continue;
        }
        const n = Math.min(r.more, ctx.page);
        out.push({ key, label: `Show ${n} more, of ${r.more} not shown`, height, version: String(r.more) });
        continue;
      }
      if (!r.thread) continue;
      // A child lifted into Working shows there and under its line.
      const key = seen.has(r.thread) ? r.thread + "#" + r.depth : r.thread;
      seen.add(r.thread);
      // The row's version is what its widgets are made of; its title, as
      // the rest the natives draw, is the app's to keep up to date.
      const version = [r.depth || 0, r.status, r.kids ? 1 : 0, r.renaming ? 1 : 0].join(":");
      out.push({ key, label: "", thread: r.thread, height: (r.depth ?? 0) > 0 ? CHILD : ROW, version });
    }
  });
  return out;
}

function body(ctx: SidebarBodyContext): Widget {
  const all = items(ctx);
  // The thread shown, where this copy shows it.
  const value = all.some((it) => it.key === ctx.selected) ? ctx.selected : "";
  const list = {
    type: "list" as const,
    id: "threads",
    label: ctx.part === "inbox" ? "Inbox" : "Working, Snoozed and Settled",
    items: all,
    gap: GAP,
    noKeys: true,
    value,
    controlled: true,
    onChange: "chose",
  };
  if (ctx.part === "inbox") {
    return { ...list, grow: true, padding: [4, 8, 8, 8] };
  }
  return { ...list, height: Math.max(ctx.maxHeight ?? 0, 1), padding: [6, 8] };
}

export function place({ items }: PlaceArgs<Record<string, never>, SidebarBodyContext>): (Widget | null)[] {
  return items.map((it) => (it.context ? body(it.context) : null));
}

type SidebarNativeName =
  | "sidebar.section"
  | "thread.title"
  | "thread.rename"
  | "thread.status"
  | "thread.children"
  | "thread.mark"
  | "thread.tile"
  | "thread.slot"
  | "thread.machine"
  | "thread.row.actions";

const native = (name: SidebarNativeName, props: Record<string, unknown> | null = null, more: Record<string, unknown> = {}): Widget =>
  ({ type: "native", name, ...(props ? { props } : {}), ...more }) as Widget;

function header(ctx: SidebarBodyContext, key: string, height: number): Widget {
  const id = key.slice(2);
  const i = ctx.sections.findIndex((s) => s.id === id);
  const top = i === 0 ? 0 : 4;
  return {
    type: "column",
    padding: [top, 0, height - HEAD - top, 0],
    children: [native("sidebar.section", { section: id })],
  };
}

function row(ctx: SidebarBodyContext, r: SidebarRow): Widget {
  const depth = r.depth ?? 0;
  if (depth > 0) {
    const children: Widget[] = [
      { type: "icon", name: "child", fontSize: 12, color: "$text.faint" },
      native("thread.title", { child: true }, { grow: true }),
    ];
    if (r.renaming) {
      children.splice(1, 1, native("thread.rename", null, { grow: true }));
      return { type: "row", grow: true, gap: 8, padding: [0, 6, 0, 8 + depth * 16], children };
    }
    if (r.status && ["working", "waiting", "completed"].includes(r.status)) children.push(native("thread.status"));
    children.push(r.kids ? native("thread.children") : native("thread.mark", { child: true }));
    return { type: "row", grow: true, gap: 8, padding: [0, 6, 0, 8 + depth * 16], children };
  }
  const children: Widget[] = [native("thread.tile")];
  if (r.renaming) {
    children.push(native("thread.rename", null, { grow: true }));
  } else {
    children.push(native("thread.title", null, { grow: true }));
    if (ctx.slots) children.push(native("thread.slot"));
    if (ctx.machines) children.push(native("thread.machine"));
    children.push(native("thread.row.actions", null, { when: "hover" }));
    children.push(native("thread.mark", null, { when: "idle" }));
    if (r.kids) children.push(native("thread.children"));
  }
  return { type: "row", grow: true, gap: 9, padding: [0, 6, 0, 8], children };
}

// more is the row that shows the next page of a section, or of a thread's
// children; its item names it to assistive tools with how many aren't
// shown, and choosing it, by a click or the keys, shows them.
function more(ctx: SidebarBodyContext, key: string, it: ListItem): Widget {
  const depth = Number(key.split("#")[1]);
  const n = Math.min(Number(it.version), ctx.page);
  return {
    type: "row",
    grow: true,
    padding: [0, 6, 0, 8 + depth * 16],
    children: [{ type: "text", text: `Show ${n} more`, fontSize: 12, color: "$text.muted" }],
  };
}

export async function update({ event }: UpdateArgs<Record<string, never>, SidebarBodyContext>): Promise<Update<Record<string, never>>> {
  if (event.kind === "ui" && event.handler === "chose" && typeof event.value === "string" && event.value.startsWith("m:")) {
    await glitched.sidebar.more(event.value.slice(2).split("#")[0]);
  }
  return {};
}

export function rows({ keys, place }: RowsArgs<Record<string, never>, SidebarBodyContext>): (Widget | null)[] {
  const ctx = place?.context;
  if (!ctx) return keys.map(() => null);
  const byKey = new Map<string, ListItem>();
  for (const it of items(ctx)) byKey.set(it.key, it);
  const byThread = new Map<string, SidebarRow>();
  for (const s of ctx.sections) for (const r of s.rows) byThread.set(r.thread + "#" + (r.depth || 0), r);
  return keys.map((key) => {
    const it = byKey.get(key);
    if (!it) return null;
    if (key.startsWith("h:")) return header(ctx, key, it.height ?? HEAD);
    if (key.startsWith("e:") || key.startsWith("f:")) {
      return { type: "text", text: it.label, fontSize: 12, color: "$text.faint", padding: [6, 10] };
    }
    if (key.startsWith("m:")) return more(ctx, key.slice(2), it);
    const r = it.thread && it.version ? byThread.get(it.thread + "#" + it.version.split(":")[0]) : undefined;
    return r ? row(ctx, r) : null;
  });
}

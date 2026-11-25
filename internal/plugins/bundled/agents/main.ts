// The agents panel's rows, as Glitched draws them by default: the threads
// under the one shown, each with its run's dot, its title, and how long ago
// it changed, or, pointed at, … for its menu; past five, More. Glitched
// keeps the pill, the panel it grows into and its head, and raises the rows
// in one after another as it opens. Copy it in Settings › Plugins, and the
// copy draws the rows in its place.

const ROW = 26;
// The panel's text starts where the composer's does.
const INSET = 9;

export function init(): Update<Record<string, never>> {
  return { state: {} };
}

function items(ctx: AgentsBodyContext): ListItem[] {
  const out: ListItem[] = ctx.rows.map((r) => ({ key: r.thread, label: "", thread: r.thread, version: String(r.depth) }));
  if (ctx.more) out.push({ key: "more", label: "More" });
  return out;
}

export function place({ items: copies }: PlaceArgs<Record<string, never>, AgentsBodyContext>): (Widget | null)[] {
  return copies.map((it) => {
    if (!it.context) return null;
    return { type: "list", id: "agents", label: "Agents", items: items(it.context), rowHeight: ROW, noKeys: true, grow: true };
  });
}

const native = (name: "thread.status" | "thread.title" | "thread.age" | "thread.actions", props: Record<string, unknown> | null, more: Record<string, unknown> = {}): Widget =>
  ({ type: "native", name, ...(props ? { props } : {}), ...more }) as Widget;

export function rows({ keys, place }: RowsArgs<Record<string, never>, AgentsBodyContext>): (Widget | null)[] {
  const ctx = place?.context;
  if (!ctx) return keys.map(() => null);
  const byThread = new Map(ctx.rows.map((r) => [r.thread, r]));
  return keys.map((key): Widget | null => {
    if (key === "more") {
      return { type: "row", grow: true, padding: [0, 8], children: [{ type: "text", text: "More", fontSize: 12.5 }] };
    }
    const r = byThread.get(key);
    if (!r) return null;
    return {
      type: "row",
      grow: true,
      gap: 8,
      padding: [0, 4, 0, INSET + (r.depth - 1) * 14],
      children: [
        native("thread.status", { dot: true }),
        native("thread.title", null, { grow: true }),
        native("thread.age", null, { when: "idle" }),
        native("thread.actions", null, { when: "hover" }),
      ],
    };
  });
}

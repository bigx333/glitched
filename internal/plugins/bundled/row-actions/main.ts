// A sidebar row's actions, as Glitched draws them by default, shown while
// the row is pointed at: snooze it until a time, with a joke for each, and
// settle it; a snoozed or settled thread goes back to the inbox instead.
// Copy it in Settings › Plugins, and the copy draws them in its place.

type State = { open: string | null };

export function init(): Update<State> {
  return { state: { open: null } };
}

function action(
  id: string,
  icon: string,
  tooltip: string,
  color: string,
  onClick: Handler,
  extra: Partial<Omit<Extract<Widget, { type: "button"; style: "ghost" }>, "type" | "id" | "style" | "size" | "icon" | "label" | "tooltip" | "color" | "onClick">> = {},
): Widget {
  return { type: "button", id, style: "ghost", size: "small", icon, label: tooltip, tooltip, color, onClick, ...extra };
}

function snoozePopover(ctx: RowActionsContext): Extract<Widget, { type: "button"; style: "ghost" }>["popover"] {
  return {
    type: "column",
    width: 220,
    padding: 5,
    gap: 1,
    children: [
      { type: "text", text: "Snooze until…", fontSize: 11, weight: 600, color: "$text.faint", padding: [4, 8, 3, 8] },
      ...ctx.snooze.map((s, i): Widget => ({
        type: "row",
        id: "when" + i,
        gap: 10,
        padding: [6, 8],
        onClick: { handler: "snooze", payload: i },
        children: [
          { type: "text", text: s.emoji, fontSize: 15 },
          {
            type: "column",
            grow: true,
            gap: 1,
            children: [
              { type: "text", text: s.when, weight: 500, color: "$text" },
              { type: "text", text: s.joke, size: "small", style: "muted", italic: true },
            ],
          },
        ],
      })),
    ],
  };
}

function actions(ctx: RowActionsContext, key: string, state: State): Widget[] {
  if (ctx.section === "settled" || ctx.section === "snoozed") {
    return [action("back", "undo-left-round", "Back to inbox", "#60a5fa", "back")];
  }
  if (ctx.section !== "inbox") return [];
  const open = state.open === key;
  return [
    action("snooze", "clock-circle", "Snooze", "#fbbf24", "snooze.toggle", {
      active: open,
      open,
      onClose: "snooze.close",
      popover: snoozePopover(ctx),
    }),
    action("settle", "check-circle", "Settle", "#4ade80", "settle"),
  ];
}

export function place({ state, items }: PlaceArgs<State, RowActionsContext>): (Widget | null)[] {
  return items.map((it) => (it.context ? { type: "row", gap: 0, children: actions(it.context, it.key, state) } : null));
}

export async function update({ state, event }: UpdateArgs<State, RowActionsContext>): Promise<Update<State>> {
  if (event.kind !== "ui") return {};
  const ctx = event.place?.context;
  if (!ctx || !event.place) return {};
  const thread = ctx.thread.id;
  switch (event.handler) {
    case "snooze.toggle":
      return { state: { ...state, open: state.open === event.place.key ? null : event.place.key } };
    case "snooze.close":
      return { state: { ...state, open: null } };
    case "snooze": {
      if (typeof event.payload !== "number") return {};
      const s = ctx.snooze[event.payload];
      if (!s) return {};
      await glitched.threads.setDisposition(thread, { state: "snoozed", until: s.until }, { animation: "snooze" });
      return { state: { ...state, open: null } };
    }
    case "settle":
      await glitched.threads.setDisposition(thread, { state: "settled" }, { animation: "settle" });
      await glitched.threads.markRead(thread);
      return {};
    case "back":
      await glitched.threads.setDisposition(thread, { state: "inbox" }, { animation: "undo" });
      return {};
  }
  return {};
}

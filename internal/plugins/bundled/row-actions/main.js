// A sidebar row's actions, as Glitched draws them by default, shown while
// the row is pointed at: snooze it until a time, with a joke for each, and
// settle it; a snoozed or settled thread goes back to the inbox instead.
// Copy it in Settings › Plugins, and the copy draws them in its place.

export function init() {
  return { state: { open: null } };
}

function action(id, icon, tooltip, color, onClick, extra = {}) {
  return { type: "button", id, style: "ghost", size: "small", icon, label: tooltip, tooltip, color, onClick, ...extra };
}

function snoozePopover(ctx) {
  return {
    type: "column",
    width: 220,
    padding: 5,
    gap: 1,
    children: [
      { type: "text", text: "Snooze until…", fontSize: 11, weight: 600, color: "$text.faint", padding: [4, 8, 3, 8] },
      ...ctx.snooze.map((s, i) => ({
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

function actions(ctx, key, state) {
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

export function place({ state, items }) {
  return items.map((it) => (it.context ? { type: "row", gap: 0, children: actions(it.context, it.key, state) } : null));
}

export async function update({ state, event }) {
  if (event.kind !== "ui") return {};
  const ctx = event.place?.context;
  if (!ctx) return {};
  const thread = ctx.thread.id;
  switch (event.handler) {
    case "snooze.toggle":
      return { state: { ...state, open: state.open === event.place.key ? null : event.place.key } };
    case "snooze.close":
      return { state: { ...state, open: null } };
    case "snooze": {
      const s = ctx.snooze[event.payload];
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

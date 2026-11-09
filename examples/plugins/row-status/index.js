// A badge on each sidebar row. place draws every row in one call, from the
// rows' records; update counts the changes it hears, so a busy thread's
// badge says so.

export function init() {
  return { state: { changes: {} } };
}

const tones = { working: "accent", waiting: "warning", completed: "success" };

export function place({ state, items }) {
  return items.map(({ key, context }) => {
    // Without the threads:read grant a record is its id alone: draw nothing.
    if (!context.status) return null;
    const n = state.changes[key] ?? 0;
    return {
      type: "row",
      gap: 4,
      children: [
        { type: "badge", text: context.status, style: tones[context.status] ?? "default" },
        ...(n > 0 ? [{ type: "badge", text: `${n}×` }] : []),
      ],
    };
  });
}

export function update({ state, event }) {
  if (event.kind === "app" && event.type === "thread.changed" && !event.data.deleted) {
    const id = event.data.id;
    return { state: { changes: { ...state.changes, [id]: (state.changes[id] ?? 0) + 1 } } };
  }
  return {};
}

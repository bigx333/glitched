/// <reference path="../glitched.d.ts" />

// A badge on each sidebar row. place draws every row in one call, from the
// rows' records; update counts the changes it hears, so a busy thread's
// badge says so.

type State = { changes: Record<string, number> };

export function init(): Update<State> {
  return { state: { changes: {} } };
}

const tones: Record<string, "accent" | "warning" | "success"> = {
  working: "accent",
  waiting: "warning",
  completed: "success",
};

export function place({ state, items }: PlaceArgs<State, ThreadRecord>): (Widget | null)[] {
  return items.map(({ key, context }) => {
    // Without the threads:read grant a record is its id alone: draw nothing.
    if (!context?.status) return null;
    const n = state.changes[key] ?? 0;
    return {
      type: "row",
      gap: 4,
      children: [
        { type: "badge", text: context.status, style: tones[context.status] ?? "default" },
        ...(n > 0 ? [{ type: "badge" as const, text: `${n}×` }] : []),
      ],
    };
  });
}

export function update({ state, event }: UpdateArgs<State, ThreadRecord>): Update<State> {
  if (event.kind === "app" && event.type === "thread.changed" && !("deleted" in event.data)) {
    const id = event.data.id;
    return { state: { changes: { ...state.changes, [id]: (state.changes[id] ?? 0) + 1 } } };
  }
  return {};
}

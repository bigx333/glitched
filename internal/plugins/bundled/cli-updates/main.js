// The footer button and popover for provider CLIs with updates. Each
// update's row is native: what runs, waits, fails or must be typed stays
// under Glitched's engine-managed updater controls.
export function init() {
  return { state: { open: false } };
}

export function place({ state, items }) {
  return items.map(({ context }) => {
    const updates = context.updates ?? [];
    if (!updates.length) return { type: "row" };
    const tone = context.failed ? "$danger" : context.needed ? "$warning" : "$accent";
    const icon = context.running ? null : context.failed || context.needed ? "info-circle" : "arrow-down";
    return {
      type: "button",
      id: "updates",
      style: "ghost",
      label: "Agent CLI updates",
      tooltip: context.tooltip,
      active: state.open,
      open: state.open,
      onClick: "toggle",
      onClose: "close",
      padding: [4, 7],
      gap: 5,
      children: [
        ...(context.running ? [{ type: "icon", id: "icon", name: "refresh", fontSize: 14, color: tone }] : [{ type: "icon", id: "icon", name: icon, fontSize: 14, color: tone }]),
        { type: "text", id: "label", text: context.running ? "Updating" : "Updates", fontSize: 12.5, weight: 550, singleLine: true },
        ...(updates.length > 1 ? [{ type: "badge", id: "count", text: String(updates.length), style: "count" }] : []),
      ],
      popover: { type: "column", width: 380, padding: 12, gap: 12, children: [
        { type: "row", align: "center", gap: 8, children: [
          { type: "text", text: "CLI updates", style: "strong", grow: true },
          ...(updates.length > 1 ? [{ type: "button", id: "all", label: "Update all", disabled: !context.canUpdateAll, onClick: "updateAll" }] : []),
        ] },
        ...updates.map((u) => ({ type: "row", id: "row:" + u.machine + ":" + u.provider, gap: 10, align: "start", children: [
          { type: "native", id: "tile:" + u.machine + ":" + u.provider, name: "model.tile", props: { family: u.provider, size: 22 } },
          { type: "native", id: "update:" + u.machine + ":" + u.provider, name: "cli.update", props: { machine: u.machine, provider: u.provider, title: u.title }, grow: true },
        ] })),
      ] },
    };
  });
}

export async function update({ state, event }) {
  if (event.kind === "app" && event.type === "place.hidden") return { state: { ...state, open: false } };
  if (event.kind !== "ui") return {};
  if (event.handler === "toggle") return { state: { ...state, open: !state.open } };
  if (event.handler === "close") return { state: { ...state, open: false } };
  if (event.handler === "updateAll") await glitched.cliUpdates.updateAll();
  return {};
}

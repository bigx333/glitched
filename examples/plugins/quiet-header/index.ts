/// <reference path="../glitched.d.ts" />

// Replaces the thread header's buttons (the thread.header.actions
// component). Grant ui.replace in Settings › Plugins and this row draws in
// place of the built-in Header actions; turn it off, or let it throw, and
// the built-in comes back.

type PanelKind = "terminal" | "diff" | "preview";

const panels: { id: PanelKind; icon: string; tooltip: string }[] = [
  { id: "terminal", icon: "terminal", tooltip: "Toggle terminal  ⌘J" },
  { id: "diff", icon: "pull-request", tooltip: "Toggle diff  ⌘D" },
  { id: "preview", icon: "global", tooltip: "Toggle preview  ⇧⌘P" },
];

function row(ctx: HeaderActionsContext): Widget {
  const toggles: Widget[] = panels
    .filter((p) => ctx.local || p.id === "preview")
    .map((p): Widget => ({
      type: "button",
      id: p.id,
      style: "ghost",
      icon: p.icon,
      label: p.tooltip,
      tooltip: p.tooltip,
      active: ctx.panels[p.id],
      badge: p.id === "diff" && ctx.changedFiles > 0 ? String(Math.min(ctx.changedFiles, 999)) : undefined,
      onClick: { handler: "panel", payload: p.id },
    }));
  if (!ctx.local) return { type: "row", gap: 2, children: toggles };
  const open: Widget = {
    type: "menu",
    id: "open",
    style: "outline",
    icon: "code",
    label: "Open",
    tooltip: "Open in",
    compact: true,
    options: [
      { value: "cursor", label: "Cursor" },
      { value: "vscode", label: "VS Code" },
      { value: "zed", label: "Zed" },
      { value: "finder", label: "Finder" },
    ],
    onChange: "open",
  };
  return { type: "row", gap: 4, children: [open, ...toggles] };
}

export function place({ items }: PlaceArgs<Record<string, never>, HeaderActionsContext>): (Widget | null)[] {
  return items.map((it) => (it.context ? row(it.context) : null));
}

export async function update({ event }: UpdateArgs<Record<string, never>, HeaderActionsContext>): Promise<Update<Record<string, never>>> {
  if (event.kind !== "ui") return {};
  if (event.handler === "panel" && typeof event.payload === "string") {
    await glitched.panels.toggle(event.payload as PanelKind);
  }
  if (event.handler === "open" && typeof event.value === "string" && event.place) {
    await glitched.openIn(event.value as "cursor" | "vscode" | "zed" | "finder" | "editor", event.place.key);
  }
  return {};
}

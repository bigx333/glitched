// The sidebar's project strip, as Glitched draws it by default: All, a
// chip per project that filters the inbox to it, and New project, which
// opens Glitched's New project wizard. The chips wrap, as many as there
// are. Copy it in Settings › Plugins, and the copy draws the strip in its
// place.

function chip(p: ProjectRecord, i: number, s: ProjectStripContext): Widget {
  const name = s.machines && p.machine ? p.name + " on " + p.machine : p.name;
  return {
    type: "button",
    id: "project" + i,
    style: "ghost",
    label: p.name,
    tooltip: (p.filtered ? "Hide " : "Show ") + name,
    active: p.filtered,
    opacity: s.filtered && !p.filtered ? 0.45 : undefined,
    padding: [3, 7, 3, 3],
    gap: 5,
    contextMenu: "project:" + p.key,
    onClick: { handler: "toggle", payload: p.key },
    children: [
      { type: "tile", icon: p.icon, color: p.color, width: 16 },
      { type: "text", text: p.name, fontSize: 11.5 },
    ],
  };
}

function strip(s: ProjectStripContext): Widget {
  return {
    type: "row",
    wrap: true,
    padding: [6, 10, 6, 12],
    gap: 4,
    children: [
      {
        type: "button",
        id: "all",
        style: "ghost",
        label: "All",
        tooltip: "All projects",
        active: !s.filtered,
        padding: [4, 8],
        onClick: "all",
        children: [{ type: "text", text: "All", fontSize: 11.5, weight: 500 }],
      },
      ...s.projects.map((p, i) => chip(p, i, s)),
      {
        type: "button",
        id: "add",
        style: "ghost",
        icon: "plus",
        label: "New project",
        tooltip: "New project",
        padding: 4,
        disabled: s.picking,
        onClick: "add",
      },
    ],
  };
}

export function place({ items }: PlaceArgs<Record<string, never>, ProjectStripContext>): (Widget | null)[] {
  return items.map((it) => (it.context ? strip(it.context) : null));
}

export async function update({ event }: UpdateArgs<Record<string, never>, ProjectStripContext>): Promise<Update<Record<string, never>>> {
  if (event.kind !== "ui") return {};
  const s = event.place?.context;
  if (!s) return {};
  switch (event.handler) {
    case "all":
      await glitched.projects.filter([]);
      break;
    case "toggle": {
      if (typeof event.payload !== "string") break;
      // A filter of every project is no filter, as is one of none.
      const on = new Set(s.projects.filter((p) => p.filtered).map((p) => p.key));
      if (on.has(event.payload)) on.delete(event.payload);
      else on.add(event.payload);
      await glitched.projects.filter([...on]);
      break;
    }
    case "add":
      await glitched.projects.add();
      break;
  }
  return {};
}

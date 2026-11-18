// Settings' About and Keybindings pages, as Glitched draws them by default.
// It is a plugin like any other: copy it in Settings › Plugins, and the
// copy draws the pages in its place.

function settingRow(title, description, control) {
  return {
    type: "row",
    padding: [14, 16],
    gap: 16,
    align: "center",
    children: [
      {
        type: "column",
        grow: true,
        gap: 2,
        children: [
          { type: "text", text: title, weight: 500 },
          { type: "text", text: description, fontSize: 12, style: "muted" },
        ],
      },
      control,
    ],
  };
}

function about(ctx) {
  const check = ctx.checking
    ? { type: "row", gap: 8, children: [{ type: "spinner" }, { type: "text", text: "Checking…", style: "muted" }] }
    : { type: "button", id: "check", style: "bordered", icon: "refresh", label: "Check for updates", onClick: "check" };
  return {
    type: "column",
    gap: 20,
    children: [
      {
        type: "column",
        align: "center",
        justify: "center",
        gap: 10,
        padding: [24, 0],
        children: [
          { type: "image", src: "app:icon", width: 96, height: 96 },
          { type: "text", text: "Glitched", fontSize: 20, weight: 650 },
          { type: "text", text: ctx.version + " · native prototype built with " + ctx.toolkit, style: "muted" },
        ],
      },
      {
        type: "card",
        style: "list",
        children: [
          settingRow("Updates", "Checks for a new version when the app starts.", check),
          settingRow("Source", ctx.source.replace(/^https:\/\//, ""), { type: "link", text: "Open on GitHub", href: ctx.source }),
        ],
      },
    ],
  };
}

function keybindings(ctx, query) {
  const q = query.toLowerCase();
  const shown = ctx.keybindings.filter((k) => k.label.toLowerCase().includes(q));
  const rows = shown.map((k, i) => ({
    type: "row",
    id: "key" + i,
    padding: [11, 16],
    align: "center",
    children: [
      { type: "text", text: k.label, grow: true },
      { type: "keys", keys: k.keys },
    ],
  }));
  return {
    type: "column",
    gap: 20,
    children: [
      { type: "input", id: "search", style: "search", placeholder: "Search keybindings", onChange: "search" },
      { type: "card", style: "list", children: rows.length > 0 ? rows : [{ type: "text", id: "none", text: "No keybindings match.", style: "muted", padding: 16 }] },
    ],
  };
}

export function init() {
  return { state: { query: "" } };
}

export function place({ at, state, items }) {
  return items.map((it) => {
    if (!it.context) return null;
    return at === "settings.page.about" ? about(it.context) : keybindings(it.context, state.query);
  });
}

export async function update({ state, event }) {
  if (event.kind !== "ui") return {};
  switch (event.handler) {
    case "search":
      return { state: { ...state, query: String(event.value ?? "") } };
    case "check":
      await glitched.app.checkForUpdates().catch((e) => glitched.toast("About", e.message));
      return {};
  }
  return {};
}

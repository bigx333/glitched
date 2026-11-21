// The button under the composer that says where the thread runs, as
// Glitched draws it by default: the workspace folder's name, the machine's
// while there is more than one, and, in a new thread's composer, a menu of
// where the thread could start. The menu's choices are Glitched's own, with
// its Add project and Remove project, at the end of the menu's options.
// Copy it in Settings › Plugins, and the copy draws the button in its place.

function face(w) {
  const children = [
    { type: "icon", name: "folder", fontSize: 12 },
    { type: "text", text: w.workspace, fontSize: 11.5 },
  ];
  if (w.machine) children.push({ type: "text", text: "on " + w.machine, fontSize: 11.5, color: "$text.faint" });
  if (w.draft) children.push({ type: "icon", name: "alt-arrow-down", fontSize: 10 });
  return children;
}

function button(w) {
  const base = { id: "workspace", style: "ghost", label: "Workspace", padding: [3, 6], children: face(w) };
  if (!w.draft) return { type: "button", ...base };
  return { type: "menu", ...base, options: [] };
}

export function place({ items }) {
  return items.map((it) => (it.context ? button(it.context) : null));
}

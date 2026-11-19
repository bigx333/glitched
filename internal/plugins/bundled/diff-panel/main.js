// The diff panel, as Glitched draws it by default: the header, and the
// list of changed files over the native diff.body, which Glitched draws.
// It is a plugin like any other: copy it in Settings › Plugins, and the
// copy draws the panel in its place.

// MAX_FILES keeps a copy within the widgets one view may have.
const MAX_FILES = 300;

function header(ctx) {
  const split = { value: "split", label: "Split" };
  if (!ctx.splitFits) Object.assign(split, { disabled: true, tooltip: "Split needs " + ctx.splitWidth + " DIPs of room" });
  return {
    type: "row",
    gap: 8,
    grow: true,
    children: [
      { type: "text", text: "Changes", style: "strong" },
      { type: "badge", text: String(ctx.files.length), style: "count" },
      { type: "spacer" },
      {
        type: "tabs",
        id: "layout",
        style: "segmented",
        value: ctx.layout,
        controlled: true,
        options: [{ value: "unified", label: "Unified" }, split],
        onChange: "layout",
      },
      { type: "button", id: "wrap", style: "ghost", icon: "wrap", label: "Wrap long lines", tooltip: "Wrap long lines", active: ctx.wrap, onClick: "wrap" },
      { type: "button", id: "close", style: "ghost", icon: "close", label: "Close diff", tooltip: "Close diff", onClick: "close" },
    ],
  };
}

function counts(f) {
  const out = [];
  if (f.add > 0) out.push({ type: "text", text: "+" + f.add, style: "code", size: "small", color: "$success" });
  if (f.del > 0) out.push({ type: "text", text: "\u2212" + f.del, style: "code", size: "small", color: "$danger" });
  return out;
}

function fileRow(ctx, f, i) {
  const id = "file" + i;
  const actions = [
    {
      type: "button",
      id: id + ".reviewed",
      style: "ghost",
      size: "small",
      icon: "check-circle",
      label: "Mark reviewed",
      tooltip: "Mark reviewed",
      active: f.reviewed,
      disabled: !f.read,
      onClick: { handler: "reviewed", payload: { path: f.path, reviewed: !f.reviewed } },
    },
    { type: "button", id: id + ".copy", style: "ghost", size: "small", icon: "copy", label: "Copy path", tooltip: "Copy path", onClick: { handler: "copy", payload: f.path } },
  ];
  if (ctx.canOpen) {
    actions.push({ type: "button", id: id + ".open", style: "ghost", size: "small", icon: "square-share-line", label: "Open in editor", tooltip: "Open in editor", onClick: { handler: "open", payload: f.path } });
  }
  return {
    type: "item",
    id,
    icon: f.reviewed ? "check-circle" : "file",
    text: f.name,
    style: "code",
    label: "Diff of " + f.name,
    active: f.path === ctx.shown,
    onClick: { handler: "show", payload: f.path },
    trailing: counts(f),
    actions,
  };
}

function body(ctx) {
  const children = [];
  if (ctx.files.length > 0) {
    const rows = ctx.files.slice(0, MAX_FILES).map((f, i) => fileRow(ctx, f, i));
    if (ctx.files.length > MAX_FILES) rows.push({ type: "text", text: "and " + (ctx.files.length - MAX_FILES) + " more", style: "muted" });
    children.push({ type: "column", padding: 6, gap: 1, children: rows }, { type: "divider" });
  }
  children.push({ type: "native", id: "diff", name: "diff.body", props: { path: ctx.shown }, grow: true });
  return { type: "column", grow: true, children };
}

export function place({ items }) {
  return items.map((it) => (it.context ? (it.context.part === "header" ? header(it.context) : body(it.context)) : null));
}

// update answers what the user did. A call the app refuses, as a file
// whose diff isn't read yet, is told in a toast.
export async function update({ event }) {
  if (event.kind !== "ui") return {};
  const ctx = event.place?.context;
  const thread = ctx?.thread?.id;
  try {
    switch (event.handler) {
      case "layout":
        await glitched.diff.set(thread, { layout: event.value });
        break;
      case "wrap":
        await glitched.diff.set(thread, { wrap: !ctx.wrap });
        break;
      case "close":
        await glitched.panels.toggle("diff", false);
        break;
      case "show":
        await glitched.diff.set(thread, { shown: event.payload });
        break;
      case "reviewed":
        await glitched.diff.setReviewed(thread, event.payload.path, event.payload.reviewed);
        break;
      case "copy":
        await glitched.clipboard.write(event.payload);
        await glitched.toast("Copied", event.payload);
        break;
      case "open":
        await glitched.diff.openFile(thread, event.payload);
        break;
    }
  } catch (e) {
    await glitched.toast("Diff panel", e.message).catch(() => {});
  }
  return {};
}

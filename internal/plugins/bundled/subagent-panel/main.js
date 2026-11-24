// The subagent panel, as Glitched draws it by default: the step's title,
// how many steps the subagent took and how it ended, over what it said
// and did, in order. Glitched scrolls the body. Copy it in Settings ›
// Plugins to change it: each copy's context is the thread, its part
// (header or body), the step's title, the subagent's parts, each a run of
// its text or one of its steps (kind, title, status, exit, output), its
// step count, whether it ended and how, and its tokens.

// MAX_PARTS keeps a copy within the widgets one component's copy may have:
// the latest parts show, and a line says how many came before.
const MAX_PARTS = 400;

// CHUNK keeps each markdown widget's text within a string's 32 KiB: a
// long text is cut between paragraphs, and a paragraph longer still is
// cut where it must be.
const CHUNK = 8000;

const ends = {
  completed: { text: "Done", style: "success" },
  stopped: { text: "Stopped", style: "warning" },
  failed: { text: "Failed", style: "danger" },
  abandoned: { text: "Abandoned", style: "warning" },
};

function plural(n, what) {
  return n === 1 ? "1 " + what : n + " " + what + "s";
}

function header(ctx) {
  const children = [
    { type: "icon", name: "child", color: "$text.muted" },
    { type: "text", text: ctx.title || "Subagent", style: "strong", singleLine: true },
    { type: "badge", text: String(ctx.steps), style: "count" },
    { type: "spacer" },
  ];
  if (!ctx.ended) children.push({ type: "spinner" });
  else if (Object.hasOwn(ends, ctx.outcome)) children.push({ type: "badge", text: ends[ctx.outcome].text, style: ends[ctx.outcome].style });
  children.push({ type: "button", id: "close", style: "ghost", icon: "close", label: "Close subagent", tooltip: "Close subagent", onClick: "close" });
  return { type: "row", gap: 8, grow: true, align: "center", children };
}

function chunks(text) {
  const out = [];
  let cur = "";
  for (const para of text.split("\n\n")) {
    const next = cur === "" ? para : cur + "\n\n" + para;
    if (next.length <= CHUNK) {
      cur = next;
      continue;
    }
    if (cur !== "") out.push(cur);
    cur = para;
    while (cur.length > CHUNK) {
      out.push(cur.slice(0, CHUNK));
      cur = cur.slice(CHUNK);
    }
  }
  if (cur.trim() !== "") out.push(cur);
  return out;
}

const icons = { command: "terminal", edit: "pen", search: "magnifer" };

function step(s) {
  let mark = null;
  if (s.status === "started") mark = { type: "spinner" };
  else if (s.exit != null) mark = { type: "text", text: (s.exit === 0 ? "✓ " : "✗ ") + s.exit, style: "code", size: "small", color: s.exit === 0 ? "$success" : "$danger" };
  else if (s.status === "failed") mark = { type: "icon", name: "close", color: "$danger" };
  const row = { type: "row", gap: 8, align: "center", children: [{ type: "icon", name: icons[s.kind] || "file", color: "$text.faint" }, { type: "text", text: s.title, size: "small", singleLine: true }, { type: "spacer" }] };
  if (mark) row.children.push(mark);
  const failed = s.status === "failed" || (s.exit != null && s.exit !== 0);
  if (!failed || !s.output) return [row];
  return [row, { type: "column", padding: [6, 10], background: "$surface", border: "$border", radius: 6, children: [{ type: "text", text: s.output.slice(-CHUNK), style: "code", size: "small", selectable: true }] }];
}

function body(ctx) {
  const children = [];
  const parts = ctx.parts.slice(-MAX_PARTS);
  if (ctx.parts.length > parts.length) children.push({ type: "text", text: plural(ctx.parts.length - parts.length, "earlier part") + " not shown", style: "muted", size: "small" });
  for (const p of parts) {
    if (p.step) children.push(...step(p.step));
    else for (const text of chunks(p.text)) children.push({ type: "markdown", text });
  }
  if (children.length === 0) children.push({ type: "text", text: ctx.ended ? "The subagent said nothing." : "Starting…", style: "muted" });
  if (ctx.ended && ctx.tokens > 0) children.push({ type: "divider" }, { type: "text", text: ctx.tokens.toLocaleString("en-US") + " tokens, not counted in the thread's", style: "muted", size: "small" });
  return { type: "column", padding: 16, gap: 10, children };
}

export function place({ items }) {
  return items.map((it) => (it.context ? (it.context.part === "header" ? header(it.context) : body(it.context)) : null));
}

export async function update({ event }) {
  if (event.kind !== "ui" || event.handler !== "close") return {};
  try {
    await glitched.panels.toggle("subagent", false);
  } catch (e) {
    await glitched.toast("Subagent panel", e.message).catch(() => {});
  }
  return {};
}

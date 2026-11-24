// The subagent panel, as Glitched draws it by default: the step's title,
// how many steps the subagent took and how it ended, over what it said
// and did, in order. Glitched scrolls the body and keeps the header's
// Close. Copy it in Settings › Plugins to change it: each copy's context
// is the thread, its part (header or body), the step's title, the
// subagent's latest parts, each a run of its text or one of its steps
// (kind, title, status, exit, output), how many parts came before them,
// its step count, whether it ended and how, and its tokens.

// CHUNK keeps each markdown widget's text within a string's 32 KiB: a
// long text is cut between paragraphs, and a paragraph longer still is
// cut between characters, of up to 4 bytes each.
const CHUNK = 8000;

const ends = {
  completed: { text: "Done", style: "success" },
  stopped: { text: "Stopped", style: "warning" },
  failed: { text: "Failed", style: "danger" },
  abandoned: { text: "Abandoned", style: "warning" },
  cut: { text: "Cut off", style: "warning" },
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
  return { type: "row", gap: 8, grow: true, align: "center", children };
}

function chunks(text) {
  const out = [];
  let cur = [];
  for (const para of text.split("\n\n")) {
    const chars = Array.from(para);
    const sep = cur.length === 0 ? 0 : 2;
    if (cur.length + sep + chars.length <= CHUNK) {
      if (sep) cur.push("\n", "\n");
      cur.push(...chars);
      continue;
    }
    if (cur.length > 0) out.push(cur.join(""));
    cur = chars;
    while (cur.length > CHUNK) {
      out.push(cur.slice(0, CHUNK).join(""));
      cur = cur.slice(CHUNK);
    }
  }
  const last = cur.join("");
  if (last.trim() !== "") out.push(last);
  return out;
}

const icons = { command: "terminal", edit: "pen", search: "magnifer" };

function step(s, ended) {
  let mark = null;
  if (s.status === "started" && !ended) mark = { type: "spinner" };
  else if (s.exit != null) mark = { type: "text", text: (s.exit === 0 ? "✓ " : "✗ ") + s.exit, style: "code", size: "small", color: s.exit === 0 ? "$success" : "$danger" };
  else if (s.status === "failed") mark = { type: "icon", name: "close", color: "$danger" };
  const row = { type: "row", gap: 8, align: "center", children: [{ type: "icon", name: icons[s.kind] || "file", color: "$text.faint" }, { type: "text", text: s.title, size: "small", singleLine: true }, { type: "spacer" }] };
  if (mark) row.children.push(mark);
  const failed = s.status === "failed" || (s.exit != null && s.exit !== 0);
  if (!failed || !s.output) return [row];
  return [row, { type: "column", padding: [6, 10], background: "$surface", border: "$border", radius: 6, children: [{ type: "text", text: s.output, style: "code", size: "small", selectable: true }] }];
}

function body(ctx) {
  const children = [];
  if (ctx.earlier > 0) children.push({ type: "text", text: plural(ctx.earlier, "earlier part") + " not shown", style: "muted", size: "small" });
  for (const p of ctx.parts) {
    if (p.step) children.push(...step(p.step, ctx.ended));
    else for (const text of chunks(p.text)) children.push({ type: "markdown", text });
  }
  if (children.length === 0) children.push({ type: "text", text: ctx.ended ? "The subagent said nothing." : "Starting…", style: "muted" });
  if (ctx.ended && ctx.tokens > 0) children.push({ type: "divider" }, { type: "text", text: ctx.tokens.toLocaleString("en-US") + " tokens, not counted in the thread's", style: "muted", size: "small" });
  return { type: "column", padding: 16, gap: 10, children };
}

export function place({ items }) {
  return items.map((it) => (it.context ? (it.context.part === "header" ? header(it.context) : body(it.context)) : null));
}

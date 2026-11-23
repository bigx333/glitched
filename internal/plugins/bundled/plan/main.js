// The thread's plan in its header, as Glitched draws it by default: how
// many of its steps are done, in a ring and in words, and the step the
// agent is at, or the next one; narrow, the count alone. Its
// tooltip lists every step with how far it is. A later plan of the
// agent's replaces the one before. Copy it in Settings › Plugins to change
// it: each copy's context is the thread, whether the header is narrow,
// and the plan's steps, each its text and its status, pending, active or
// done.

// clip is text on one line, its runs of spaces and line breaks one space,
// cut to 40 characters with an ellipsis.
function clip(text) {
  const chars = Array.from(text.split(/[\t\n\r ]+/).filter(Boolean).join(" "));
  return chars.length > 40 ? chars.slice(0, 39).join("") + "…" : chars.join("");
}

const marks = { done: "✓", active: "→", pending: "○" };

function plan({ narrow, steps }) {
  const done = steps.filter((s) => s.status === "done").length;
  const at = steps.find((s) => s.status === "active") || steps.find((s) => s.status === "pending");
  let text = done + " of " + steps.length;
  if (narrow) text = done + "/" + steps.length;
  else if (at) text += " · " + clip(at.text);
  const lines = steps.map((s) => (Object.hasOwn(marks, s.status) ? marks[s.status] : marks.pending) + " " + s.text);
  return {
    type: "badge",
    style: "pill",
    text,
    value: Math.round((done / steps.length) * 1e4) / 1e4,
    tooltip: "Plan, " + done + " of " + steps.length + " done\n" + lines.join("\n"),
  };
}

export function place({ items }) {
  return items.map(({ context }) => (context && context.steps.length > 0 ? plan(context) : null));
}

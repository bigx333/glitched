// Restyles your messages in the chat timeline (the timeline.message
// component). Grant ui.replace in Settings › Plugins and your messages
// draw in this bubble; every other row answers null, so the built-in
// Timeline rows draws it. The words are transcript.body's: a plugin draws
// only what is around them.

const body = { type: "native", id: "body", name: "transcript.body" };
const retry = { type: "native", id: "retry", name: "transcript.retry" };

function row({ kind, note, failed }) {
  if (kind !== "user") {
    return null;
  }
  const bubble = {
    type: "column",
    padding: [8, 12],
    radius: 4,
    background: "$surface",
    border: "$accent",
    children: [body],
  };
  const children = [bubble];
  if (note) {
    const text = { type: "text", text: note, size: "small", color: failed ? "$danger" : "$text.muted" };
    children.push({ type: "row", gap: 8, children: failed ? [text, retry] : [text] });
  }
  return {
    type: "row",
    children: [{ type: "spacer" }, { type: "column", align: "end", gap: 4, maxWidthPercent: 60, children }],
  };
}

export function place({ items }) {
  return items.map(({ context }) => row(context));
}

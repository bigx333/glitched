// A row of the chat timeline, as Glitched draws it by default: the row's
// chrome in widgets, around the native transcript.body, which draws the
// message's own text, a run's work log or an error's words. A message of
// yours is a bubble on the right with its note under it; an error is a
// red outline, with transcript.retry's Retry and Dismiss where it failed a
// command. Copy it in Settings › Plugins and restyle the chrome: each
// copy's context names the row's key and kind, whether it streams, its
// note and whether it failed. A layout that leaves transcript.body out,
// or places it twice, isn't drawn: Glitched draws its own row instead.
// Glitched draws a message's Copy and Edit or Retry over every copy, while
// it is pointed at.

const body = { type: "native", id: "body", name: "transcript.body" };
const retry = { type: "native", id: "retry", name: "transcript.retry" };

function row({ kind, note, failed }) {
  switch (kind) {
    case "user": {
      const bubble = {
        type: "column",
        padding: [10, 14],
        corners: [16, 16, 4, 16],
        background: "$surface",
        border: "$border",
        children: [body],
      };
      const children = [bubble];
      if (note) {
        const text = { type: "text", text: note, size: "small", color: failed ? "$danger" : "$text.faint" };
        children.push({ type: "row", gap: 8, children: failed ? [text, retry] : [text] });
      }
      return {
        type: "row",
        children: [{ type: "spacer" }, { type: "column", align: "end", gap: 6, maxWidthPercent: 80, children }],
      };
    }
    case "failed":
    case "error": {
      const words = { ...body, grow: true };
      return {
        type: "row",
        gap: 8,
        align: "center",
        padding: [6, 6, 6, 14],
        radius: 12,
        border: "$danger",
        borderOpacity: 0.35,
        children: kind === "failed" ? [words, retry] : [words],
      };
    }
    default:
      return body;
  }
}

export function place({ items }) {
  return items.map(({ context }) => row(context));
}

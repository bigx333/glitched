/// <reference path="../glitched.d.ts" />

// Restyles your messages in the chat timeline (the timeline.message
// component). Grant ui.replace in Settings › Plugins and your messages
// draw in this bubble; every other row answers null, so the built-in
// Timeline rows draws it. The words are transcript.body's: a plugin draws
// only what is around them.

const body = { type: "native", id: "body", name: "transcript.body", props: { bare: true } } as const;
const retry = { type: "native", id: "retry", name: "transcript.retry" } as const;

function row({ kind, note, failed }: TimelineMessageContext): Widget | null {
  if (kind !== "user") {
    return null;
  }
  const bubble: Widget = {
    type: "column",
    radius: 4,
    background: "$surface",
    border: "$accent",
    children: [{ ...body, padding: [8, 12] }],
  };
  const children: Widget[] = [bubble];
  if (note) {
    const text: Widget = { type: "text", text: note, size: "small", color: failed ? "$danger" : "$text.muted" };
    children.push({ type: "row", children: [text, retry] });
  }
  return {
    type: "row",
    children: [{ type: "spacer" }, { type: "column", align: "end", gap: 4, maxWidthPercent: 60, children }],
  };
}

export function place({ items }: PlaceArgs<Record<string, never>, TimelineMessageContext>): (Widget | null)[] {
  return items.map(({ context }) => (context ? row(context) : null));
}

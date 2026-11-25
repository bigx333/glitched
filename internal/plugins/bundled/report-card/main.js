// The native report body is the live card of each child report: its state,
// outcome and way into the child. The plugin owns the card's room and the
// line saying how the reports went to the parent's agent.
export function place({ items }) {
  return items.map(({ context }) => {
    const children = [{ type: "native", id: "body", name: "report.body" }];
    if (context.wake) {
      children.push({ type: "text", id: "wake", text: context.wake, fontSize: 11.5, color: "$text.faint", padding: [0, 0, 0, 14] });
    }
    return { type: "column", gap: 6, children };
  });
}

// Quote puts the selection on the composer. Explain does the same with
// this prompt; copying the plugin is how to change what Explain asks.
const explain = "Explain this.";
const action = (id, icon, label) => ({
  type: "button", id, style: "ghost", padding: [5, 8], gap: 5, tooltip: label, onClick: id,
  children: [
    { type: "icon", id: id + "-icon", name: icon, fontSize: 13 },
    { type: "text", id: id + "-label", text: label, fontSize: 12 },
  ],
});
export function place({ items }) {
  return items.map(() => ({ type: "row", gap: 2, children: [action("quote", "quote", "Quote"), action("explain", "sparkles", "Explain")] }));
}
export async function update({ event }) {
  if (event.kind === "ui") await glitched.composer.quote(event.place.context.thread.id, event.handler === "explain" ? explain : "");
  return {};
}

// A row of the queue over the composer, as Glitched draws it by default:
// a message of yours that waits its turn, then what can be done with it,
// each a native Glitched draws and does. queue.text is its words, or the
// area they're edited in; queue.steer sends it now, into the agent's turn
// or as the next one; queue.edit edits it in place (Save and Cancel while
// it's edited); queue.up and queue.down move it past its neighbour; and
// queue.remove takes it out of the queue. A native with nothing to do, as
// Up on the first row, draws nothing and leaves no gap. Copy it in
// Settings › Plugins to reorder, space or drop them. Glitched keeps the
// queue's card and its order.

export function place({ items }) {
  return items.map(() => ({
    type: "row",
    gap: 2,
    align: "center",
    grow: true,
    children: [
      { type: "native", id: "text", name: "queue.text", grow: true },
      { type: "native", id: "steer", name: "queue.steer" },
      { type: "native", id: "edit", name: "queue.edit" },
      { type: "native", id: "up", name: "queue.up" },
      { type: "native", id: "down", name: "queue.down" },
      { type: "native", id: "remove", name: "queue.remove" },
    ],
  }));
}

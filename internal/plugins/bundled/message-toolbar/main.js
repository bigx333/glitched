// The actions under a message, as Glitched draws them by default: a row
// of its natives, each the button Glitched draws and does. message.copy
// copies the message; message.edit (a message of yours) puts it back in
// the composer; message.retry (a reply) asks the agent again; and
// message.item draws the command a plugin put in message.actions at its
// index, of the context's items. Copy it in Settings › Plugins to
// reorder, space or drop them. Glitched keeps where the toolbar sits and
// when it shows.

export function place({ items }) {
  return items.map(({ context }) => ({
    type: "row",
    gap: 2,
    children: [
      { type: "native", id: "copy", name: "message.copy" },
      context.role === "user"
        ? { type: "native", id: "edit", name: "message.edit" }
        : { type: "native", id: "retry", name: "message.retry" },
      ...Array.from({ length: context.items ?? 0 }, (_, i) => ({
        type: "native",
        id: "item-" + i,
        name: "message.item",
        props: { index: i },
      })),
    ],
  }));
}

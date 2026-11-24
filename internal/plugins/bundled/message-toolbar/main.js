// The actions under a message, as Glitched draws them by default: a row
// of its natives, each the button Glitched draws and does. message.copy
// copies the message; message.edit (a message of yours) puts it back in
// the composer; message.retry (a turn that failed or was stopped) asks
// the agent again; message.fork (a turn's last reply) forks the thread
// from that turn into a new one; message.item draws the command a plugin put in
// message.actions at its index, of the context's items; and message.time
// says when the message was sent, on the toolbar's outer side: before the
// actions under a message of yours, after them under a reply. A native with
// nothing to do under a message draws nothing and leaves no gap. Copy it
// in Settings › Plugins to reorder, space or drop them. Glitched keeps
// where the toolbar sits and when it shows.

export function place({ items }) {
  return items.map(({ context }) => {
    const time = { type: "native", id: "time", name: "message.time" };
    const actions = [
      { type: "native", id: "copy", name: "message.copy" },
      { type: "native", id: "edit", name: "message.edit" },
      { type: "native", id: "retry", name: "message.retry" },
      { type: "native", id: "fork", name: "message.fork" },
      ...Array.from({ length: context.items ?? 0 }, (_, i) => ({
        type: "native",
        id: "item-" + i,
        name: "message.item",
        props: { index: i },
      })),
    ];
    return {
      type: "row",
      gap: 2,
      children: context.role === "user" ? [time, ...actions] : [...actions, time],
    };
  });
}

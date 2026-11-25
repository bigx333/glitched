// The built-in Send is the native circular control, so its send/stop
// animation, keyboard state and exact hit target stay Glitched's. A copy
// can arrange it, or draw its own control and call composer.submit or
// threads.interrupt.
export function place({ items }) {
  return items.map(() => ({ type: "native", id: "send", name: "composer.send.button" }));
}

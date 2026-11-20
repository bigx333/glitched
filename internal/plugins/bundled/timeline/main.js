// A row of the chat timeline, as Glitched draws it by default: the native
// transcript.body, which draws the message, its note and its actions, and
// nothing around it. Copy it in Settings › Plugins and draw around it:
// each copy's context names the row's key and kind, whether it streams,
// its note and whether it failed. A layout that leaves transcript.body
// out, or places it twice, isn't drawn: Glitched draws its own row instead.

export function place({ items }) {
  return items.map(() => ({ type: "native", id: "body", name: "transcript.body" }));
}

// The composer, as Glitched draws it by default: the native
// composer.input, which Glitched draws, and nothing around it. Copy it in
// Settings › Plugins and put widgets over or under it. A layout that
// leaves composer.input out, or places it twice, isn't drawn: Glitched
// draws its own composer instead.

export function place({ items }) {
  return items.map(() => ({ type: "native", id: "input", name: "composer.input" }));
}

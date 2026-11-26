// The greetings of a thread with nothing in it yet: over a new thread's
// composer (the hero), and in a thread with no messages. Glitched draws
// them with this plugin by default; copy it in Settings › Plugins, and the
// copy draws them in its place.

function greeting(gap: number, size: number, line: string): Widget {
  return {
    type: "column",
    gap,
    align: "center",
    children: [
      { type: "icon", name: "sparkles", fontSize: 30, color: "$accent" },
      { type: "text", text: "What should we build?", fontSize: size, weight: 600 },
      { type: "text", text: line, style: "muted" },
    ],
  };
}

export function place({ at, items }: PlaceArgs<Record<string, never>, ThreadEmptyContext | Record<string, never>>): Widget[] {
  return items.map(() =>
    at === "hero.heading"
      ? greeting(10, 22, "Send a message to start the thread.")
      : greeting(12, 18, "Send a message to start the conversation."),
  );
}

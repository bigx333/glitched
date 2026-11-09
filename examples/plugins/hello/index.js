function greetings() {
  return host.storage.get("greetings") ?? 0;
}

export function page({ id }) {
  if (id !== "home") throw new Error("no page " + id);
  const n = greetings();
  return {
    title: "Hello",
    subtitle: "A sample plugin's page, drawn by Glitched",
    markdown: "This page is **data** a plugin returned. Glitched draws it with its own components.",
    sections: [
      {
        title: "Greetings",
        items: [
          {
            id: "count",
            title: n === 1 ? "Greeted once" : `Greeted ${n} times`,
            subtitle: "Greet runs the plugin's greet command, which counts in its storage.",
            actions: [{ id: "greet", title: "Greet", kind: "command", meta: { command: "greet" } }],
          },
          {
            id: "docs",
            title: "mygo-plugin",
            subtitle: "The plugin host Glitched runs this with",
            actions: [{ id: "web", title: "Open", kind: "url", url: "https://github.com/yxxbc/mygo-plugin" }],
          },
        ],
      },
    ],
  };
}

export function command({ id }) {
  if (id !== "greet") throw new Error("unknown command: " + id);
  host.storage.set("greetings", greetings() + 1);
  return { greetings: greetings() };
}

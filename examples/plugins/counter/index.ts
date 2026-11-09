/// <reference path="../glitched.d.ts" />

type State = { count: number; name: string; details: boolean };

export function init(): Update<State> {
  return { state: { count: 0, name: "", details: false } };
}

export function view({ state }: ViewArgs<State>): View {
  const greeting: Widget = state.name
    ? { type: "text", id: "greeting", text: `Hello, ${state.name}!` }
    : { type: "text", id: "greeting", text: "Type a name.", style: "muted" };
  const details: Widget[] = state.details
    ? [{ type: "text", id: "length", text: `The name has ${state.name.length} characters.` }]
    : [];
  return {
    title: "Counter",
    subtitle: "A native view that a TypeScript plugin describes",
    body: {
      type: "column",
      gap: 16,
      children: [
        {
          type: "card",
          title: "Counter",
          children: [
            {
              type: "row",
              gap: 12,
              children: [
                { type: "button", id: "dec", label: "Less", onClick: "dec", disabled: state.count <= 0 },
                { type: "text", id: "count", text: `Count: ${state.count}`, style: "heading" },
                { type: "button", id: "inc", label: "More", onClick: "inc", style: "primary" },
              ],
            },
          ],
        },
        {
          type: "card",
          title: "Greeting",
          children: [
            { type: "input", id: "name", label: "Name", placeholder: "Who to greet", value: state.name, controlled: true, onChange: "name" },
            greeting,
            { type: "toggle", id: "details", label: "Show details", value: state.details, controlled: true, onChange: "details" },
            ...details,
          ],
        },
      ],
    },
  };
}

export function update({ state, event }: UpdateArgs<State>): Update<State> {
  if (event.kind !== "ui") return {};
  switch (event.handler) {
    case "inc":
      return { state: { ...state, count: state.count + 1 } };
    case "dec":
      return { state: { ...state, count: Math.max(0, state.count - 1) } };
    case "name":
      return { state: { ...state, name: String(event.value ?? "") } };
    case "details":
      return { state: { ...state, details: event.value === true } };
  }
  return {};
}

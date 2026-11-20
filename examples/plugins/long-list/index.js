// A list of 10,000 rows under a header every 1000. The view names every
// item, as data; Glitched draws the rows in view and asks rows() for those
// alone. Up and Down in the search field move the choice, Enter opens it,
// and a starred row is drawn again as its version changes.
const N = 10000;

export function init() {
  return { state: { chosen: "", opened: "", starred: [] } };
}

export function update({ state, event }) {
  if (event.kind !== "ui") return {};
  switch (event.handler) {
    case "chose":
      return { state: { ...state, chosen: event.value } };
    case "opened":
      return { state: { ...state, opened: event.value + (event.modifiers ? " with " + event.modifiers.join("+") : "") } };
    case "star": {
      const n = Number(event.row.key.slice(1));
      const starred = state.starred.includes(n) ? state.starred.filter((s) => s !== n) : [...state.starred, n];
      return { state: { ...state, starred } };
    }
  }
  return {};
}

export function view({ state }) {
  const items = [];
  for (let i = 0; i < N; i++) {
    if (i % 1000 === 0) items.push({ key: "h" + i, label: "From " + i, header: true, height: 30 });
    items.push({ key: "r" + i, label: "Row " + i, version: state.starred.includes(i) ? "starred" : "" });
  }
  return {
    title: "Long list",
    body: {
      type: "column",
      gap: 8,
      children: [
        { type: "text", style: "muted", text: state.opened ? "Opened " + state.opened : "Nothing opened" },
        { type: "input", id: "find", style: "search", placeholder: "Up and Down move, Enter opens" },
        {
          type: "list", id: "rows", label: "Rows", items, height: 480, gap: 2,
          value: state.chosen, controlled: true, input: "find", onChange: "chose", onSubmit: "opened",
        },
      ],
    },
  };
}

export function rows({ keys, state }) {
  return keys.map((key) => {
    const n = Number(key.slice(1));
    if (key.startsWith("h")) return { type: "row", padding: [0, 8], children: [{ type: "text", text: "From " + n, size: "small", style: "muted" }] };
    const starred = state.starred.includes(n);
    return {
      type: "row", gap: 8, padding: [0, 8], grow: true,
      children: [
        { type: "icon", name: starred ? "check" : "list" },
        { type: "text", text: "Row " + n },
        { type: "spacer" },
        { type: "text", text: starred ? "starred" : "#" + n, style: "muted", size: "small", when: "idle" },
        { type: "button", id: "star", style: "ghost", icon: "check", tooltip: starred ? "Unstar" : "Star", when: "hover", onClick: "star" },
      ],
    };
  });
}

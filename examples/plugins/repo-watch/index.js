// The window: a page that asks the service for the latest check and its
// history. A service call can fail (no grant, no engine): the page says why.

async function load() {
  try {
    const now = await glitched.service.call("check");
    const history = await glitched.service.call("history");
    return { now, history, error: now.error ?? null };
  } catch (e) {
    const why = e.code === "denied" ? "Grant “service” and “programs” to Repo watch in Settings › Plugins." : e.message;
    return { now: null, history: [], error: why };
  }
}

export async function init() {
  return { state: await load() };
}

export function view({ state }) {
  const rows = state.history.map((h, i) => ({
    key: String(i),
    cells: { at: new Date(h.at).toISOString().slice(11, 19), branch: h.branch, changed: String(h.changed) },
  }));
  return {
    title: "Repo watch",
    subtitle: state.now?.branch ? `On ${state.now.branch}` : "",
    body: {
      type: "column",
      gap: 16,
      children: [
        state.error
          ? { type: "text", id: "error", text: state.error, style: "error" }
          : { type: "text", id: "now", text: `${state.now.changed} changed`, style: "heading" },
        { type: "button", id: "refresh", label: "Check now", onClick: "refresh" },
        {
          type: "card",
          title: "Checks",
          children: [
            rows.length
              ? { type: "table", id: "checks", columns: [{ id: "at", title: "At" }, { id: "branch", title: "Branch" }, { id: "changed", title: "Changed" }], rows }
              : { type: "text", id: "none", text: "No checks yet.", style: "muted" },
          ],
        },
      ],
    },
  };
}

export async function update({ event }) {
  if (event.kind === "ui" && event.handler === "refresh") return { state: await load() };
  return {};
}

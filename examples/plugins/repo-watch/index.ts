/// <reference path="../glitched.d.ts" />

// The window: a page that asks the service for the latest check and its
// history. A service call can fail (no grant, no engine): the page says why.

type CheckResult = { branch?: string; changed?: number; error?: string };
type HistoryRow = { at: number; branch: string; changed: number };
type State = { now: CheckResult | null; history: HistoryRow[]; error: string | null };

function errorMessage(e: unknown): { code?: string; message: string } {
  if (e !== null && typeof e === "object") {
    const err = e as { code?: unknown; message?: unknown };
    return {
      code: typeof err.code === "string" ? err.code : undefined,
      message: typeof err.message === "string" ? err.message : String(e),
    };
  }
  return { message: String(e) };
}

async function load(): Promise<State> {
  try {
    const now = await glitched.service.call<CheckResult>("check");
    const history = await glitched.service.call<HistoryRow[]>("history");
    return { now, history, error: now.error ?? null };
  } catch (e) {
    const err = errorMessage(e);
    const why = err.code === "denied" ? "Grant “service” and “programs” to Repo watch in Settings › Plugins." : err.message;
    return { now: null, history: [], error: why };
  }
}

export async function init(): Promise<Update<State>> {
  return { state: await load() };
}

export function view({ state }: ViewArgs<State>): View {
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
          : { type: "text", id: "now", text: `${state.now?.changed ?? 0} changed`, style: "heading" },
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

export async function update({ event }: UpdateArgs<State>): Promise<Update<State>> {
  if (event.kind === "ui" && event.handler === "refresh") return { state: await load() };
  return {};
}

interface PendingSweep {
  secs?: number;
  prompt?: string;
}

interface State {
  popover: string;
  drafts: Record<string, string>;
  pending: Record<string, PendingSweep>;
}

const MAX_INPUT_BYTES = 32000;

const SWEEP_CHOICES = [
  { secs: 0, label: "Off", name: "Sweeps off" },
  { secs: 900, label: "15m", name: "Sweep every 15m" },
  { secs: 1800, label: "30m", name: "Sweep every 30m" },
  { secs: 3600, label: "1h", name: "Sweep every 1h" },
  { secs: 14400, label: "4h", name: "Sweep every 4h" },
];

function utf8Bytes(s: string): number {
  let n = 0;
  for (let i = 0; i < s.length; i++) {
    const c = s.charCodeAt(i);
    if (c < 0x80) n += 1;
    else if (c < 0x800) n += 2;
    else if (c >= 0xd800 && c <= 0xdbff && i + 1 < s.length) {
      n += 4;
      i++;
    } else n += 3;
  }
  return n;
}

function truncateUtf8(s: string, maxBytes: number): string {
  let n = 0;
  let i = 0;
  while (i < s.length) {
    const c = s.charCodeAt(i);
    let step = 1;
    let bytes = 1;
    if (c < 0x80) {
      bytes = 1;
    } else if (c < 0x800) {
      bytes = 2;
    } else if (c >= 0xd800 && c <= 0xdbff && i + 1 < s.length) {
      bytes = 4;
      step = 2;
    } else {
      bytes = 3;
    }
    if (n + bytes > maxBytes) break;
    n += bytes;
    i += step;
  }
  return s.slice(0, i);
}

function sweepLabel(secs: number): string {
  if (secs <= 0) return "Sweep off";
  if (secs % 3600 === 0) return `Sweep ${secs / 3600}h`;
  if (secs % 60 === 0) return `Sweep ${secs / 60}m`;
  return `Sweep ${secs}s`;
}

function withoutKey<T>(rec: Record<string, T>, key: string): Record<string, T> {
  if (!(key in rec)) return rec;
  const next = { ...rec };
  delete next[key];
  return next;
}

function withPending(rec: Record<string, PendingSweep>, key: string, next: PendingSweep): Record<string, PendingSweep> {
  if (next.secs === undefined && next.prompt === undefined) return withoutKey(rec, key);
  return { ...rec, [key]: next };
}

function sweepPopover(t: ThreadRecord, at: number, saved: string, draft: string): { type: "column"; width: number; padding: number; gap: number; children: Widget[] } {
  const mgr = t.manager ?? {};
  const tooLong = utf8Bytes(draft) > MAX_INPUT_BYTES;
  const trimmed = draft.trim();
  const actions: Widget[] = [
    { type: "button", id: "sweep-save", label: "Save prompt", disabled: tooLong || trimmed === saved, onClick: "sweep.save" },
  ];
  if (saved !== "") {
    actions.push({ type: "button", id: "sweep-reset", label: "Reset", onClick: "sweep.reset" });
  }
  actions.push(
    { type: "spacer" },
    { type: "button", id: "sweep-run", label: "Run sweep now", style: "primary", disabled: Boolean(mgr.sweepBlocked), onClick: "sweep.run" },
  );
  const promptField: Widget = tooLong
    ? { type: "text", text: "Standing prompt is too long to edit here; edit it in Overview.", fontSize: 11.5, color: "$text.muted" }
    : {
        type: "input",
        id: "sweep-prompt",
        label: "Standing sweep prompt",
        placeholder: "Default: verify finished workers, steer stalled ones, update WORKERS.md and NOTES.md, and stay quiet unless you must act.",
        value: draft,
        controlled: true,
        multiline: true,
        onChange: "sweep.draft",
      };
  return {
    type: "column" as const,
    width: 340,
    padding: 14,
    gap: 12,
    children: [
      {
        type: "column",
        gap: 2,
        children: [
          { type: "text", text: "Sweep schedule", fontSize: 13, weight: 600 },
          { type: "text", text: mgr.sweepTiming || "Sweeps off", fontSize: 11.5, color: "$text.muted" },
        ],
      },
      {
        type: "row",
        gap: 6,
        children: SWEEP_CHOICES.map((c) => ({
          type: "button" as const,
          id: `cadence-${c.secs}`,
          style: "ghost" as const,
          label: c.name,
          active: at === c.secs,
          padding: [3, 8] as [number, number],
          children: [{ type: "text" as const, text: c.label, fontSize: 11.5, color: at === c.secs ? ("$accent" as const) : ("$text.muted" as const), singleLine: true }],
          onClick: { handler: "sweep.cadence", payload: c.secs },
        })),
      },
      promptField,
      { type: "row", gap: 6, align: "center", children: actions },
    ],
  };
}

function overviewButton(t: ThreadRecord): Widget {
  const mgr = t.manager ?? {};
  const needs = mgr.needs ?? 0;
  const active = mgr.active ?? 0;
  const children: Widget[] = [
    { type: "icon", name: "manager", fontSize: 11, color: "$accent" },
    { type: "text", text: "Overview", fontSize: 11, color: "$text.muted", singleLine: true },
  ];
  if (needs > 0) {
    children.push({ type: "text", text: String(needs), fontSize: 10.5, weight: 600, color: "$warning", singleLine: true });
  } else if (active > 0) {
    children.push({ type: "text", text: String(active), fontSize: 10.5, weight: 600, color: "$accent", singleLine: true });
  }
  return {
    type: "button",
    id: "overview",
    style: "ghost",
    label: "Overview",
    tooltip: "Toggle overview",
    active: Boolean(mgr.overviewOpen),
    gap: 4,
    padding: [2, 7],
    children,
    onClick: "overview.toggle",
  };
}

export function init(): Update<State> {
  return { state: { popover: "", drafts: {}, pending: {} } };
}

export function place({ state, items }: PlaceArgs<State, ThreadRecord>): (Widget | null)[] {
  return items.map(({ key, context, compact }): Widget | null => {
    if (context?.role !== "manager") return null;
    const open = state.popover === key;
    const pend = open ? state.pending[key] : undefined;
    const at = pend?.secs ?? context.sweep?.intervalSeconds ?? 0;
    const saved = pend?.prompt ?? context.sweep?.prompt ?? "";
    const label = sweepLabel(at);
    const draft = key in state.drafts ? state.drafts[key] : saved;
    const popover = open ? sweepPopover(context, at, saved, draft) : { type: "column" as const, width: 340, children: [] as Widget[] };
    const sweep: Widget = compact
      ? {
          type: "button",
          id: "sweep",
          style: "ghost",
          icon: "clock-circle",
          label,
          tooltip: label,
          active: open,
          open,
          onClick: "sweep.toggle",
          onClose: "sweep.close",
          popover,
        }
      : {
          type: "button",
          id: "sweep",
          style: "ghost",
          label,
          active: open,
          open,
          padding: [2, 7],
          children: [{ type: "text", text: label, fontSize: 11, color: "$text.muted", singleLine: true }],
          onClick: "sweep.toggle",
          onClose: "sweep.close",
          popover,
        };
    if (compact) return sweep;
    const overview = overviewButton(context);
    const badge: Widget = {
      type: "row",
      gap: 4,
      align: "center",
      padding: [2, 7],
      radius: 999,
      border: "$accent",
      borderOpacity: 0.28,
      children: [
        { type: "icon", name: "manager", fontSize: 11, color: "$accent" },
        { type: "text", text: "Manager", fontSize: 11, color: "$accent", singleLine: true },
      ],
    };
    return { type: "row", gap: 8, align: "center", children: [badge, sweep, overview] };
  });
}

export async function update({ state, event }: UpdateArgs<State, ThreadRecord>): Promise<Update<State>> {
  if (event.kind === "app" && event.type === "place.shown") {
    const key = event.data.key;
    const pend = state.pending[key];
    if (!pend) return {};
    const ctx = event.data.context as ThreadRecord | undefined;
    if (!ctx) return {};
    const nextPend: PendingSweep = { ...pend };
    if (nextPend.secs !== undefined && (ctx.sweep?.intervalSeconds ?? 0) === nextPend.secs) {
      delete nextPend.secs;
    }
    if (nextPend.prompt !== undefined && (ctx.sweep?.prompt ?? "") === nextPend.prompt) {
      delete nextPend.prompt;
    }
    if (nextPend.secs === pend.secs && nextPend.prompt === pend.prompt) return {};
    const pending =
      nextPend.secs === undefined && nextPend.prompt === undefined
        ? withoutKey(state.pending, key)
        : { ...state.pending, [key]: nextPend };
    return { state: { ...state, pending } };
  }
  if (event.kind === "app" && event.type === "place.hidden") {
    const key = event.data.key;
    if (state.popover !== key && !(key in state.pending)) return {};
    return {
      state: {
        ...state,
        popover: state.popover === key ? "" : state.popover,
        pending: withoutKey(state.pending, key),
      },
    };
  }
  if (event.kind !== "ui" || !event.place?.context) return {};
  const key = event.place.key;
  const t = event.place.context;
  const pend = state.pending[key];
  const at = pend?.secs ?? t.sweep?.intervalSeconds ?? 0;
  const saved = pend?.prompt ?? t.sweep?.prompt ?? "";
  switch (event.handler) {
    case "sweep.toggle":
      return {
        state: {
          ...state,
          popover: state.popover === key ? "" : key,
          pending: withoutKey(state.pending, key),
        },
      };
    case "sweep.close":
      return state.popover === key || key in state.pending
        ? { state: { ...state, popover: "", pending: withoutKey(state.pending, key) } }
        : {};
    case "sweep.cadence": {
      const secs = typeof event.payload === "number" ? event.payload : 0;
      if (secs === at) return {};
      await glitched.threads.configure(t.id, { sweep: { intervalSeconds: secs, ...(saved ? { prompt: saved } : {}) } });
      const next: PendingSweep = { ...pend };
      if ((t.sweep?.intervalSeconds ?? 0) === secs) delete next.secs;
      else next.secs = secs;
      return { state: { ...state, pending: withPending(state.pending, key, next) } };
    }
    case "sweep.draft": {
      const raw = typeof event.value === "string" ? event.value : "";
      const val = utf8Bytes(raw) > MAX_INPUT_BYTES ? truncateUtf8(raw, MAX_INPUT_BYTES) : raw;
      const drafts = val === saved ? withoutKey(state.drafts, key) : { ...state.drafts, [key]: val };
      return { state: { ...state, drafts } };
    }
    case "sweep.save": {
      const cur = key in state.drafts ? state.drafts[key] : saved;
      const trimmed = cur.trim();
      await glitched.threads.configure(t.id, { sweep: { intervalSeconds: at, ...(trimmed ? { prompt: trimmed } : {}) } });
      const next: PendingSweep = { ...pend };
      if ((t.sweep?.prompt ?? "") === trimmed) delete next.prompt;
      else next.prompt = trimmed;
      return { state: { ...state, drafts: withoutKey(state.drafts, key), pending: withPending(state.pending, key, next) } };
    }
    case "sweep.reset": {
      await glitched.threads.configure(t.id, { sweep: { intervalSeconds: at } });
      const next: PendingSweep = { ...pend };
      if ((t.sweep?.prompt ?? "") === "") delete next.prompt;
      else next.prompt = "";
      return { state: { ...state, drafts: withoutKey(state.drafts, key), pending: withPending(state.pending, key, next) } };
    }
    case "sweep.run":
      await glitched.threads.sweepNow(t.id);
      return { state: { ...state, popover: "", pending: withoutKey(state.pending, key) } };
    case "overview.toggle":
      await glitched.panels.toggle("overview");
      return { state: { ...state, popover: "", pending: withoutKey(state.pending, key) } };
  }
  return {};
}

// The skills Glitched gives a thread's agent, in the thread's header, as
// the engine of the thread's machine finds them. A thread's List is what
// glitched.skills.list answers:
//
//   { skills: [{ name, description, source, shown, overrides }], skipped: [{ source, name, why }], delivery }
//
// source is "home", the user's own, or "project". The pill shows while a
// thread has a skill or a skipped one; its popover lists them.

const SOURCES = { home: "Yours", project: "Project" };

export function init() {
  return { state: { lists: {}, shown: null, open: null } };
}

export function place({ state, items }) {
  return items.map(({ key, context, compact }) => {
    const list = context?.id ? state.lists[context.id] : null;
    if (!list || list.skills.length + (list.skipped?.length ?? 0) === 0) return null;
    return pill(list, state.open === key, compact);
  });
}

// pill is a thread's skills, by count; compact, it keeps to its icon,
// and its label names it to assistive tools.
function pill(list, open, compact) {
  const n = list.skills.length;
  const skipped = list.skipped?.length ?? 0;
  return {
    type: "button",
    id: "skills",
    style: "ghost",
    label: "Skills " + n,
    tooltip: count(n, "skill") + " for this thread's agent" + (skipped ? ", " + skipped + " skipped" : ""),
    gap: 4,
    children: [
      { type: "icon", name: "magic-stick-3", color: skipped ? "$warning" : "$text.muted" },
      ...(compact ? [] : [{ type: "text", text: "Skills " + n }]),
    ],
    active: open,
    open,
    onClick: "popover.toggle",
    onClose: "popover.close",
    popover: { type: "column", width: 340, padding: 12, gap: 10, children: popover(list) },
  };
}

function count(n, what) {
  return n + " " + what + (n === 1 ? "" : "s");
}

function popover(list) {
  const out = [{ type: "text", text: "Skills", style: "strong" }];
  if (list.skills.length === 0) out.push({ type: "text", text: "None for this thread.", style: "muted" });
  for (const s of list.skills) out.push(skill(s));
  if (list.skipped?.length) {
    out.push({ type: "text", text: "Skipped", style: "strong" });
    for (const s of list.skipped) out.push(skipped(s));
  }
  out.push({ type: "text", id: "delivery", text: list.delivery || "This thread's agent doesn't get them.", style: "muted" });
  return out;
}

function skill(s) {
  const badges = [{ type: "badge", text: SOURCES[s.source] ?? s.source, style: s.source === "project" ? "accent" : "pill" }];
  if (s.overrides) badges.push({ type: "badge", text: "replaces yours", style: "warning" });
  return {
    type: "column",
    id: "skill:" + s.source + ":" + s.name,
    gap: 2,
    children: [
      { type: "row", gap: 6, align: "center", children: [{ type: "text", text: s.name, singleLine: true }, { type: "spacer" }, ...badges] },
      { type: "text", text: s.description, style: "muted", selectable: true },
      { type: "text", text: s.shown, style: "muted", singleLine: true, selectable: true },
    ],
  };
}

function skipped(s) {
  return {
    type: "column",
    id: "skipped:" + s.source + ":" + s.name,
    gap: 2,
    children: [
      { type: "row", gap: 6, align: "center", children: [{ type: "icon", name: "shield-warning", color: "$warning" }, { type: "text", text: s.name || "A skills folder", singleLine: true }, { type: "spacer" }, { type: "badge", text: SOURCES[s.source] ?? s.source, style: "pill" }] },
      { type: "text", text: s.why, style: "muted", selectable: true },
    ],
  };
}

// read is state with the skills of the thread shown read again. A read
// that fails, as of an engine too old for the call, keeps what was read.
async function read(state) {
  const thread = state.shown?.thread;
  if (!thread) return { state };
  const list = await glitched.skills.list(thread).catch(() => null);
  return list ? { state: { ...state, lists: { ...state.lists, [thread]: list } } } : { state };
}

export async function update({ state, event }) {
  if (event.kind === "app") {
    switch (event.type) {
      case "place.shown": {
        const thread = event.data.context?.id;
        return read({ ...state, shown: thread ? { key: event.data.key, thread } : null, open: null });
      }
      case "place.hidden":
        return { state: { ...state, shown: state.shown?.key === event.data.key ? null : state.shown, open: state.open === event.data.key ? null : state.open } };
      case "window.focused":
        return event.data.focused ? read(state) : {};
    }
    return {};
  }
  if (event.kind !== "ui" || !event.place) return {};
  switch (event.handler) {
    case "popover.toggle":
      return { state: { ...state, open: state.open === event.place.key ? null : event.place.key } };
    case "popover.close":
      return { state: { ...state, open: null } };
  }
  return {};
}

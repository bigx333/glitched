// The skills Glitched gives a thread's agent, in the thread's header, as
// the engine of the thread's machine last said them: the header's record
// holds them as glitched.skills.list answers,
//
//   { skills: [{ name, description, source, shown, from, replaces }], skipped: [{ source, name, why }], delivery, synced: { name, pulled } }
//
// and leaves them out till the engine says. source is "home", the user's
// own on the thread's machine, "synced", the user's from the machine
// from, or "project"; replaces is the source of the skill of its name it
// replaces. synced says which machine gave the synced ones, and when it
// last did. The pill shows while a thread has a skill or a skipped one;
// its popover lists them.

type State = { open: string | null };
type NormalizedSkills = { skills: Skill[]; skipped: SkippedSkill[]; delivery: string; synced: SyncedFrom | null };

const SOURCES: Record<string, string> = { home: "Yours", synced: "Synced", project: "Project" };

export function init(): Update<State> {
  return { state: { open: null } };
}

export function place({ state, items }: PlaceArgs<State, ThreadRecord>): (Widget | null)[] {
  return items.map(({ key, context, compact }) => {
    const list = skillsOf(context);
    if (!list || list.skills.length + list.skipped.length === 0) return null;
    return pill(list, state.open === key, !!compact);
  });
}

// skillsOf is the header's skills with every list there, or null before
// the engine says.
function skillsOf(context: ThreadRecord | undefined): NormalizedSkills | null {
  const s = context?.skills;
  return s ? { skills: s.skills ?? [], skipped: s.skipped ?? [], delivery: s.delivery ?? "", synced: s.synced ?? null } : null;
}

// pill is a thread's skills, by count; compact, it keeps to its icon,
// and its label names it to assistive tools.
function pill(list: NormalizedSkills, open: boolean, compact: boolean): Widget {
  const n = list.skills.length;
  const skipped = list.skipped.length;
  return {
    type: "button",
    id: "skills",
    style: "ghost",
    label: "Skills " + n,
    tooltip: count(n, "skill") + " for this thread's agent" + (skipped ? ", " + skipped + " skipped" : ""),
    gap: 4,
    children: [
      { type: "icon", name: "magic-stick-3", color: skipped ? "$warning" : "$text.muted" },
      ...(compact ? [] : [{ type: "text" as const, text: "Skills " + n }]),
    ],
    active: open,
    open,
    onClick: "popover.toggle",
    onClose: "popover.close",
    popover: { type: "column", width: 340, padding: 12, gap: 10, children: popover(list) },
  };
}

function count(n: number, what: string): string {
  return n + " " + what + (n === 1 ? "" : "s");
}

function popover(list: NormalizedSkills): Widget[] {
  const out: Widget[] = [{ type: "text", text: "Skills", style: "strong" }];
  if (list.synced) out.push({ type: "text", id: "synced", text: "Synced from " + list.synced.name + " " + ago(list.synced.pulled), style: "muted" });
  if (list.skills.length === 0) out.push({ type: "text", text: "None for this thread.", style: "muted" });
  for (const s of list.skills) out.push(skill(s, list.synced));
  if (list.skipped.length) {
    out.push({ type: "text", text: "Skipped", style: "strong" });
    for (const s of list.skipped) out.push(skipped(s));
  }
  out.push({ type: "text", id: "delivery", text: list.delivery || "This thread's agent doesn't get them.", style: "muted" });
  return out;
}

const UNITS: [string, number][] = [["d", 86400], ["h", 3600], ["m", 60]];

// ago is how long before now at, an ISO time, was, as the app says it.
function ago(at: string): string {
  const s = (Date.now() - Date.parse(at)) / 1000;
  if (!(s >= 60)) return "just now";
  for (const [unit, n] of UNITS) {
    if (s >= n) return Math.floor(s / n) + unit + " ago";
  }
  return "just now";
}

const REPLACED: Record<string, (synced: SyncedFrom | null) => string> = {
  home: () => "replaces yours",
  synced: (synced) => "replaces " + (synced?.name ?? "the synced") + "'s",
};

function skill(s: Skill, synced: SyncedFrom | null): Widget {
  const badges: Widget[] = [{ type: "badge", text: s.source === "synced" ? "From " + s.from : SOURCES[s.source] ?? s.source, style: s.source === "project" ? "accent" : "pill" }];
  if (s.replaces && REPLACED[s.replaces]) badges.push({ type: "badge", text: REPLACED[s.replaces](synced), style: "warning" });
  return {
    type: "column",
    id: "skill:" + s.source + ":" + s.name,
    gap: 2,
    children: [
      { type: "row", gap: 6, align: "center", children: [{ type: "text", text: s.name, singleLine: true }, { type: "spacer" }, ...badges] },
      { type: "text", text: s.description, style: "muted", selectable: true },
      ...(s.source === "synced" ? [] : [{ type: "text" as const, text: s.shown, style: "muted" as const, singleLine: true, selectable: true }]),
    ],
  };
}

function skipped(s: SkippedSkill): Widget {
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

// A popover shuts as its header goes, as another thread shows: it opens
// again only on a click.
export function update({ state, event }: UpdateArgs<State, ThreadRecord>): Update<State> {
  if (event.kind === "app" && event.type === "place.hidden") {
    return state.open === event.data.key ? { state: { ...state, open: null } } : {};
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

// The Schedules page in Settings, and its New schedule command. The page
// sends every write through the service, the only writer of glitched.db,
// and reads the schedules back from it: whole, page after page, by id, in
// one call of update, so a schedule renamed meanwhile is listed once. It
// keeps of each only what its row shows, and reads one whole to edit it,
// so its state stays small however long the prompts are.
//
// Its rows are laid out as Settings' own (settings.go): a padding and gaps
// in points around texts at the app's font sizes, with no fixed height, so
// each row is as tall as its texts are on the system it runs on.

const ACCESS = ["Ask for approval", "Approve for me", "Full access"];
const DAYS = ["Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"];
const MONTHS = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];
const MINUTE = 60_000;
// A Run now's op is refused once it is 7 days old, so one kept for a try
// again is let go a day before.
const KEEP_OP = 6 * 1440 * MINUTE;
// A Run now's call gives up in time for its update to keep the op, inside
// the 30 s an update has.
const RUN_TIMEOUT = 20;
// Rows drawn at once: a row is about a dozen widgets, of the 2000 a view
// may have.
const SHOWN = 100;

const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ";
function ulid(t) {
  let out = "";
  for (let i = 9, n = t; i >= 0; i--, n = Math.floor(n / 32)) {
    out = crockford[n % 32] + out;
  }
  for (let i = 0; i < 16; i++) {
    out += crockford[Math.floor(Math.random() * 32)];
  }
  return out;
}
const opTime = (op) => [...op.slice(0, 10)].reduce((n, c) => n * 32 + crockford.indexOf(c), 0);

// asked is set by the command for the page to open on an empty form: as
// it starts, if it wasn't open, or as it hears the command ran.
let asked = false;

export async function command({ id }) {
  if (id !== "new") return;
  asked = true;
  await glitched.pages.open("schedules");
}

const call = (method, args, options) => glitched.service.call(method, args, options);

// read lists every schedule, by title, and what the form chooses from:
// this machine's projects, and the models the picker offers. It keeps the
// rows shown, from offset, and how many there are.
async function read(state, offset) {
  const [all, models] = await Promise.all([glitched.projects.list(), glitched.models.list()]);
  const projects = named(all.filter((p) => p.local));
  const byID = new Map();
  for (let after; ; ) {
    const page = await call("list", { after });
    for (const s of page.schedules) byID.set(s.id, s);
    if (page.next === null) break;
    after = page.next;
  }
  const schedules = [...byID.values()].sort((a, b) => a.title.localeCompare(b.title) || (a.id < b.id ? -1 : 1));
  const at = Math.max(0, Math.min(offset, Math.floor((schedules.length - 1) / SHOWN) * SHOWN));
  const rows = schedules.slice(at, at + SHOWN).map((s) => shown(projects, s));
  const notes = Object.fromEntries(rows.flatMap((r) => (state.notes[r.id] ? [[r.id, state.notes[r.id]]] : [])));
  return { ...state, rows, count: schedules.length, offset: at, notes, projects, models: models.models, error: null };
}

// named gives each project the name the form shows: the folder's own, or
// with its parents' while another project has it.
function named(projects) {
  const parts = (p) => p.workspace.split(/[\\/]/).filter(Boolean);
  return projects.map((p) => {
    let n = 1;
    const label = (q, k) => (k === 1 ? q.name : parts(q).slice(-k).join("/"));
    while (n < parts(p).length && projects.some((q) => q !== p && label(q, n) === label(p, n))) n++;
    return { workspace: p.workspace, name: label(p, n) };
  });
}

// shown is what s's row shows, and the revision its switch saves over.
function shown(projects, s) {
  return {
    id: s.id, title: s.title, project: projectName(projects, s.workspace), kind: s.kind, every: s.every, at: s.at,
    weekdays: s.weekdays, zone: s.zone, enabled: s.enabled, nextDue: s.nextDue, revision: s.revision,
    lastThread: s.lastThread, lastState: s.lastState, lastReason: s.lastReason,
  };
}

async function refresh(state, offset = state.offset) {
  try {
    return await read(state, offset);
  } catch (e) {
    return { ...state, error: "Couldn't read the schedules: " + e.message };
  }
}

function takeAsked(state) {
  if (!asked) return state;
  asked = false;
  return { ...state, form: blank(state) };
}

export async function init({ page }) {
  if (page !== "schedules") return {};
  const state = await refresh({ rows: [], count: 0, offset: 0, projects: [], models: [], form: null, deleting: null, notes: {}, error: null });
  return { state: takeAsked(state) };
}

const modelValue = (provider, id) => provider + " " + id;

// blank is the form of a new schedule, with the id it is saved under on
// every try.
function blank(state) {
  const m = state.models[0];
  return {
    key: ulid(Date.now()), id: ulid(Date.now()), editing: false,
    title: "", prompt: "", workspace: state.projects[0]?.workspace ?? "",
    model: m ? modelValue(m.provider, m.id) : "", effort: m?.efforts.includes("medium") ? "medium" : (m?.efforts[0] ?? ""),
    access: ACCESS[0], kind: "weekly", every: "60", at: "09:00", weekdays: [1, 2, 3, 4, 5],
    zone: Intl.DateTimeFormat().resolvedOptions().timeZone, missed: "once", worktree: false, error: null,
  };
}

function editing(s) {
  return {
    key: ulid(Date.now()), id: s.id, editing: true, revision: s.revision,
    title: s.title, prompt: s.prompt, workspace: s.workspace, model: modelValue(s.provider, s.model), effort: s.effort,
    access: s.access, kind: s.kind, every: String(s.every ?? 60), at: s.at ?? "09:00", weekdays: s.weekdays,
    zone: s.zone, missed: s.missed, worktree: s.worktree, error: null,
  };
}

// kept is the form with what its fields hold now, so a field the next view
// leaves out, as the days are for an interval, comes back as it was.
function kept(state, form, values) {
  const v = (k, was) => values[form.key + ":" + k] ?? was;
  const days = DAYS.map((_, i) => v("day" + (i + 1), form.weekdays.includes(i + 1)));
  return {
    ...form, title: v("title", form.title), prompt: v("prompt", form.prompt), workspace: v("project", form.workspace),
    model: v("model", form.model), effort: values[effortID(state, form, form.model)] ?? form.effort, access: v("access", form.access),
    kind: v("kind", form.kind), every: v("every", form.every), at: v("at", form.at), zone: v("zone", form.zone),
    weekdays: days.flatMap((on, i) => (on ? [i + 1] : [])), missed: v("missed", form.missed), worktree: v("worktree", form.worktree),
  };
}

// fromForm is the schedule the form's values make. An edit saves over the
// revision it read, so it neither brings back a schedule deleted meanwhile
// nor undoes another save; it leaves out whether it's on, which its switch
// says.
function fromForm(state, form, values) {
  const f = kept(state, form, values);
  const space = f.model.indexOf(" ");
  const s = {
    id: f.id, title: f.title.trim(), prompt: f.prompt, workspace: f.workspace,
    provider: f.model.slice(0, space), model: f.model.slice(space + 1), effort: f.effort,
    access: f.access, kind: f.kind, zone: f.zone.trim(), missed: f.missed, worktree: f.worktree,
  };
  if (f.editing) s.revision = f.revision;
  else s.enabled = true;
  if (f.kind === "interval") {
    const every = f.every.trim();
    s.every = /^\d+$/.test(every) ? Number(every) : null;
  } else {
    s.at = f.at.trim();
    s.weekdays = f.weekdays;
  }
  return s;
}

// The effort's select is the model's, so choosing another model starts it
// over at that model's.
const effortID = (state, form, model) => form.key + ":effort:" + state.models.findIndex((m) => modelValue(m.provider, m.id) === model);

const note = (state, id, text) => ({ ...state, notes: { ...state.notes, [id]: text } });

// redrawn is state with s's row as a write answered it, should the list
// after it fail.
const redrawn = (state, s) => ({ ...state, rows: state.rows.map((r) => (r.id === s.id ? shown(state.projects, s) : r)) });

export async function update({ page, state, event, values }) {
  if (page !== "schedules") return {};
  if (event.kind === "app") return event.type === "command" ? { state: takeAsked(state) } : {};
  if (event.kind !== "ui") return {};
  // A row's control names its schedule, and the revision it drew, in its
  // payload: what it does is the row's the user clicked, whichever rows
  // show by the time it runs.
  const { id, revision, title, thread } = event.payload ?? {};
  switch (event.handler) {
    case "refresh":
      return { state: await refresh(state) };
    case "new":
      return { state: { ...state, form: blank(state) } };
    case "edit":
      try {
        return { state: note({ ...state, form: editing(await call("get", { id })) }, id, null) };
      } catch (e) {
        return { state: note(state, id, "Couldn't read it: " + e.message) };
      }
    case "page":
      return { state: await refresh(state, state.offset + (event.payload.by ?? 0)) };
    case "cancel":
      return { state: { ...state, form: null } };
    case "kind":
    case "model":
      return state.form ? { state: { ...state, form: kept(state, state.form, values) } } : {};
    case "save": {
      const form = state.form;
      if (!form) return {};
      try {
        const saved = await call("save", { schedule: fromForm(state, form, values) });
        return { state: await refresh(redrawn({ ...state, form: null }, saved)) };
      } catch (e) {
        return { state: { ...state, form: { ...form, error: e.message } } };
      }
    }
    case "enable":
      try {
        const saved = await call("save", { schedule: { id, enabled: event.value, revision } });
        return { state: await refresh(note(redrawn(state, saved), id, null)) };
      } catch (e) {
        return { state: await refresh(note(state, id, "Couldn't turn it " + (event.value ? "on" : "off") + ": " + e.message)) };
      }
    case "run": {
      // The op is kept, in the plugin's storage, from before it is sent
      // till the service answers it: an answer lost, or an update that
      // never returned, has the next click try the same Run now again.
      // Its answer shows at once, with no list after it to wait on.
      const key = "runNow:" + id;
      const kept = host.storage.get(key);
      const op = kept && Date.now() - opTime(kept) < KEEP_OP ? kept : ulid(Date.now());
      host.storage.set(key, op);
      let started;
      try {
        started = await call("runNow", { id, op }, { timeout: RUN_TIMEOUT });
      } catch (e) {
        return { state: note(state, id, "Run now didn't answer: " + e.message + ". Run now tries it again.") };
      }
      host.storage.remove(key);
      return { state: note(started.schedule ? redrawn(state, started.schedule) : state, id, null) };
    }
    case "open":
      if (thread) await glitched.threads.open(thread);
      return {};
    case "delete":
      return { state: { ...state, deleting: { id, title } } };
    case "keep":
      return { state: { ...state, deleting: null } };
    case "confirmDelete": {
      const gone = state.deleting?.id;
      if (!gone) return {};
      try {
        await call("delete", { id: gone });
        host.storage.remove("runNow:" + gone);
        const next = { ...state, deleting: null, rows: state.rows.filter((x) => x.id !== gone) };
        return { state: await refresh(state.form?.id === gone ? { ...next, form: null } : next) };
      } catch (e) {
        return { state: note({ ...state, deleting: null }, gone, "Couldn't delete it: " + e.message) };
      }
    }
  }
  return {};
}

// --- How a schedule reads -----------------------------------------------------

function offset(t, zone) {
  const f = new Intl.DateTimeFormat("en-US", {
    timeZone: zone, hourCycle: "h23",
    year: "numeric", month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", second: "2-digit",
  });
  const p = Object.fromEntries(f.formatToParts(new Date(t)).map((x) => [x.type, Number(x.value)]));
  return Date.UTC(p.year, p.month - 1, p.day, p.hour % 24, p.minute, p.second) - Math.floor(t / 1000) * 1000;
}

// clock is t on zone's clocks: "Tue 25 Nov, 09:00".
function clock(t, zone) {
  const d = new Date(t + offset(t, zone));
  const two = (n) => String(n).padStart(2, "0");
  return DAYS[(d.getUTCDay() + 6) % 7] + " " + d.getUTCDate() + " " + MONTHS[d.getUTCMonth()] + ", " + two(d.getUTCHours()) + ":" + two(d.getUTCMinutes());
}

function every(minutes) {
  if (minutes % 1440 === 0) return minutes === 1440 ? "Every day" : "Every " + minutes / 1440 + " days";
  if (minutes % 60 === 0) return minutes === 60 ? "Every hour" : "Every " + minutes / 60 + " hours";
  return "Every " + minutes + " minutes";
}

// when is when s runs, in words: "Weekdays at 09:00, Europe/Berlin".
function when(s) {
  if (s.kind === "interval") return every(s.every);
  const d = s.weekdays.join();
  const days = d === "" || d === "1,2,3,4,5,6,7" ? "Every day" : d === "1,2,3,4,5" ? "Weekdays" : d === "6,7" ? "Weekends" : s.weekdays.map((n) => DAYS[n - 1]).join(", ");
  return days + " at " + s.at + ", " + s.zone;
}

const LAST = { starting: "starting", started: "started", skipped: "skipped", missed: "missed", failed: "failed" };

function last(s) {
  if (!s.lastState) return "No runs yet";
  return "Last run " + LAST[s.lastState] + (s.lastReason ? " (" + s.lastReason + ")" : "");
}

function projectName(projects, workspace) {
  const p = projects.find((x) => x.workspace === workspace);
  return p ? p.name : workspace.split(/[\\/]/).filter(Boolean).at(-1) ?? workspace;
}

// --- The page ---------------------------------------------------------------

const text = (t, more) => ({ type: "text", text: t, ...more });
const small = (t) => text(t, { fontSize: 12, style: "muted", singleLine: true });

// row is a schedule's, as Settings › Servers lays out a server's: its
// texts in a column that takes the room left, cut short to one line each
// where it is narrow, then its controls.
function row(state, s) {
  const next = !s.enabled ? "Off" : s.nextDue ? "Next run " + clock(Date.parse(s.nextDue), s.zone) : "Not due";
  const lines = [
    text(s.title, { weight: 500, singleLine: true }),
    small(s.project + " · " + when(s)),
    small(next + " · " + last(s)),
  ];
  if (state.notes[s.id]) lines.push(text(state.notes[s.id], { fontSize: 12, style: "error", selectable: true }));
  const controls = [];
  if (s.lastThread) {
    controls.push({ type: "button", id: "open:" + s.id, style: "ghost", icon: "square-share-line", label: "Open thread", tooltip: "Open thread",
      onClick: { handler: "open", payload: { thread: s.lastThread } } });
  }
  controls.push(
    { type: "button", id: "run:" + s.id, label: "Run now", progress: true, onClick: { handler: "run", payload: { id: s.id } } },
    { type: "button", id: "edit:" + s.id, label: "Edit", onClick: { handler: "edit", payload: { id: s.id } } },
    { type: "button", id: "delete:" + s.id, label: "Delete", style: "danger", onClick: { handler: "delete", payload: { id: s.id, title: s.title } } },
    { type: "toggle", id: "on:" + s.id, style: "switch", label: "Run " + s.title, value: s.enabled, controlled: true,
      onChange: { handler: "enable", payload: { id: s.id, revision: s.revision } } },
  );
  return { type: "row", padding: [12, 12, 12, 16], gap: 12, children: [{ type: "column", grow: true, gap: 2, children: lines }, ...controls] };
}

function select(form, k, label, options, value, width) {
  return { type: "select", id: form.key + ":" + k, label, options, value, width, ...(k === "kind" || k === "model" ? { onChange: k } : {}) };
}

// form is the schedule's, as Settings › Servers lays out Add a server:
// fields under their labels, then why a save failed, Cancel and Save.
function form(state) {
  const f = state.form;
  const id = (k) => f.key + ":" + k;
  const input = (k, label, value, more) => ({ type: "input", id: id(k), label, value, ...more });
  const projects = state.projects.map((p) => ({ value: p.workspace, label: p.name }));
  if (f.workspace && !projects.some((p) => p.value === f.workspace)) projects.push({ value: f.workspace, label: projectName(state.projects, f.workspace) });
  const models = state.models.map((m) => ({ value: modelValue(m.provider, m.id), label: m.name }));
  if (f.model && !models.some((m) => m.value === f.model)) models.push({ value: f.model, label: f.model.slice(f.model.indexOf(" ") + 1) });
  const model = state.models.find((m) => modelValue(m.provider, m.id) === f.model);
  const efforts = (model?.efforts ?? [f.effort]).filter(Boolean).map((e) => ({ value: e, label: e[0].toUpperCase() + e.slice(1) }));
  // A draft made before the options came, as with no engine yet, takes
  // the first.
  const pick = (options, value) => (options.some((o) => (o.value ?? o) === value) ? value : (options[0].value ?? options[0]));
  const fields = [
    input("title", "Title", f.title, { placeholder: "Morning triage" }),
    input("prompt", "Prompt", f.prompt, { multiline: true, placeholder: "What the agent does each time it runs" }),
  ];
  if (projects.length === 0) fields.push(small("No project is on this machine. Add one to the sidebar, then schedule it."));
  const picks = [];
  if (projects.length > 0) picks.push(select(f, "project", "Project", projects, pick(projects, f.workspace), 180));
  if (models.length > 0) picks.push(select(f, "model", "Model", models, pick(models, f.model), 180));
  if (efforts.length > 0) picks.push({ type: "select", id: effortID(state, f, f.model), label: "Effort", options: efforts, value: pick(efforts, f.effort), width: 120 });
  picks.push(select(f, "access", "Access", ACCESS, f.access, 180));
  fields.push({ type: "row", gap: 12, wrap: true, children: picks });
  fields.push(select(f, "kind", "Runs", [{ value: "weekly", label: "At a time, on chosen days" }, { value: "interval", label: "Every few minutes" }], f.kind, 240));
  if (f.kind === "interval") {
    fields.push(input("every", "Every (minutes, 5 at least)", f.every));
  } else {
    fields.push({ type: "row", gap: 12, children: [input("at", "At (HH:MM)", f.at), input("zone", "Time zone", f.zone)] });
    // A label over its checkboxes, as a Field draws its own.
    fields.push({ type: "column", gap: 6, children: [
      text("Days", { weight: 500 }),
      { type: "row", gap: 12, wrap: true, children: DAYS.map((d, i) => ({ type: "toggle", id: id("day" + (i + 1)), label: d, value: f.weekdays.includes(i + 1) })) },
      small("None checked runs it every day."),
    ] });
  }
  if (f.kind === "interval") fields.push(input("zone", "Time zone", f.zone));
  fields.push(select(f, "missed", "If a run is missed", [{ value: "once", label: "Run once when back" }, { value: "skip", label: "Skip" }], f.missed, 200));
  fields.push({ type: "row", gap: 16, children: [
    { type: "column", grow: true, gap: 2, children: [
      text("New worktree each run", { weight: 500 }),
      small("Off by default: each run makes a worktree, so a daily schedule makes 365 a year."),
    ] },
    { type: "toggle", id: id("worktree"), style: "switch", label: "New worktree each run", value: f.worktree },
  ] });
  fields.push({ type: "row", gap: 12, children: [
    { type: "column", grow: true, children: f.error ? [text(f.error, { fontSize: 12, style: "error", selectable: true })] : [] },
    { type: "button", id: id("cancel"), label: "Cancel", onClick: "cancel" },
    { type: "button", id: id("save"), label: f.editing ? "Save" : "Add schedule", style: "primary", progress: true, onClick: "save" },
  ] });
  return { type: "card", title: f.editing ? "Edit schedule" : "New schedule", gap: 14, padding: 16, children: fields };
}

export function view({ page, state }) {
  if (page !== "schedules") return { body: { type: "column", children: [] } };
  const n = state.count;
  const actions = [];
  if (state.error) actions.push({ type: "column", grow: true, children: [text(state.error, { fontSize: 12, style: "error", selectable: true })] });
  else actions.push({ type: "spacer" });
  if (n > SHOWN) {
    actions.push(
      small((state.offset + 1) + "–" + Math.min(n, state.offset + SHOWN) + " of " + n),
      { type: "button", id: "previous", label: "Previous", style: "bordered", disabled: state.offset === 0, onClick: { handler: "page", payload: { by: -SHOWN } } },
      { type: "button", id: "next", label: "Next", style: "bordered", disabled: state.offset + SHOWN >= n, onClick: { handler: "page", payload: { by: SHOWN } } },
    );
  }
  actions.push({ type: "button", id: "refresh", label: "Refresh", style: "bordered", onClick: "refresh" });
  if (!state.form) actions.push({ type: "button", id: "new", label: "New schedule", style: "primary", onClick: "new" });
  // Over the list, which may be long, as are its controls.
  const body = [{ type: "row", gap: 12, children: actions }];
  if (state.form) body.push(form(state));
  const list = n > 0
    ? state.rows.map((s) => row(state, s))
    : [{ type: "column", padding: 16, children: [text("No schedules yet.", { style: "muted" })] }];
  body.push({ type: "card", style: "list", title: "SCHEDULES", children: list });
  if (state.deleting) {
    body.push({ type: "dialog", id: "delete", title: "Delete " + state.deleting.title + "?", open: true, onClose: "keep", children: [
      text("It stops running. The threads its runs started stay.", { style: "muted" }),
      { type: "row", gap: 12, justify: "end", children: [
        { type: "button", id: "keep", label: "Keep it", onClick: "keep" },
        { type: "button", id: "confirmDelete", label: "Delete schedule", style: "danger", progress: true, onClick: "confirmDelete" },
      ] },
    ] });
  }
  return { subtitle: "Prompts that start an agent thread on a schedule, in a project of this machine.", body: { type: "column", gap: 20, children: body } };
}

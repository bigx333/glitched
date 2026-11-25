// Schedules: prompts that start an agent thread on a schedule. The service
// is the only writer of its glitched.db; each minute it starts what is due,
// once per occurrence, under a key the host keeps to one thread across
// restarts. It never wakes the Mac: a run missed while the engine was down
// runs once when it's back, or is skipped, as the schedule says.
//
// A run is written as an intent, with the create request it sends, before
// it is sent; a run left starting is sent again with that same request, so
// the engine answers the thread it may already have made. Nothing waits on
// a create but the run it is for: the page's writes go at once.
//
// Turning a schedule off stops each run it hadn't sent; one already sent
// may still start after, as the engine may have it.
//
// No two writes go together: each is ordered so that a cut after it leaves
// what the next tick, or a try of the same call again, finishes the same.

type ScheduleKind = "interval" | "weekly";
type MissedPolicy = "once" | "skip";
type RunState = "starting" | "started" | "skipped" | "missed" | "failed";

interface ScheduleRow {
  id: string;
  title: string;
  prompt: string;
  workspace: string;
  worktree: number;
  provider: string;
  model: string;
  effort: string;
  fast: number;
  access: AccessLevel;
  kind: ScheduleKind;
  every: number | null;
  at: string | null;
  weekdays: string | null;
  zone: string;
  anchor: string;
  missed: MissedPolicy;
  enabled: number;
  next_due: string | null;
  revision: number;
  served: number | null;
  last_thread?: string | null;
  last_state?: RunState | null;
  last_reason?: string | null;
  starting?: number;
}

interface ParsedSchedule {
  id: string;
  title: string;
  prompt: string;
  workspace: string;
  worktree: boolean;
  provider: string;
  model: string;
  effort: string;
  fast: boolean;
  access: AccessLevel;
  kind: ScheduleKind;
  every: number | null;
  at: string | null;
  weekdays: number[];
  zone: string;
  anchor: string;
  missed: MissedPolicy;
  enabled: boolean;
}

interface Schedule extends ParsedSchedule {
  nextDue: string | null;
  revision: number;
  lastThread: string | null;
  lastState: RunState | null;
  lastReason: string | null;
  starting: number;
}

interface RunRow {
  schedule: string;
  key: string;
  due: string;
  manual: number;
  state: RunState;
  request: string | null;
  sent: number;
  tried: number | null;
  thread: string | null;
  open: number;
  reason: string | null;
  at: string;
}

interface MetaRow {
  key: string;
  value: string;
}

interface ThreadStatusSnapshot {
  status: Map<string, ThreadRecord["status"]>;
  at: number;
}

function errMessage(e: unknown): string {
  return e instanceof Error ? e.message : String(e);
}

const ACCESS: AccessLevel[] = ["Ask for approval", "Approve for me", "Full access"];
const MINUTE = 60_000;
const DAY = 1440 * MINUTE;
// A run this late is missed, for a schedule that skips missed runs.
const GRACE = 10 * MINUTE;
// How long a tick starts runs for, so it ends inside its timer's minute;
// what is left goes on the next.
const BUDGET = 20_000;
// The most schedules, and runs left starting, a tick reads at once; it
// reads page after page while it has time.
const PAGE = 50;
// The most runs whose threads a tick checks at once.
const OPEN_PAGE = 500;
// The most schedules list answers at once.
const LIST_PAGE = 100;
// The finished runs a schedule keeps, newest first.
const KEEP = 50;
// How long a Run now's op is kept, to answer a try of it again with its
// run's thread: in its history, and once it leaves that, as a receipt in
// ops. An older op is refused.
const RECEIPTS = 7 * DAY;
// The most Run nows of one schedule left starting.
const MAX_PENDING = 20;
// The most schedules there can be.
const MAX_SCHEDULES = 1000;
// The longest prompt, in UTF-16 code units (a JavaScript string's
// length), and the longest reason a run records, in code points (as
// SQLite's length counts them). One measured example: 1000 schedules of
// the longest prompt, each with a run starting and its whole history,
// took 61 MiB of the 100 MB database; longer folders and model names take
// more.
const MAX_PROMPT = 4000;
const MAX_REASON = 100;
// SCHEMA is the database's shape. A database of another, made by a build
// of this plugin from before its release, is reset.
const SCHEMA = "2";

let ready: Promise<void> | null = null;

function db(): Promise<void> {
  ready ??= (async () => {
    await exec("CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)");
    const [schema] = await query<MetaRow>("SELECT value FROM meta WHERE key = 'schema'");
    if (schema?.value !== SCHEMA) {
      await exec("DROP TABLE IF EXISTS ops; DROP TABLE IF EXISTS runs; DROP TABLE IF EXISTS schedules; DELETE FROM meta;");
    }
    await exec(`
      CREATE TABLE IF NOT EXISTS schedules (
        id TEXT PRIMARY KEY,
        title TEXT NOT NULL,
        prompt TEXT NOT NULL,
        workspace TEXT NOT NULL,
        worktree INTEGER NOT NULL,
        provider TEXT NOT NULL, model TEXT NOT NULL, effort TEXT NOT NULL, fast INTEGER NOT NULL,
        access TEXT NOT NULL CHECK (access IN ('Ask for approval', 'Approve for me', 'Full access')),
        kind TEXT NOT NULL CHECK (kind IN ('interval', 'weekly')),
        every INTEGER,
        at TEXT,
        weekdays TEXT,
        zone TEXT NOT NULL,
        anchor TEXT NOT NULL,
        missed TEXT NOT NULL CHECK (missed IN ('once', 'skip')),
        enabled INTEGER NOT NULL,
        next_due TEXT,
        revision INTEGER NOT NULL DEFAULT 0,
        served INTEGER,
        CHECK ((kind = 'interval') = (every IS NOT NULL AND at IS NULL))
      );
      CREATE TABLE IF NOT EXISTS runs (
        schedule TEXT NOT NULL,
        key TEXT NOT NULL,
        due TEXT NOT NULL,
        manual INTEGER NOT NULL DEFAULT 0,
        state TEXT NOT NULL CHECK (state IN ('starting', 'started', 'skipped', 'missed', 'failed')),
        request TEXT,
        sent INTEGER NOT NULL DEFAULT 0,
        tried INTEGER,
        thread TEXT,
        open INTEGER NOT NULL DEFAULT 0,
        reason TEXT CHECK (length(reason) <= ${MAX_REASON}),
        at TEXT NOT NULL,
        PRIMARY KEY (schedule, key),
        CHECK ((state = 'starting') = (request IS NOT NULL)),
        CHECK (open = 0 OR thread IS NOT NULL)
      );
      CREATE TABLE IF NOT EXISTS ops (
        key TEXT PRIMARY KEY,
        schedule TEXT NOT NULL,
        due TEXT NOT NULL,
        thread TEXT
      ) WITHOUT ROWID;
      CREATE TRIGGER IF NOT EXISTS a_run_now_keeps_its_op BEFORE DELETE ON runs WHEN old.manual = 1 BEGIN
        INSERT OR IGNORE INTO ops (key, schedule, due, thread) VALUES (old.key, old.schedule, old.due, old.thread);
      END;
      CREATE INDEX IF NOT EXISTS history ON runs (schedule, due);
      CREATE INDEX IF NOT EXISTS starting ON runs (manual, tried) WHERE state = 'starting';
      CREATE INDEX IF NOT EXISTS open ON runs (schedule, key) WHERE open = 1;
      CREATE TRIGGER IF NOT EXISTS runs_go_with_their_schedule AFTER DELETE ON schedules BEGIN
        DELETE FROM runs WHERE schedule = old.id;
        DELETE FROM ops WHERE schedule = old.id;
      END;
      CREATE INDEX IF NOT EXISTS due ON schedules (enabled, next_due);
      INSERT OR IGNORE INTO meta (key, value) VALUES ('schema', '${SCHEMA}');
    `);
  })().catch((e) => {
    ready = null;
    throw e;
  });
  return ready;
}

const query = <Row = Record<string, unknown>>(sql: string, params?: unknown[]) => glitched.db.query<Row>(sql, params);
const exec = (sql: string, params?: unknown[]) => glitched.db.exec(sql, params);
// TRIED is the next try's place in the order runs left starting are tried
// in: after every one's last.
const TRIED = "(SELECT coalesce(max(tried), 0) + 1 FROM runs WHERE state = 'starting')";
// clip is a reason as a run records it.
const clip = (reason: string | null): string | null => (reason === null ? null : [...reason].slice(0, MAX_REASON).join(""));

// --- When a schedule runs ---------------------------------------------------

// offset is how far zone's wall clock is ahead of UTC at t, in ms.
function offset(t: number, zone: string): number {
  const f = new Intl.DateTimeFormat("en-US", {
    timeZone: zone, hourCycle: "h23",
    year: "numeric", month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", second: "2-digit",
  });
  const p = Object.fromEntries(f.formatToParts(new Date(t)).map((x) => [x.type, Number(x.value)]));
  return Date.UTC(p.year, p.month - 1, p.day, p.hour % 24, p.minute, p.second) - Math.floor(t / 1000) * 1000;
}

// civil is the wall-clock date of t in zone, as a UTC date at midnight.
function civil(t: number, zone: string): number {
  const d = new Date(t + offset(t, zone));
  return Date.UTC(d.getUTCFullYear(), d.getUTCMonth(), d.getUTCDate());
}

// wallTime is the instant zone's clocks show minutes after the midnight of
// day: the first of two, where clocks go back, and the time it would be
// had they not gone forward, where that time is skipped.
function wallTime(day: number, minutes: number, zone: string): number {
  const wall = day + minutes * MINUTE;
  const before = wall - offset(wall - DAY, zone);
  const after = wall - offset(wall + DAY, zone);
  const valid = [before, after].filter((t) => offset(t, zone) === wall - t);
  return valid.length > 0 ? Math.min(...valid) : before;
}

// isoWeekday is 1 for Monday to 7 for Sunday.
function isoWeekday(day: number): number {
  return new Date(day).getUTCDay() || 7;
}

function minutesOf(hhmm: string): number {
  const [h, m] = hhmm.split(":").map(Number);
  return h * 60 + m;
}

// occurrence is s's occurrence after t, or at or before it with back,
// never one before s's anchor; null where there's none.
function occurrence(s: ParsedSchedule, t: number, back: boolean): number | null {
  const anchor = Date.parse(s.anchor);
  if (s.kind === "interval") {
    const period = (s.every ?? 0) * MINUTE;
    if (back) {
      return t < anchor ? null : anchor + Math.floor((t - anchor) / period) * period;
    }
    return t < anchor ? anchor : anchor + (Math.floor((t - anchor) / period) + 1) * period;
  }
  const days = s.weekdays.length > 0 ? s.weekdays : [1, 2, 3, 4, 5, 6, 7];
  const minutes = minutesOf(s.at ?? "");
  const from = back ? t : Math.max(t, anchor - 1);
  const today = civil(from, s.zone);
  for (let i = back ? 0 : -1; i <= 8; i++) {
    const day = today + (back ? -i : i) * DAY;
    if (!days.includes(isoWeekday(day))) {
      continue;
    }
    const at = wallTime(day, minutes, s.zone);
    if (back ? at <= from : at > from) {
      return at < anchor ? null : at;
    }
  }
  return null;
}

const nextAfter = (s: ParsedSchedule, t: number): number | null => occurrence(s, t, false);
const latestBy = (s: ParsedSchedule, t: number): number | null => occurrence(s, t, true);
function iso(t: number): string;
function iso(t: number | null): string | null;
function iso(t: number | null): string | null {
  return t === null ? null : new Date(t).toISOString();
}

// --- Schedules as the page writes them -----------------------------------------

const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ";
function ulid(t: number): string {
  let out = "";
  for (let i = 9, n = t; i >= 0; i--, n = Math.floor(n / 32)) {
    out = crockford[n % 32] + out;
  }
  for (let i = 0; i < 16; i++) {
    out += crockford[Math.floor(Math.random() * 32)];
  }
  return out;
}

class Invalid extends Error {}

// time is the instant of an RFC 3339 time, or NaN for anything else, a day
// the calendar lacks included.
function time(v: unknown): number {
  const str = typeof v === "string" ? v : "";
  const m = /^(\d{4})-(\d{2})-(\d{2})T([01]\d|2[0-3]):[0-5]\d:[0-5]\d(\.\d+)?(Z|[+-]\d{2}:\d{2})$/.exec(str);
  if (!m || new Date(Date.UTC(Number(m[1]), Number(m[2]) - 1, Number(m[3]))).getUTCDate() !== Number(m[3])) return NaN;
  return Date.parse(str);
}

// absolute is whether Go's filepath.IsAbs holds for p on Unix or on
// Windows, a drive's, by its ASCII letter, or a share's: the engine that
// starts the run checks it again for its own.
function absolute(p: string): boolean {
  return /^(\/|[\\/]{2}|[A-Za-z]:[\\/])/.test(p);
}

function zoneOK(zone: string): boolean {
  try {
    new Intl.DateTimeFormat("en-US", { timeZone: zone });
    return true;
  } catch {
    return false;
  }
}

// parse checks a schedule the page sends and fills what it leaves out: a
// new one's id and anchor, now; the Mac's zone; and a missed run running
// once when the engine is back.
function parse(v: Record<string, unknown>, now: number): ParsedSchedule {
  const text = (k: string, max: number): string => {
    const val = v[k];
    if (typeof val !== "string" || val.trim() === "" || val.length > max) {
      throw new Invalid(k + " is text of 1 to " + max + " characters (UTF-16 code units)");
    }
    return val;
  };
  const idVal = v.id ?? ulid(now);
  if (typeof idVal !== "string" || !/^[0-9A-Z]{26}$/.test(idVal)) throw new Invalid("id is a schedule's");
  const workspace = text("workspace", 4096);
  if (!absolute(workspace)) throw new Invalid("workspace is an absolute folder");
  const effortVal = v.effort ?? "";
  if (typeof effortVal !== "string" || effortVal.length > 100) throw new Invalid("effort is text");
  const accessVal = v.access ?? "Ask for approval";
  if (typeof accessVal !== "string" || !ACCESS.includes(accessVal as AccessLevel)) throw new Invalid("access is one of " + ACCESS.join(", "));
  const kindVal = v.kind;
  if (kindVal !== "interval" && kindVal !== "weekly") throw new Invalid("kind is interval or weekly");
  let everyVal: number | null = null;
  let atVal: string | null = null;
  let weekdaysVal: number[] = [];
  if (kindVal === "interval") {
    if (typeof v.every !== "number" || !(Number.isInteger(v.every) && v.every >= 5 && v.every <= 7 * 1440)) {
      throw new Invalid("every is whole minutes, from 5 to a week");
    }
    everyVal = v.every;
  } else {
    if (typeof v.at !== "string" || !/^([01]\d|2[0-3]):[0-5]\d$/.test(v.at)) throw new Invalid("at is HH:MM");
    atVal = v.at;
    const rawDays = v.weekdays ?? [];
    if (!Array.isArray(rawDays) || !rawDays.every((d): d is number => typeof d === "number" && Number.isInteger(d) && d >= 1 && d <= 7)) {
      throw new Invalid("weekdays are 1 (Monday) to 7 (Sunday)");
    }
    weekdaysVal = [...new Set(rawDays)].sort();
  }
  const zoneVal = v.zone ?? Intl.DateTimeFormat().resolvedOptions().timeZone;
  if (typeof zoneVal !== "string" || !zoneOK(zoneVal)) throw new Invalid("zone is an IANA time zone");
  const rawAnchor = v.anchor ?? iso(now);
  const anchorMs = time(rawAnchor);
  if (Number.isNaN(anchorMs)) throw new Invalid("anchor is an RFC 3339 time");
  const missedVal = v.missed ?? "once";
  if (missedVal !== "once" && missedVal !== "skip") throw new Invalid("missed is once or skip");
  return {
    id: idVal,
    title: text("title", 200),
    prompt: text("prompt", MAX_PROMPT),
    workspace,
    worktree: v.worktree === true,
    provider: text("provider", 100),
    model: text("model", 200),
    effort: effortVal,
    fast: v.fast === true,
    access: accessVal as AccessLevel,
    kind: kindVal,
    every: everyVal,
    at: atVal,
    weekdays: weekdaysVal,
    zone: zoneVal,
    anchor: iso(anchorMs),
    missed: missedVal,
    enabled: v.enabled !== false,
  };
}

function fromRow(r: ScheduleRow): Schedule {
  return {
    id: r.id, title: r.title, prompt: r.prompt, workspace: r.workspace, worktree: r.worktree === 1,
    provider: r.provider, model: r.model, effort: r.effort, fast: r.fast === 1, access: r.access,
    kind: r.kind, every: r.every, at: r.at, weekdays: r.weekdays ? r.weekdays.split(",").map(Number) : [],
    zone: r.zone, anchor: r.anchor, missed: r.missed, enabled: r.enabled === 1,
    nextDue: r.next_due, revision: r.revision, lastThread: r.last_thread ?? null,
    lastState: r.last_state ?? null, lastReason: r.last_reason ?? null, starting: r.starting ?? 0,
  };
}

// SCHEDULE reads schedules with the thread of each one's newest run that
// has one, the newest run's state and why, and how many of its runs are sent and still starting: turning it off stops
// none of these.
const SCHEDULE = `SELECT s.*, (SELECT thread FROM runs WHERE schedule = s.id AND thread IS NOT NULL ORDER BY due DESC, key DESC LIMIT 1) AS last_thread,
    (SELECT state FROM runs WHERE schedule = s.id ORDER BY due DESC, key DESC LIMIT 1) AS last_state,
    (SELECT reason FROM runs WHERE schedule = s.id ORDER BY due DESC, key DESC LIMIT 1) AS last_reason,
    (SELECT count(*) FROM runs WHERE schedule = s.id AND state = 'starting' AND sent = 1) AS starting
  FROM schedules s`;

async function load(id: unknown): Promise<Schedule> {
  const rows = await query<ScheduleRow>(SCHEDULE + " WHERE s.id = ?", [id]);
  if (rows.length === 0) throw new Invalid("no schedule " + String(id));
  return fromRow(rows[0]);
}

// save writes a schedule: a new one under the id it comes with, if any, so
// a try of it again saves the same one; for one saved, the fields it names
// over those it has, so a switch that sends enabled alone changes nothing
// else. It saves over the revision it read, and is refused if another save
// came between; with a revision, over that one, so a page's edit neither
// brings back a schedule deleted since it read it nor undoes a save since.
// One whose times and enabled stay as they were keeps its next_due, so an
// edit drops no run the timer hasn't seen; one turned off drops, after, the
// runs it meant to start and hadn't sent yet, which no tick sends once it's
// off. Each save is a new revision, which a
// run not yet written is made from.
async function save(v: Record<string, unknown>): Promise<Schedule> {
  const now = Date.now();
  const [row] = v.id === undefined ? [] : await query<ScheduleRow>(SCHEDULE + " WHERE s.id = ?", [v.id]);
  const was = row === undefined ? null : fromRow(row);
  if (v.revision !== undefined && was?.revision !== v.revision) {
    throw new Invalid(was === null ? "this schedule was deleted" : "this schedule was saved since it was read: read it again");
  }
  const s = parse(was === null ? v : { ...was, ...v }, now);
  const same = was !== null && (["kind", "every", "at", "zone", "anchor", "enabled"] as const).every((k) => was[k] === s[k]) &&
    was.weekdays.join() === s.weekdays.join();
  const due = !s.enabled ? null : same ? was.nextDue : iso(nextAfter(s, now));
  const fields = [s.title, s.prompt, s.workspace, s.worktree ? 1 : 0, s.provider, s.model, s.effort, s.fast ? 1 : 0, s.access,
    s.kind, s.every, s.at, s.kind === "weekly" ? s.weekdays.join(",") : null, s.zone, s.anchor, s.missed, s.enabled ? 1 : 0, due];
  if (was === null) {
    const { changes } = await exec(
      `INSERT INTO schedules (id, title, prompt, workspace, worktree, provider, model, effort, fast, access, kind, every, at, weekdays, zone, anchor, missed, enabled, next_due)
       SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ? WHERE (SELECT count(*) FROM schedules) < ${MAX_SCHEDULES}`,
      [s.id, ...fields],
    );
    if (changes === 0) throw new Invalid("there are " + MAX_SCHEDULES + " schedules already, the most there can be");
    return load(s.id);
  }
  const { changes } = await exec(
    `UPDATE schedules SET title = ?, prompt = ?, workspace = ?, worktree = ?, provider = ?, model = ?, effort = ?, fast = ?, access = ?,
       kind = ?, every = ?, at = ?, weekdays = ?, zone = ?, anchor = ?, missed = ?, enabled = ?, next_due = ?, revision = revision + 1
     WHERE id = ? AND revision = ?`,
    [...fields, s.id, was.revision],
  );
  if (changes === 0) throw new Invalid("this schedule was saved or deleted as this save read it: read it again");
  if (!s.enabled) {
    await exec(
      "UPDATE runs SET state = 'skipped', reason = 'turned off', request = NULL, at = ? WHERE schedule = ? AND manual = 0 AND state = 'starting' AND sent = 0",
      [iso(now), s.id],
    );
  }
  return load(s.id);
}

// --- Starting runs -----------------------------------------------------------------

// request is the create a run of s sends, under key, at due: written with
// the run's intent, and sent the same on every try.
function request(s: Schedule, key: string, due: string): string {
  return JSON.stringify({
    prompt: s.prompt,
    options: {
      title: s.title,
      workspace: s.workspace,
      model: { provider: s.provider, model: s.model, effort: s.effort, fast: s.fast },
      access: s.access,
      ...(s.worktree ? { worktree: {} } : {}),
      key: { id: key, at: due },
    },
  });
}

// prune drops schedule's finished runs past its newest KEEP, but for one
// whose thread may still go.
async function prune(schedule: string): Promise<void> {
  await exec(
    `DELETE FROM runs WHERE schedule = ? AND state <> 'starting' AND open = 0
       AND key NOT IN (SELECT key FROM runs WHERE schedule = ? ORDER BY due DESC, key DESC LIMIT ${KEEP})`,
    [schedule, schedule],
  );
}

// settle ends run r as state, a thread with it counting as one that may
// still go.
async function settle(r: RunRow, state: RunState, { thread = null, reason = null }: { thread?: string | null; reason?: string | null } = {}): Promise<void> {
  await exec(
    "UPDATE runs SET state = ?, thread = ?, open = ?, reason = ?, request = NULL, at = ? WHERE schedule = ? AND key = ?",
    [state, thread, thread === null ? 0 : 1, clip(reason), iso(Date.now()), r.schedule, r.key],
  );
  await prune(r.schedule);
}

// inflight are the creates this runtime is sending, by run: a second send
// of one answers the first's thread.
const inflight = new Map<string, Promise<string | null>>();
const runID = (r: { schedule: string; key: string }): string => r.schedule + "\n" + r.key;

// send sends run r's create and settles it on the answer; it answers the
// thread, or null. A run not sent yet is sent only if it is still
// starting and its schedule is on. A write that fails after the create
// leaves the run starting, to be sent again.
function send(r: RunRow): Promise<string | null> {
  const id = runID(r);
  const existing = inflight.get(id);
  if (existing) return existing;
  const p = sending(r).finally(() => inflight.delete(id));
  inflight.set(id, p);
  return p;
}

async function sending(r: RunRow): Promise<string | null> {
  if (r.sent === 0) {
    const { changes } = await exec(
      `UPDATE runs SET sent = 1 WHERE schedule = ? AND key = ? AND state = 'starting' AND sent = 0
         AND EXISTS (SELECT 1 FROM schedules WHERE id = runs.schedule AND enabled = 1)`,
      [r.schedule, r.key],
    );
    if (changes === 0) return null;
  }
  const { prompt, options } = JSON.parse(r.request ?? "{}") as { prompt: string; options: Parameters<typeof glitched.threads.create>[1] };
  let thread: string;
  try {
    ({ thread } = await glitched.threads.create(prompt, options));
  } catch (e) {
    await settle(r, "failed", { reason: errMessage(e) });
    return null;
  }
  await settle(r, "started", { thread });
  return thread;
}

// retry sends again run r, left starting, unless this runtime is sending
// it already, and records the try, so a tick tries the runs it tried
// longest ago first, across restarts and clocks that go back too.
async function retry(r: RunRow): Promise<void> {
  await exec(`UPDATE runs SET tried = ${TRIED} WHERE schedule = ? AND key = ?`, [r.schedule, r.key]);
  if (!inflight.has(runID(r))) await send(r);
}

// threadsOf reads the engine's threads once for a tick, when the tick
// first needs them, and when it asked for them.
function threadsOf(): () => Promise<ThreadStatusSnapshot> {
  let read: Promise<ThreadStatusSnapshot> | null = null;
  return () => {
    if (read === null) {
      const at = Date.now();
      read = glitched.threads.list().then((list) => ({ status: new Map(list.map((t) => [t.id, t.status])), at }));
    }
    return read;
  };
}

// check checks the threads of a page of runs that may still go, schedule's
// or every schedule's, after the run named: one that has stopped, or is
// gone, stops counting, and its schedule keeps its newest history again.
// It answers whether one still works or waits on you, and the page's last
// run, or null at the end; a run settled since the threads were asked for
// counts as one that does.
async function check(threads: () => Promise<ThreadStatusSnapshot>, schedule: string | null, after: [string, string]): Promise<{ busy: boolean; last: [string, string] | null }> {
  const rows = await query<{ schedule: string; key: string; thread: string; at: string }>(
    `SELECT schedule, key, thread, at FROM runs WHERE open = 1 AND (? IS NULL OR schedule = ?) AND (schedule, key) > (?, ?)
     ORDER BY schedule, key LIMIT ?`,
    [schedule, schedule, ...after, OPEN_PAGE],
  );
  const { status, at } = rows.length > 0 ? await threads() : { status: new Map<string, ThreadRecord["status"]>(), at: 0 };
  const stopped = new Map<string, string[]>();
  let busy = false;
  for (const r of rows) {
    const s = status.get(r.thread);
    if (Date.parse(r.at) >= at || s === "working" || s === "waiting") {
      busy = true;
    } else {
      stopped.set(r.schedule, [...(stopped.get(r.schedule) ?? []), r.key]);
    }
  }
  for (const [id, keys] of stopped) {
    await exec("UPDATE runs SET open = 0 WHERE schedule = ? AND key IN (SELECT value FROM json_each(?))", [id, JSON.stringify(keys)]);
    await prune(id);
  }
  const lastRow = rows.at(-1);
  return { busy, last: rows.length < OPEN_PAGE || !lastRow ? null : [lastRow.schedule, lastRow.key] };
}

// going is whether a run of s may still go: one starting, or one whose
// thread works or waits on you, all of whose pages it checks.
async function going(s: Schedule, threads: () => Promise<ThreadStatusSnapshot>): Promise<boolean> {
  const [starting] = await query("SELECT 1 FROM runs WHERE schedule = ? AND state = 'starting' LIMIT 1", [s.id]);
  if (starting !== undefined) return true;
  let busy = false;
  for (let after: [string, string] | null = ["", ""]; after !== null; ) {
    const page = await check(threads, s.id, after);
    busy ||= page.busy;
    after = page.last;
  }
  return busy;
}

// cleanup checks one page of every schedule's runs that may still go, from
// where the last tick's left off, and drops the ops past their time.
async function cleanup(threads: () => Promise<ThreadStatusSnapshot>): Promise<void> {
  const [from] = await query<MetaRow>("SELECT value FROM meta WHERE key = 'checked'");
  const { last } = await check(threads, null, from === undefined ? ["", ""] : (JSON.parse(from.value) as [string, string]));
  await exec("INSERT INTO meta (key, value) VALUES ('checked', ?) ON CONFLICT (key) DO UPDATE SET value = excluded.value", [
    JSON.stringify(last ?? ["", ""]),
  ]);
  await exec(`DELETE FROM ops WHERE key IN (SELECT key FROM ops WHERE due < ? LIMIT ${OPEN_PAGE})`, [iso(Date.now() - RECEIPTS - MINUTE)]);
}

// unit does what schedule id's automatic runs need, from what it is now:
// occur, read again for a schedule saved meanwhile.
async function unit(id: string, threads: () => Promise<ThreadStatusSnapshot>): Promise<void> {
  for (let i = 0; i < 3; i++) {
    const [row] = await query<ScheduleRow>("SELECT * FROM schedules WHERE id = ?", [id]);
    if (row === undefined || (await occur(fromRow(row), threads))) return;
  }
}

// occur sends again a run of s left starting, as its catch-up, once
// next_due has moved past now. Else, on and due, it records the run of its
// latest occurrence, unless that is before its next_due: missed where its
// next_due shows it was, skipped where a run of it may still go, and
// starting otherwise; then next_due moves past now, and a run starting is
// sent. A run starts only if no run of s started, or settled, since occur
// began looking: one that did may still go, though the threads it read
// were asked for before it. Every step reads what is there and does what
// is left, so a crash anywhere comes back to the same end: a run recorded
// at or past next_due, by a tick cut before next_due moved, moves next_due
// as that tick would have, and one left starting is sent again under its
// key. It answers false where s is to be read again: saved since it was
// read, its next_due moved for such a run, or a run of it begun meanwhile.
async function occur(s: Schedule, threads: () => Promise<ThreadStatusSnapshot>): Promise<boolean> {
  const now = Date.now();
  const advance = () => exec(
    "UPDATE schedules SET next_due = ?, served = (SELECT coalesce(max(served), 0) + 1 FROM schedules) WHERE id = ? AND enabled = 1 AND next_due = ?",
    [iso(nextAfter(s, now)), s.id, s.nextDue],
  );
  const starting = await query<RunRow>("SELECT * FROM runs WHERE schedule = ? AND manual = 0 AND state = 'starting' ORDER BY due", [s.id]);
  if (starting.length > 0) await advance();
  for (const r of starting) {
    if (r.sent === 0 && !s.enabled) {
      await settle(r, "skipped", { reason: "turned off" });
    } else {
      await retry(r);
    }
  }
  if (starting.length > 0 || !s.enabled || s.nextDue === null) return true;
  const [recorded] = await query<{ due: string | null }>("SELECT max(due) AS due FROM runs WHERE schedule = ? AND manual = 0 AND due >= ?", [s.id, s.nextDue]);
  if (recorded.due !== null) {
    await exec("UPDATE schedules SET next_due = ? WHERE id = ? AND enabled = 1 AND next_due = ?", [iso(nextAfter(s, Date.parse(recorded.due))), s.id, s.nextDue]);
    return false;
  }
  const nextDue = Date.parse(s.nextDue);
  const at = latestBy(s, now);
  if (at === null || at < nextDue) return true;
  const occ = iso(at);
  const key = s.id + "@" + occ;
  let state: RunState = "starting";
  let reason: string | null = null;
  if (s.missed === "skip" && now - nextDue > GRACE) {
    [state, reason] = ["missed", "Glitched wasn't running then"];
  } else {
    const busy = await going(s, threads).catch((e: unknown) => (e instanceof Error ? e : new Error(String(e))));
    if (busy instanceof Error) {
      [state, reason] = ["failed", "its runs couldn't be checked: " + busy.message];
    } else if (busy) {
      [state, reason] = ["skipped", "the previous run is still going"];
    }
  }
  const req = state === "starting" ? request(s, key, occ) : null;
  await exec(
    `INSERT INTO runs (schedule, key, due, state, request, reason, tried, at)
     SELECT ?, ?, ?, ?, ?, ?, CASE WHEN ? IS NULL THEN NULL ELSE ${TRIED} END, ?
     WHERE EXISTS (SELECT 1 FROM schedules WHERE id = ? AND enabled = 1 AND next_due = ? AND revision = ?)
       AND (? IS NULL OR NOT EXISTS (SELECT 1 FROM runs WHERE schedule = ? AND (state = 'starting' OR at >= ?)))
     ON CONFLICT (schedule, key) DO NOTHING`,
    [s.id, key, occ, state, req, clip(reason), req, iso(now), s.id, s.nextDue, s.revision, req, s.id, iso(now)],
  );
  const [r] = await query<RunRow>("SELECT * FROM runs WHERE schedule = ? AND key = ?", [s.id, key]);
  if (r === undefined) return false;
  await advance();
  if (r.state === "starting") {
    await send(r);
  } else {
    await prune(s.id);
  }
  return true;
}

// pages are one kind of a tick's work, read PAGE at a time as it goes:
// next answers the next row the tick hasn't taken, by key, or null at the
// end.
function pages<T>(read: () => Promise<T[]>): (key: (r: T) => string) => Promise<T | null> {
  const taken = new Set<string>();
  let rows: T[] = [];
  let end = false;
  return async (key) => {
    while (rows.length === 0 && !end) {
      const page = await read();
      rows = page.filter((r) => !taken.has(key(r)));
      end = page.length < PAGE || rows.length === 0;
    }
    const r = rows.shift() ?? null;
    if (r !== null) taken.add(key(r));
    return r;
  };
}

interface KindRunner<T> {
  next: (key: (r: T) => string) => Promise<T | null>;
  key: (r: T) => string;
  does: (r: T) => Promise<void>;
}

// tick takes turns: a Run now left starting, then a schedule's unit, and
// so on, till both are done or its budget is spent. Which kind goes first
// alternates from tick to tick. Each kind has its first turn whatever the
// budget, and then a page of runs that may still go is checked. Run nows
// go in the order they were last tried; schedules that are due, the one
// due longest ago first, then those with a run left starting, in the
// order they were last tried. Of those due at once, the one whose run was
// recorded longest ago goes first. One that fails holds up none of the rest;
// the host logs what failed once the tick ends.
async function tick(): Promise<void> {
  await db();
  const begun = Date.now();
  const failed: string[] = [];
  const threads = threadsOf();
  const [turn] = await query<MetaRow>("SELECT value FROM meta WHERE key = 'turn'");
  const manualFirst = turn?.value !== "automatic";
  await exec("INSERT INTO meta (key, value) VALUES ('turn', ?) ON CONFLICT (key) DO UPDATE SET value = excluded.value", [manualFirst ? "automatic" : "manual"]);
  const manual: KindRunner<RunRow> = {
    next: pages(() => query<RunRow>(
      "SELECT * FROM runs WHERE manual = 1 AND state = 'starting' ORDER BY tried, schedule, key LIMIT ?",
      [PAGE],
    )),
    key: runID,
    does: (r) => retry(r),
  };
  const automatic: KindRunner<{ id: string }> = {
    next: pages(() => query<{ id: string }>(
      `SELECT s.id FROM schedules s
       LEFT JOIN (SELECT schedule, max(tried) AS tried FROM runs WHERE manual = 0 AND state = 'starting' GROUP BY schedule) w ON w.schedule = s.id
       WHERE (s.enabled = 1 AND s.next_due <= ?) OR w.schedule IS NOT NULL
       ORDER BY w.tried IS NOT NULL, w.tried, s.next_due, s.served IS NOT NULL, s.served, s.id LIMIT ?`,
      [iso(begun), PAGE],
    )),
    key: (s) => s.id,
    does: (s) => unit(s.id, threads),
  };
  const step = async <T>(k: KindRunner<T>): Promise<boolean> => {
    const r = await k.next(k.key).catch((e: unknown) => {
      failed.push(errMessage(e));
      return null;
    });
    if (r === null) return false;
    await k.does(r).catch((e: unknown) => failed.push(errMessage(e)));
    return true;
  };
  let runManual = true;
  let runAuto = true;
  const runNext = async (isManual: boolean): Promise<void> => {
    if (isManual) {
      if (runManual && !(await step(manual))) runManual = false;
    } else if (runAuto && !(await step(automatic))) {
      runAuto = false;
    }
  };
  const order = manualFirst ? [true, false] : [false, true];
  const round = async (budgeted: boolean): Promise<void> => {
    for (const isManual of order) {
      if (budgeted && Date.now() - begun > BUDGET) {
        runManual = false;
        runAuto = false;
        return;
      }
      await runNext(isManual);
    }
  };
  await round(false);
  await cleanup(threads).catch((e: unknown) => failed.push(errMessage(e)));
  while (runManual || runAuto) await round(true);
  if (failed.length > 0) throw new Error(failed.join("; "));
}

// --- What the engine calls -----------------------------------------------------------

export function timer(): Promise<void> {
  return tick();
}

// OP names one Run now: a ULID the page makes for each click, and sends
// the same on each try of it. Its time is the run's occurrence, so each
// try of it is the same create.
const OP = /^[0-9A-HJKMNP-TV-Z]{26}$/;

const methods: Record<string, (args: Record<string, unknown>) => unknown> = {
  // list is a page of the schedules, by id, after the one named: an id
  // never changes, so a schedule edited while pages are read is on one.
  async list({ after }: { after?: unknown } = {}) {
    const rows = await query<ScheduleRow>(SCHEDULE + " WHERE s.id > ? ORDER BY s.id LIMIT ?", [after ?? "", LIST_PAGE + 1]);
    const schedules = rows.slice(0, LIST_PAGE).map(fromRow);
    const lastSched = schedules.at(-1);
    return { schedules, next: rows.length > LIST_PAGE && lastSched ? lastSched.id : null };
  },
  save: ({ schedule }) => save(typeof schedule === "object" && schedule !== null ? (schedule as Record<string, unknown>) : {}),
  get: ({ id }) => load(id),
  // delete deletes a schedule, or answers as it did for one already gone.
  async delete({ id }) {
    await exec("DELETE FROM schedules WHERE id = ?", [id]);
    return null;
  },
  async runs({ id, limit }) {
    const n = typeof limit === "number" && Number.isInteger(limit) && limit > 0 ? Math.min(limit, KEEP) : 50;
    return query("SELECT schedule, due, key, thread, state, reason, at FROM runs WHERE schedule = ? ORDER BY due DESC, key DESC LIMIT ?", [id, n]);
  },
  // runNow starts s now, as the person asked for it: past the check that
  // a run of it may still go. A try of an op already written answers its
  // run's thread, and starts no other, for 7 days; an op older is refused.
  async runNow({ id, op }) {
    if (typeof op !== "string" || !OP.test(op)) throw new Invalid("op is a ULID, the same on each try of one Run now");
    const at = [...op.slice(0, 10)].reduce((n, c) => n * 32 + crockford.indexOf(c), 0);
    const now = Date.now();
    if (now - at > RECEIPTS) throw new Invalid("this Run now is more than 7 days old, so it won't run again");
    if (at - now > MINUTE) throw new Invalid("this Run now's op is from a time to come");
    const s = await load(id);
    const key = "manual:" + s.id + ":" + op;
    await exec(
      `INSERT INTO runs (schedule, key, due, manual, state, request, sent, tried, at)
       SELECT ?, ?, ?, 1, 'starting', ?, 1, ${TRIED}, ? WHERE EXISTS (SELECT 1 FROM schedules WHERE id = ?)
         AND NOT EXISTS (SELECT 1 FROM ops WHERE key = ?)
         AND (SELECT count(*) FROM runs WHERE schedule = ? AND manual = 1 AND state = 'starting') < ${MAX_PENDING}
       ON CONFLICT (schedule, key) DO NOTHING`,
      [s.id, key, iso(at), request(s, key, iso(at)), iso(now), s.id, key, s.id],
    );
    // It answers the run's thread, and the schedule as the run left it.
    const answer = async (thread: string | null) => ({ thread, schedule: await load(s.id).catch(() => null) });
    const [r] = await query<RunRow>("SELECT * FROM runs WHERE schedule = ? AND key = ?", [s.id, key]);
    if (r !== undefined) return answer(r.state === "starting" ? await send(r) : r.thread);
    const [receipt] = await query<{ thread: string | null }>("SELECT thread FROM ops WHERE key = ?", [key]);
    if (receipt !== undefined) return answer(receipt.thread);
    await load(id);
    throw new Invalid("this schedule has " + MAX_PENDING + " Run nows starting already");
  },
  // preview is when a schedule, saved or not, runs next: count occurrences
  // after from, which is now without one.
  preview({ schedule, from, count }) {
    const rawSched = typeof schedule === "object" && schedule !== null ? (schedule as Record<string, unknown>) : {};
    const s = parse({ ...rawSched, id: rawSched.id ?? ulid(0) }, Date.now());
    let t = from === undefined ? Date.now() : time(from);
    if (Number.isNaN(t)) throw new Invalid("from is an RFC 3339 time");
    const n = typeof count === "number" ? count : 5;
    const out: string[] = [];
    for (let i = 0; i < Math.min(n, 100); i++) {
      const next = nextAfter(s, t);
      if (next === null) break;
      out.push(iso(next));
      t = next;
    }
    return out;
  },
};

// serve answers the page, each call as it comes. A call that timed out
// may still have done what it asked: each is safe to try again, and a
// list or runs shows what it did.
export function serve({ method, args }: ServeArgs<Record<string, unknown>>): unknown {
  const m = Object.prototype.hasOwnProperty.call(methods, method) ? methods[method] : undefined;
  if (!m) throw new Error("no method " + method);
  if (method === "preview") return m(args ?? {});
  return db().then(() => m(args ?? {}));
}

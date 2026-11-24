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

const ACCESS = ["Ask for approval", "Approve for me", "Full access"];
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
// The most schedules there can be.
const MAX_SCHEDULES = 1000;
// The longest prompt, and reason a run records, in characters: 1000
// schedules of the longest, each with a run starting and its whole
// history, leave a third of the 100 MB database, for the copies of the
// prompts that Run nows write as they start.
const MAX_PROMPT = 4000;
const MAX_REASON = 100;

let ready = null;

function db() {
  ready ??= (async () => {
    await glitched.db.exec(`
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
        tried TEXT,
        thread TEXT,
        open INTEGER NOT NULL DEFAULT 0,
        reason TEXT,
        at TEXT NOT NULL,
        PRIMARY KEY (schedule, key),
        CHECK ((state = 'starting') = (request IS NOT NULL)),
        CHECK (open = 0 OR thread IS NOT NULL)
      );
      CREATE INDEX IF NOT EXISTS history ON runs (schedule, due);
      CREATE INDEX IF NOT EXISTS starting ON runs (manual, tried) WHERE state = 'starting';
      CREATE INDEX IF NOT EXISTS open ON runs (schedule, key) WHERE open = 1;
      CREATE TRIGGER IF NOT EXISTS runs_go_with_their_schedule AFTER DELETE ON schedules BEGIN
        DELETE FROM runs WHERE schedule = old.id;
      END;
      CREATE INDEX IF NOT EXISTS due ON schedules (enabled, next_due);
      CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
      INSERT OR IGNORE INTO meta (key, value) VALUES ('schema', '1');
    `);
  })().catch((e) => {
    ready = null;
    throw e;
  });
  return ready;
}

const query = (sql, params) => glitched.db.query(sql, params);
const exec = (sql, params) => glitched.db.exec(sql, params);
const transaction = (statements) => glitched.db.transaction(statements);

// --- When a schedule runs ---------------------------------------------------

// offset is how far zone's wall clock is ahead of UTC at t, in ms.
function offset(t, zone) {
  const f = new Intl.DateTimeFormat("en-US", {
    timeZone: zone, hourCycle: "h23",
    year: "numeric", month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", second: "2-digit",
  });
  const p = Object.fromEntries(f.formatToParts(new Date(t)).map((x) => [x.type, Number(x.value)]));
  return Date.UTC(p.year, p.month - 1, p.day, p.hour % 24, p.minute, p.second) - Math.floor(t / 1000) * 1000;
}

// civil is the wall-clock date of t in zone, as a UTC date at midnight.
function civil(t, zone) {
  const d = new Date(t + offset(t, zone));
  return Date.UTC(d.getUTCFullYear(), d.getUTCMonth(), d.getUTCDate());
}

// wallTime is the instant zone's clocks show minutes after the midnight of
// day: the first of two, where clocks go back, and the time it would be
// had they not gone forward, where that time is skipped.
function wallTime(day, minutes, zone) {
  const wall = day + minutes * MINUTE;
  const before = wall - offset(wall - DAY, zone);
  const after = wall - offset(wall + DAY, zone);
  const valid = [before, after].filter((t) => offset(t, zone) === wall - t);
  return valid.length > 0 ? Math.min(...valid) : before;
}

// isoWeekday is 1 for Monday to 7 for Sunday.
function isoWeekday(day) {
  return new Date(day).getUTCDay() || 7;
}

function minutesOf(hhmm) {
  const [h, m] = hhmm.split(":").map(Number);
  return h * 60 + m;
}

// occurrence is s's occurrence after t, or at or before it with back,
// never one before s's anchor; null where there's none.
function occurrence(s, t, back) {
  const anchor = Date.parse(s.anchor);
  if (s.kind === "interval") {
    const period = s.every * MINUTE;
    if (back) {
      return t < anchor ? null : anchor + Math.floor((t - anchor) / period) * period;
    }
    return t < anchor ? anchor : anchor + (Math.floor((t - anchor) / period) + 1) * period;
  }
  const days = s.weekdays.length > 0 ? s.weekdays : [1, 2, 3, 4, 5, 6, 7];
  const minutes = minutesOf(s.at);
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

const nextAfter = (s, t) => occurrence(s, t, false);
const latestBy = (s, t) => occurrence(s, t, true);
const iso = (t) => (t === null ? null : new Date(t).toISOString());

// --- Schedules as the page writes them -----------------------------------------

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

class Invalid extends Error {}

// time is the instant of an RFC 3339 time, or NaN for anything else, a day
// the calendar lacks included.
function time(v) {
  const m = /^(\d{4})-(\d{2})-(\d{2})T([01]\d|2[0-3]):[0-5]\d:[0-5]\d(\.\d+)?(Z|[+-]\d{2}:\d{2})$/.exec(v ?? "");
  if (!m || new Date(Date.UTC(m[1], m[2] - 1, m[3])).getUTCDate() !== Number(m[3])) return NaN;
  return Date.parse(v);
}

// absolute is whether Go's filepath.IsAbs holds for p on Unix or on
// Windows, a drive's, by its ASCII letter, or a share's: the engine that
// starts the run checks it again for its own.
function absolute(p) {
  return /^(\/|[\\/]{2}|[A-Za-z]:[\\/])/.test(p);
}

function zoneOK(zone) {
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
function parse(v, now) {
  const text = (k, max) => {
    if (typeof v[k] !== "string" || v[k].trim() === "" || v[k].length > max) {
      throw new Invalid(k + " is text of 1 to " + max + " characters");
    }
    return v[k];
  };
  const s = {
    id: v.id ?? ulid(now),
    title: text("title", 200),
    prompt: text("prompt", MAX_PROMPT),
    workspace: text("workspace", 4096),
    worktree: v.worktree === true,
    provider: text("provider", 100),
    model: text("model", 200),
    effort: v.effort ?? "",
    fast: v.fast === true,
    access: v.access ?? "Ask for approval",
    kind: v.kind,
    every: v.kind === "interval" ? v.every : null,
    at: v.kind === "weekly" ? v.at : null,
    weekdays: v.kind === "weekly" ? (v.weekdays ?? []) : [],
    zone: v.zone ?? Intl.DateTimeFormat().resolvedOptions().timeZone,
    anchor: v.anchor ?? iso(now),
    missed: v.missed ?? "once",
    enabled: v.enabled !== false,
  };
  if (typeof s.id !== "string" || !/^[0-9A-Z]{26}$/.test(s.id)) throw new Invalid("id is a schedule's");
  if (!absolute(s.workspace)) throw new Invalid("workspace is an absolute folder");
  if (typeof s.effort !== "string" || s.effort.length > 100) throw new Invalid("effort is text");
  if (!ACCESS.includes(s.access)) throw new Invalid("access is one of " + ACCESS.join(", "));
  if (s.kind !== "interval" && s.kind !== "weekly") throw new Invalid("kind is interval or weekly");
  if (s.kind === "interval" && !(Number.isInteger(s.every) && s.every >= 5 && s.every <= 7 * 1440)) {
    throw new Invalid("every is whole minutes, from 5 to a week");
  }
  if (s.kind === "weekly") {
    if (typeof s.at !== "string" || !/^([01]\d|2[0-3]):[0-5]\d$/.test(s.at)) throw new Invalid("at is HH:MM");
    if (!Array.isArray(s.weekdays) || !s.weekdays.every((d) => Number.isInteger(d) && d >= 1 && d <= 7)) {
      throw new Invalid("weekdays are 1 (Monday) to 7 (Sunday)");
    }
    s.weekdays = [...new Set(s.weekdays)].sort();
  }
  if (typeof s.zone !== "string" || !zoneOK(s.zone)) throw new Invalid("zone is an IANA time zone");
  if (Number.isNaN(time(s.anchor))) throw new Invalid("anchor is an RFC 3339 time");
  s.anchor = iso(time(s.anchor));
  if (s.missed !== "once" && s.missed !== "skip") throw new Invalid("missed is once or skip");
  return s;
}

function fromRow(r) {
  return {
    id: r.id, title: r.title, prompt: r.prompt, workspace: r.workspace, worktree: r.worktree === 1,
    provider: r.provider, model: r.model, effort: r.effort, fast: r.fast === 1, access: r.access,
    kind: r.kind, every: r.every, at: r.at, weekdays: r.weekdays ? r.weekdays.split(",").map(Number) : [],
    zone: r.zone, anchor: r.anchor, missed: r.missed, enabled: r.enabled === 1,
    nextDue: r.next_due, revision: r.revision, lastThread: r.last_thread ?? null, starting: r.starting ?? 0,
  };
}

// SCHEDULE reads schedules with the thread of each one's newest run, and
// how many of its runs are sent and still starting: turning it off stops
// none of these.
const SCHEDULE = `SELECT s.*, (SELECT thread FROM runs WHERE schedule = s.id AND thread IS NOT NULL ORDER BY due DESC, key DESC LIMIT 1) AS last_thread,
    (SELECT count(*) FROM runs WHERE schedule = s.id AND state = 'starting' AND sent = 1) AS starting
  FROM schedules s`;

async function load(id) {
  const rows = await query(SCHEDULE + " WHERE s.id = ?", [id]);
  if (rows.length === 0) throw new Invalid("no schedule " + id);
  return fromRow(rows[0]);
}

// late refuses a call that has run out of time: as serve takes it, and
// before each write, so a call that timed out never writes after.
function late(deadline) {
  if (deadline !== undefined && Date.now() > deadline) throw new Error("the call ran out of time before it wrote");
}

// save writes a schedule. One whose times and enabled stay as they were
// keeps its next_due, so an edit drops no run the timer hasn't seen; one
// turned off drops, with it, the runs it meant to start and hadn't sent
// yet. Each save is a new revision, which a run not yet written is made
// from.
async function save(v, deadline) {
  const now = Date.now();
  const was = v.id === undefined ? null : await load(v.id);
  const s = parse(was === null ? v : { anchor: was.anchor, ...v }, now);
  const same = was !== null && ["kind", "every", "at", "zone", "anchor", "enabled"].every((k) => was[k] === s[k]) &&
    was.weekdays.join() === s.weekdays.join();
  const due = !s.enabled ? null : same ? was.nextDue : iso(nextAfter(s, now));
  const fields = [s.title, s.prompt, s.workspace, s.worktree ? 1 : 0, s.provider, s.model, s.effort, s.fast ? 1 : 0, s.access,
    s.kind, s.every, s.at, s.kind === "weekly" ? s.weekdays.join(",") : null, s.zone, s.anchor, s.missed, s.enabled ? 1 : 0, due];
  late(deadline);
  if (was === null) {
    const { changes } = await exec(
      `INSERT INTO schedules (id, title, prompt, workspace, worktree, provider, model, effort, fast, access, kind, every, at, weekdays, zone, anchor, missed, enabled, next_due)
       SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ? WHERE (SELECT count(*) FROM schedules) < ${MAX_SCHEDULES}`,
      [s.id, ...fields],
    );
    if (changes === 0) throw new Invalid("there are " + MAX_SCHEDULES + " schedules already, the most there can be");
    return load(s.id);
  }
  const statements = [[
    `UPDATE schedules SET title = ?, prompt = ?, workspace = ?, worktree = ?, provider = ?, model = ?, effort = ?, fast = ?, access = ?,
       kind = ?, every = ?, at = ?, weekdays = ?, zone = ?, anchor = ?, missed = ?, enabled = ?, next_due = ?, revision = revision + 1 WHERE id = ?`,
    [...fields, s.id],
  ]];
  if (!s.enabled) {
    statements.push([
      "UPDATE runs SET state = 'skipped', reason = 'turned off', request = NULL, at = ? WHERE schedule = ? AND manual = 0 AND state = 'starting' AND sent = 0",
      [iso(now), s.id],
    ]);
  }
  await transaction(statements);
  return load(s.id);
}

// --- Starting runs -----------------------------------------------------------------

// request is the create a run of s sends, under key, at due: written with
// the run's intent, and sent the same on every try.
function request(s, key, due) {
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
async function prune(schedule) {
  await exec(
    `DELETE FROM runs WHERE schedule = ? AND state <> 'starting' AND open = 0
       AND key NOT IN (SELECT key FROM runs WHERE schedule = ? ORDER BY due DESC, key DESC LIMIT ${KEEP})`,
    [schedule, schedule],
  );
}

// settle ends run r as state, a thread with it counting as one that may
// still go, and writes also with it, all or none.
async function settle(r, state, { thread = null, reason = null, also = [] } = {}) {
  await transaction([
    [
      "UPDATE runs SET state = ?, thread = ?, open = ?, reason = ?, request = NULL, at = ? WHERE schedule = ? AND key = ?",
      [state, thread, thread === null ? 0 : 1, reason?.slice(0, MAX_REASON) ?? null, iso(Date.now()), r.schedule, r.key],
    ],
    ...also,
  ]);
  await prune(r.schedule);
}

// inflight are the creates this runtime is sending, by run: a second send
// of one answers the first's thread.
const inflight = new Map();
const runID = (r) => r.schedule + "\n" + r.key;

// send sends run r's create and settles it on the answer, writing also
// with the settle; it answers the thread, or null. A run not sent yet is
// sent only if it is still starting: turning its schedule off skips it in
// the same write. A write that fails after the create leaves the run
// starting, to be sent again.
function send(r, also = []) {
  const id = runID(r);
  if (!inflight.has(id)) {
    inflight.set(id, sending(r, also).finally(() => inflight.delete(id)));
  }
  return inflight.get(id);
}

async function sending(r, also) {
  if (r.sent === 0) {
    const { changes } = await exec(
      "UPDATE runs SET sent = 1 WHERE schedule = ? AND key = ? AND state = 'starting' AND sent = 0",
      [r.schedule, r.key],
    );
    if (changes === 0) return null;
  }
  const { prompt, options } = JSON.parse(r.request);
  let thread;
  try {
    ({ thread } = await glitched.threads.create(prompt, options));
  } catch (e) {
    await settle(r, "failed", { reason: e.message, also });
    return null;
  }
  await settle(r, "started", { thread, also });
  return thread;
}

// retry sends again run r, left starting, unless this runtime is sending
// it already, and records the try, so a tick tries the runs it tried
// longest ago first, across restarts too.
async function retry(r, also = []) {
  await exec("UPDATE runs SET tried = ? WHERE schedule = ? AND key = ?", [iso(Date.now()), r.schedule, r.key]);
  if (!inflight.has(runID(r))) await send(r, also);
}

// threadsOf reads the engine's threads once for a tick, when the tick
// first needs them, and when it read them.
function threadsOf() {
  let read = null;
  return () => (read ??= glitched.threads.list().then((list) => ({ status: new Map(list.map((t) => [t.id, t.status])), at: Date.now() })));
}

// reconcile checks the threads of runs that may still go, schedule's or
// every schedule's, a page at a time: one that has stopped, or is gone,
// stops counting, and its schedule keeps its newest history again. It
// answers whether one still works or waits on you; a run settled since
// the threads were read counts as one that does.
async function reconcile(threads, schedule = null) {
  let after = ["", ""];
  let busy = false;
  for (;;) {
    const rows = await query(
      `SELECT schedule, key, thread, at FROM runs WHERE open = 1 AND (? IS NULL OR schedule = ?) AND (schedule, key) > (?, ?)
       ORDER BY schedule, key LIMIT ?`,
      [schedule, schedule, ...after, OPEN_PAGE],
    );
    const { status, at } = rows.length > 0 ? await threads() : {};
    const stopped = new Map();
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
    if (rows.length < OPEN_PAGE) return busy;
    after = [rows.at(-1).schedule, rows.at(-1).key];
  }
}

// going is whether a run of s may still go: one starting, or one whose
// thread works or waits on you.
async function going(s, threads) {
  const [starting] = await query("SELECT 1 FROM runs WHERE schedule = ? AND state = 'starting' LIMIT 1", [s.id]);
  return starting !== undefined || reconcile(threads, s.id);
}

// unit does what schedule id's automatic runs need, from what it is now:
// occur, read again for a schedule saved meanwhile.
async function unit(id, threads) {
  for (let i = 0; i < 3; i++) {
    const [row] = await query("SELECT * FROM schedules WHERE id = ?", [id]);
    if (row === undefined || (await occur(fromRow(row), threads))) return;
  }
}

// occur sends again a run of s left starting, as its catch-up, and moves
// next_due past now with its record. Else, on and due, it records the run
// of its latest occurrence, unless that is before its next_due: missed
// where its next_due shows it was, skipped where a run of it may still go,
// and starting otherwise, which it then sends; next_due moves past now
// with that record. It answers false, and writes nothing, where s was
// saved since it was read. Every step reads what is there and does what
// is left, so a crash anywhere comes back to the same end.
async function occur(s, threads) {
  const now = Date.now();
  const advance = ["UPDATE schedules SET next_due = ? WHERE id = ? AND enabled = 1 AND next_due = ?", [iso(nextAfter(s, now)), s.id, s.nextDue]];
  const starting = await query("SELECT * FROM runs WHERE schedule = ? AND manual = 0 AND state = 'starting' ORDER BY due", [s.id]);
  for (const r of starting) {
    if (r.sent === 0 && !s.enabled) {
      await settle(r, "skipped", { reason: "turned off" });
    } else {
      await retry(r, [advance]);
    }
  }
  if (starting.length > 0 || !s.enabled || s.nextDue === null) return true;
  const nextDue = Date.parse(s.nextDue);
  const at = latestBy(s, now);
  if (at === null || at < nextDue) return true;
  const occ = iso(at);
  const key = s.id + "@" + occ;
  let state = "starting";
  let reason = null;
  if (s.missed === "skip" && now - nextDue > GRACE) {
    [state, reason] = ["missed", "Glitched wasn't running then"];
  } else {
    const busy = await going(s, threads).catch((e) => e);
    if (busy instanceof Error) {
      [state, reason] = ["failed", "its runs couldn't be checked: " + busy.message];
    } else if (busy) {
      [state, reason] = ["skipped", "the previous run is still going"];
    }
  }
  const r = { schedule: s.id, key, sent: 0, request: state === "starting" ? request(s, key, occ) : null };
  const [{ changes }] = await transaction([
    [
      `INSERT INTO runs (schedule, key, due, state, request, reason, tried, at)
       SELECT ?, ?, ?, ?, ?, ?, ?, ? WHERE EXISTS (SELECT 1 FROM schedules WHERE id = ? AND enabled = 1 AND next_due = ? AND revision = ?)
       ON CONFLICT (schedule, key) DO NOTHING`,
      [s.id, key, occ, state, r.request, reason, r.request === null ? null : iso(now), iso(now), s.id, s.nextDue, s.revision],
    ],
    [advance[0] + " AND revision = ?", [...advance[1], s.revision]],
  ]);
  if (changes === 0) return false;
  if (state === "starting") {
    await send(r);
  } else {
    await prune(s.id);
  }
  return true;
}

// pages are one kind of a tick's work, read PAGE at a time as it goes:
// next answers the next row the tick hasn't taken, by key, or null at the
// end.
function pages(read) {
  const taken = new Set();
  let rows = [];
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

// tick checks the threads of runs that may still go, then takes turns: a
// Run now left starting, then a schedule's unit that is due or has a run
// left starting, and so on, each kind's tried longest ago first, till both
// are done or its budget is spent. Which kind goes first alternates from
// tick to tick. One that fails holds up none of the rest; the host logs
// what failed once the tick ends.
async function tick() {
  await db();
  const begun = Date.now();
  const failed = [];
  const threads = threadsOf();
  await reconcile(threads).catch((e) => failed.push(e.message));
  const [turn] = await query("SELECT value FROM meta WHERE key = 'turn'");
  const manualFirst = turn?.value !== "automatic";
  await exec("INSERT INTO meta (key, value) VALUES ('turn', ?) ON CONFLICT (key) DO UPDATE SET value = excluded.value", [manualFirst ? "automatic" : "manual"]);
  const manual = {
    next: pages(() => query(
      `SELECT * FROM runs WHERE manual = 1 AND state = 'starting' AND (tried IS NULL OR tried <= ?)
       ORDER BY tried IS NOT NULL, tried, schedule, key LIMIT ?`,
      [iso(begun), PAGE],
    )),
    key: runID,
    does: (r) => retry(r),
  };
  const automatic = {
    next: pages(() => query(
      `SELECT s.id FROM schedules s
       LEFT JOIN (SELECT schedule, max(tried) AS tried FROM runs WHERE manual = 0 AND state = 'starting' GROUP BY schedule) w ON w.schedule = s.id
       WHERE ((s.enabled = 1 AND s.next_due <= ?) OR w.schedule IS NOT NULL) AND (w.tried IS NULL OR w.tried <= ?)
       ORDER BY w.tried IS NOT NULL, w.tried, s.id LIMIT ?`,
      [iso(begun), iso(begun), PAGE],
    )),
    key: (s) => s.id,
    does: (s) => unit(s.id, threads),
  };
  let kinds = manualFirst ? [manual, automatic] : [automatic, manual];
  while (kinds.length > 0) {
    for (const k of kinds.slice()) {
      if (Date.now() - begun > BUDGET) {
        kinds = [];
        break;
      }
      const r = await k.next(k.key).catch((e) => {
        failed.push(e.message);
        return null;
      });
      if (r === null) {
        kinds = kinds.filter((x) => x !== k);
      } else {
        await k.does(r).catch((e) => failed.push(e.message));
      }
    }
  }
  if (failed.length > 0) throw new Error(failed.join("; "));
}

// --- What the engine calls -----------------------------------------------------------

export function timer() {
  return tick();
}

// OP is what names one Run now: the page makes one for each click, and
// sends the same on each try of it.
const OP = /^[0-9A-Za-z_-]{1,36}$/;

const methods = {
  // list is a page of the schedules, by title, after the one named.
  async list({ after } = {}) {
    const rows = await query(SCHEDULE + " WHERE (s.title, s.id) > (?, ?) ORDER BY s.title, s.id LIMIT ?", [
      after?.title ?? "", after?.id ?? "", LIST_PAGE + 1,
    ]);
    const schedules = rows.slice(0, LIST_PAGE).map(fromRow);
    const last = schedules.at(-1);
    return { schedules, next: rows.length > LIST_PAGE ? { title: last.title, id: last.id } : null };
  },
  save: ({ schedule }, deadline) => save(schedule ?? {}, deadline),
  async delete({ id }, deadline) {
    await load(id);
    late(deadline);
    await exec("DELETE FROM schedules WHERE id = ?", [id]);
    return null;
  },
  async runs({ id, limit }) {
    const n = Number.isInteger(limit) && limit > 0 ? Math.min(limit, KEEP) : 50;
    return query("SELECT schedule, due, key, thread, state, reason, at FROM runs WHERE schedule = ? ORDER BY due DESC, key DESC LIMIT ?", [id, n]);
  },
  // runNow starts s now, as the person asked for it: past the check that
  // a run of it may still go. A try of an op already written answers its
  // run's thread, and starts no other.
  async runNow({ id, op }, deadline) {
    if (typeof op !== "string" || !OP.test(op)) throw new Invalid("op is 1 to 36 letters, digits, - or _, the same on each try of one Run now");
    const s = await load(id);
    const now = iso(Date.now());
    const key = "manual:" + s.id + ":" + op;
    late(deadline);
    await exec(
      `INSERT INTO runs (schedule, key, due, manual, state, request, sent, tried, at)
       SELECT ?, ?, ?, 1, 'starting', ?, 1, ?, ? WHERE EXISTS (SELECT 1 FROM schedules WHERE id = ?)
       ON CONFLICT (schedule, key) DO NOTHING`,
      [s.id, key, now, request(s, key, now), now, now, s.id],
    );
    const [r] = await query("SELECT * FROM runs WHERE schedule = ? AND key = ?", [s.id, key]);
    if (r === undefined) throw new Invalid("no schedule " + id);
    return { thread: r.state === "starting" ? await send(r) : r.thread };
  },
  // preview is when a schedule, saved or not, runs next: count occurrences
  // after from, which is now without one.
  preview({ schedule, from, count }) {
    const s = parse({ ...schedule, id: schedule?.id ?? ulid(0) }, Date.now());
    let t = from === undefined ? Date.now() : time(from);
    if (Number.isNaN(t)) throw new Invalid("from is an RFC 3339 time");
    const out = [];
    for (let i = 0; i < Math.min(count ?? 5, 100); i++) {
      const next = nextAfter(s, t);
      if (next === null) break;
      out.push(iso(next));
      t = next;
    }
    return out;
  },
};

// serve answers the page, each call as it comes. A call refuses once its
// deadline has passed: as it comes, once the database is ready, and before
// each write.
export function serve({ method, args, deadline }) {
  const m = methods[method];
  if (!m) throw new Error("no method " + method);
  late(deadline);
  if (method === "preview") return m(args ?? {});
  return db().then(() => {
    late(deadline);
    return m(args ?? {}, deadline);
  });
}

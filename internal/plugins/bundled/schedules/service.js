// Schedules: prompts that start an agent thread on a schedule. The service
// is the only writer of its glitched.db; each minute it starts what is due,
// once per occurrence, under a key the host keeps to one thread across
// restarts. It never wakes the Mac: a run missed while the engine was down
// runs once when it's back, or is skipped, as the schedule says.

const ACCESS = ["Ask for approval", "Approve for me", "Full access"];
const MINUTE = 60_000;
const DAY = 1440 * MINUTE;
// A run this late is missed, for a schedule that skips missed runs.
const GRACE = 10 * MINUTE;
// How long a tick starts runs for, so a write of the page waits on it no
// longer; what is left is due on the next.
const BUDGET = 20_000;
// The most rows a tick reads of each kind, well past what a budget starts.
const PAGE = 500;

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
        last_thread TEXT,
        CHECK ((kind = 'interval') = (every IS NOT NULL AND at IS NULL))
      );
      CREATE TABLE IF NOT EXISTS runs (
        schedule TEXT NOT NULL,
        due TEXT NOT NULL,
        key TEXT NOT NULL,
        thread TEXT,
        state TEXT NOT NULL CHECK (state IN ('starting', 'started', 'skipped', 'missed', 'failed')),
        reason TEXT,
        at TEXT NOT NULL,
        PRIMARY KEY (schedule, due)
      );
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

// serial runs fn after every call before it has settled: a tick and a
// call of the page never interleave their writes.
let queue = Promise.resolve();
function serial(fn) {
  const done = queue.then(fn);
  queue = done.catch(() => {});
  return done;
}

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
    prompt: text("prompt", 100_000),
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
  if (!s.workspace.startsWith("/")) throw new Invalid("workspace is an absolute folder");
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
    nextDue: r.next_due, lastThread: r.last_thread,
  };
}

async function load(id) {
  const rows = await glitched.db.query("SELECT * FROM schedules WHERE id = ?", [id]);
  if (rows.length === 0) throw new Invalid("no schedule " + id);
  return fromRow(rows[0]);
}

// save writes a schedule. One whose times and enabled stay as they were
// keeps its next_due, so an edit drops no run the timer hasn't seen.
async function save(v) {
  const now = Date.now();
  let was = null;
  if (v.id !== undefined) {
    was = await load(v.id);
    v = { anchor: was.anchor, ...v };
  }
  const s = parse(v, now);
  const same = was !== null && ["kind", "every", "at", "zone", "anchor", "enabled"].every((k) => was[k] === s[k]) &&
    was.weekdays.join() === s.weekdays.join();
  const due = !s.enabled ? null : same ? was.nextDue : iso(nextAfter(s, now));
  await glitched.db.exec(
    `INSERT INTO schedules (id, title, prompt, workspace, worktree, provider, model, effort, fast, access, kind, every, at, weekdays, zone, anchor, missed, enabled, next_due)
     VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
     ON CONFLICT (id) DO UPDATE SET title = excluded.title, prompt = excluded.prompt, workspace = excluded.workspace,
       worktree = excluded.worktree, provider = excluded.provider, model = excluded.model, effort = excluded.effort,
       fast = excluded.fast, access = excluded.access, kind = excluded.kind, every = excluded.every, at = excluded.at,
       weekdays = excluded.weekdays, zone = excluded.zone, anchor = excluded.anchor, missed = excluded.missed,
       enabled = excluded.enabled, next_due = excluded.next_due`,
    [s.id, s.title, s.prompt, s.workspace, s.worktree ? 1 : 0, s.provider, s.model, s.effort, s.fast ? 1 : 0, s.access,
      s.kind, s.every, s.at, s.kind === "weekly" ? s.weekdays.join(",") : null, s.zone, s.anchor, s.missed, s.enabled ? 1 : 0, due],
  );
  return load(s.id);
}

// --- Starting runs -----------------------------------------------------------------

async function record(s, due, key, state, fields = {}) {
  await glitched.db.exec(
    `INSERT INTO runs (schedule, due, key, thread, state, reason, at) VALUES (?, ?, ?, ?, ?, ?, ?)
     ON CONFLICT (schedule, due) DO UPDATE SET thread = excluded.thread, state = excluded.state, reason = excluded.reason, at = excluded.at`,
    [s.id, due, key, fields.thread ?? null, state, fields.reason ?? null, iso(Date.now())],
  );
}

// start asks the host for s's thread under key, at due, and records how it
// went. The key makes a repeat, as after a crash, answer the first thread,
// so a write that fails after the create leaves the run starting, and the
// next tick finishes it.
async function start(s, due, key) {
  let thread;
  try {
    ({ thread } = await glitched.threads.create(s.prompt, {
      title: s.title,
      workspace: s.workspace,
      model: { provider: s.provider, model: s.model, effort: s.effort, fast: s.fast },
      access: s.access,
      ...(s.worktree ? { worktree: {} } : {}),
      key: { id: key, at: due },
    }));
  } catch (e) {
    await record(s, due, key, "failed", { reason: e.message });
    return null;
  }
  await glitched.db.exec("UPDATE schedules SET last_thread = ? WHERE id = ?", [thread, s.id]);
  await record(s, due, key, "started", { thread });
  return thread;
}

// busy is whether the thread is still working or waiting on you. A thread
// that's gone isn't.
async function busy(thread) {
  if (!thread) return false;
  const t = (await glitched.threads.list()).find((r) => r.id === thread);
  return t !== undefined && (t.status === "working" || t.status === "waiting");
}

// due starts s's latest occurrence by now, once, unless a run of it is
// recorded, the schedule skips a run its next_due shows was missed, or its
// last run still goes. Every step reads what is there and does what is
// left, so a crash anywhere comes back to the same end.
async function due(s, now) {
  const at = latestBy(s, now);
  if (at !== null) {
    const occ = iso(at);
    const key = s.id + "@" + occ;
    const [run] = await glitched.db.query("SELECT state FROM runs WHERE schedule = ? AND due = ?", [s.id, occ]);
    if (run === undefined) {
      if (s.missed === "skip" && now - Date.parse(s.nextDue) > GRACE) {
        await record(s, occ, key, "missed", { reason: "Glitched wasn't running then" });
      } else {
        const [pending] = await glitched.db.query("SELECT 1 FROM runs WHERE schedule = ? AND state = 'starting' LIMIT 1", [s.id]);
        const going = pending !== undefined || (await busy((await load(s.id)).lastThread).catch((e) => e));
        if (going instanceof Error) {
          await record(s, occ, key, "failed", { reason: "its last run couldn't be checked: " + going.message });
        } else if (going) {
          await record(s, occ, key, "skipped", { reason: "the previous run is still going" });
        } else {
          await record(s, occ, key, "starting");
          await start(s, occ, key);
        }
      }
    }
  }
  await glitched.db.exec("UPDATE schedules SET next_due = ? WHERE id = ? AND enabled = 1", [iso(nextAfter(s, now)), s.id]);
}

// tick first finishes every run a crash or a failed write left starting,
// a Run now's and a disabled schedule's too, under its own key, then
// starts what is due, in next_due order, till its budget is spent. One
// schedule that fails doesn't hold up the rest; the host logs what failed.
async function tick() {
  await db();
  const begun = Date.now();
  const starting = await glitched.db.query(
    "SELECT s.*, r.due AS run_due, r.key AS run_key FROM runs r JOIN schedules s ON s.id = r.schedule WHERE r.state = 'starting' ORDER BY r.due LIMIT ?",
    [PAGE],
  );
  const rows = await glitched.db.query("SELECT * FROM schedules WHERE enabled = 1 AND next_due <= ? ORDER BY next_due LIMIT ?", [iso(begun), PAGE]);
  const work = [
    ...starting.map((r) => () => start(fromRow(r), r.run_due, r.run_key)),
    ...rows.map((r) => () => due(fromRow(r), Date.now())),
  ];
  const failed = [];
  for (const w of work) {
    if (Date.now() - begun > BUDGET) break;
    await w().catch((e) => failed.push(e.message));
  }
  if (failed.length > 0) throw new Error(failed.join("; "));
}

// --- What the engine calls -----------------------------------------------------------

// timer starts a tick, unless one still goes, and answers at once: a tick
// waits on creates of up to 30 s each, past what the timer's call may take.
// A tick that failed says so on the timer after it.
let ticking = null;
let failed = null;
export function timer() {
  ticking ??= serial(tick)
    .catch((e) => {
      failed = e;
    })
    .finally(() => {
      ticking = null;
    });
  const e = failed;
  failed = null;
  if (e) throw e;
}

const methods = {
  async list() {
    const rows = await glitched.db.query("SELECT * FROM schedules ORDER BY title, id");
    return rows.map(fromRow);
  },
  save: ({ schedule }) => save(schedule ?? {}),
  async delete({ id }) {
    await load(id);
    await glitched.db.exec("DELETE FROM schedules WHERE id = ?", [id]);
    return null;
  },
  async runs({ id, limit }) {
    const n = Number.isInteger(limit) && limit > 0 ? Math.min(limit, 200) : 50;
    return glitched.db.query("SELECT schedule, due, key, thread, state, reason, at FROM runs WHERE schedule = ? ORDER BY due DESC LIMIT ?", [id, n]);
  },
  // runNow starts s now, as the person asked for it: past the check that
  // its last run still goes.
  async runNow({ id }) {
    const s = await load(id);
    const now = Date.now();
    const occ = iso(now);
    const key = "manual:" + ulid(now);
    await record(s, occ, key, "starting");
    return { thread: await start(s, occ, key) };
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

// serve answers the page. A read goes at once; a write waits its turn
// behind the tick.
export function serve({ method, args }) {
  const m = methods[method];
  if (!m) throw new Error("no method " + method);
  if (method === "preview") return m(args ?? {});
  if (method === "list" || method === "runs") return db().then(() => m(args ?? {}));
  return serial(async () => {
    await db();
    return m(args ?? {});
  });
}

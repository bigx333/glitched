// The pull request of a thread's branch, in the thread's header, read with
// git and gh as you. Each workspace has one Repo, read in one place (read)
// and drawn from in another (header):
//
//   { kind: "hidden" }                       nothing to show
//   { kind: "gh", problem }                  a GitHub remote gh can't answer for: "missing" or "signed-out"
//   { kind: "ready", branch, base, slug, ahead, dirty, pushed }        no pull request yet
//   { kind: "pr", branch, slug, ahead, dirty, pr, stale? }             its pull request
//
// pr is { number, title, url, state, review, conflicts, checks }, each
// check { name, status, url? }; stale says why the last read failed.

const GH_ENV = { GH_PROMPT_DISABLED: "1" };
const HIDDEN = { kind: "hidden" };
const FRESH = 10 * 1000;
const OPEN_EVERY = 55 * 1000;
const IDLE_EVERY = 5 * 60 * 1000 - 5 * 1000;
const MAX_ROWS = 12;
const PR_FIELDS = "number,title,url,state,isDraft,reviewDecision,mergeable,statusCheckRollup,headRefName";

const SYSTEM =
  'You write GitHub pull request titles and descriptions. Answer with one JSON object and nothing else: {"title": "...", "body": "..."}. ' +
  'The title is one line under 72 characters, in the imperative. The body is GitHub Markdown: "## Summary", a few bullets of what changed and why, ' +
  'then "## Testing", how the change was checked, or "Not stated." when the changes don\'t say.';

export function init() {
  return { state: { shown: null, repos: {}, bases: {}, open: null, passed: false, note: null, dialog: null } };
}

// header draws a copy from its workspace's Repo, and nothing until one is read.
export function place({ state, items }) {
  return items.map(({ key, context }) => {
    const repo = context?.local && context.workspace ? state.repos[context.workspace]?.repo : null;
    return repo ? header(repo, key, state) : null;
  });
}

function header(repo, key, state) {
  const open = state.open === key;
  const popover = (width, children) => ({ active: open, open, onClick: "popover.toggle", onClose: "popover.close", popover: { type: "column", width, gap: 10, children } });
  switch (repo.kind) {
    case "gh":
      return {
        type: "button",
        id: "pr",
        style: "ghost",
        label: "GitHub CLI",
        tooltip: repo.problem === "missing" ? "Install the GitHub CLI" : "Sign the GitHub CLI in",
        gap: 4,
        children: [
          { type: "icon", name: "pull-request", color: "$warning" },
          { type: "text", text: "PR", color: "$warning" },
        ],
        ...popover(300, ghProblem(repo.problem)),
      };
    case "ready": {
      if (repo.ahead === 0 && repo.dirty === 0) return null;
      const button = { type: "button", id: "create", style: "outline", icon: "pull-request", label: "Create PR", tooltip: "Create a pull request", onClick: "create.open" };
      const d = state.dialog;
      return d && d.key === key ? { type: "row", gap: 8, children: [button, createDialog(d)] } : button;
    }
    case "pr": {
      const pr = repo.pr;
      const mark = checksMark(pr.checks);
      return {
        type: "button",
        id: "pr",
        style: "ghost",
        label: "Pull request #" + pr.number,
        tooltip: "Pull request #" + pr.number + ": " + STATES[pr.state].label + (mark ? ", checks " + mark.word : ""),
        gap: 4,
        children: [
          { type: "icon", name: "pull-request", color: STATES[pr.state].color },
          { type: "text", text: "#" + pr.number },
          ...(mark ? [{ type: "icon", name: mark.icon, color: mark.color }] : []),
        ],
        ...popover(360, prPopover(repo, state)),
      };
    }
  }
  return null;
}

const STATES = {
  open: { label: "Open", badge: "success", color: "$success" },
  draft: { label: "Draft", badge: "default", color: "$text.muted" },
  merged: { label: "Merged", badge: "accent", color: "$accent" },
  closed: { label: "Closed", badge: "danger", color: "$danger" },
};

const REVIEWS = { approved: "Approved", changes: "Changes requested", required: "Awaiting review" };

const CHECKS = {
  failed: { label: "Failed", icon: "close", color: "$danger" },
  running: { label: "Running", icon: "clock-circle", color: "$warning" },
  waiting: { label: "Waiting", icon: "clock-circle", color: "$text.muted" },
  passed: { label: "Passed", icon: "check-circle", color: "$success" },
  skipped: { label: "Skipped", icon: "check", color: "$text.muted" },
};

function checksMark(checks) {
  if (checks.some((c) => c.status === "failed")) return { word: "failing", icon: "close", color: "$danger" };
  if (checks.some((c) => c.status === "running" || c.status === "waiting")) return { word: "running", icon: "clock-circle", color: "$warning" };
  if (checks.some((c) => c.status === "passed")) return { word: "passed", icon: "check-circle", color: "$success" };
  return null;
}

function ghProblem(problem) {
  const text =
    problem === "missing"
      ? "Install the GitHub CLI to see and open pull requests: `brew install gh`, then `gh auth login`."
      : "Sign gh in to GitHub: run `gh auth login` in a terminal, then Refresh.";
  return [
    { type: "markdown", text },
    { type: "row", justify: "end", children: [{ type: "button", id: "refresh", label: "Refresh", onClick: "refresh" }] },
  ];
}

function checkRow(c) {
  const s = CHECKS[c.status];
  return {
    type: "row",
    gap: 6,
    align: "center",
    children: [
      { type: "icon", name: s.icon, color: s.color },
      { type: "text", text: c.name + " · " + s.label, singleLine: true },
      { type: "spacer" },
      c.url ? { type: "link", text: "Details", href: c.url } : { type: "spacer" },
    ],
  };
}

function rows(checks, more) {
  const out = checks.slice(0, MAX_ROWS).map(checkRow);
  if (checks.length > MAX_ROWS) out.push({ type: "text", text: "and " + (checks.length - MAX_ROWS) + " more " + more, style: "muted" });
  return out;
}

function prPopover(repo, state) {
  const pr = repo.pr;
  const badges = [{ type: "badge", text: STATES[pr.state].label, style: STATES[pr.state].badge }];
  if (pr.review) badges.push({ type: "badge", text: REVIEWS[pr.review], style: "pill" });
  if (pr.conflicts) badges.push({ type: "badge", text: "Has conflicts", style: "warning" });
  const attention = pr.checks.filter((c) => c.status === "failed" || c.status === "running" || c.status === "waiting");
  attention.sort((a, b) => (a.status === "failed" ? 0 : 1) - (b.status === "failed" ? 0 : 1));
  const done = pr.checks.filter((c) => c.status === "passed" || c.status === "skipped");
  const checks = [];
  if (pr.checks.length === 0) checks.push({ type: "text", text: "No checks reported.", style: "muted" });
  checks.push(...rows(attention, "running or failed"));
  if (done.length > 0) {
    checks.push({ type: "row", id: "passed", onClick: "passed.toggle", children: [{ type: "text", text: (state.passed ? "Hide " : "Show ") + done.length + " passed", style: "muted" }] });
    if (state.passed) checks.push(...rows(done, "passed"));
  }
  const buttons = [
    { type: "button", id: "github", label: "Open on GitHub", onClick: "github" },
    { type: "button", id: "copy", label: "Copy link", onClick: "copy" },
    { type: "button", id: "refresh", label: "Refresh", onClick: "refresh" },
  ];
  if (pr.state === "draft") buttons.push({ type: "button", id: "ready", label: "Ready for review", progress: true, onClick: "ready" });
  if (pr.checks.some((c) => c.status === "failed")) buttons.push({ type: "button", id: "fix", label: "Ask the agent to fix", style: "primary", onClick: "fix" });
  const lines = [];
  if (repo.stale) lines.push({ type: "text", id: "stale", text: "Couldn't refresh: " + repo.stale, style: "error", selectable: true });
  if (state.note) lines.push({ type: "text", id: "note", text: state.note, style: "error", selectable: true });
  return [
    { type: "text", text: pr.title, style: "strong" },
    { type: "row", gap: 6, wrap: true, children: badges },
    { type: "column", id: "checks", gap: 4, children: checks },
    ...lines,
    { type: "row", gap: 6, wrap: true, children: buttons },
  ];
}

function createDialog(d) {
  const files = d.dirty === 1 ? "1 changed file" : d.dirty + " changed files";
  const where = d.pushed && !d.dirty ? d.branch + " is pushed to " + d.to + "." : "Pushes " + d.branch + " to " + d.to + (d.track ? ", which it starts tracking." : ".");
  return {
    type: "dialog",
    id: "create-dialog",
    title: "Create pull request",
    open: true,
    onClose: "create.cancel",
    children: [
      { type: "text", id: "what", text: "Opens a pull request on " + d.slug + " from " + d.branch + " into the base below. " + where, style: "muted" },
      { type: "input", id: "title", label: "Title", value: d.title },
      { type: "input", id: "body", label: "Description", value: d.body, multiline: true },
      { type: "input", id: "base", label: "Base", value: d.base },
      { type: "toggle", id: "draft", label: "Open as draft", value: d.draft },
      d.dirty > 0 ? { type: "toggle", id: "commit", label: "Commit " + files + " first", value: d.commit } : { type: "spacer", id: "nocommit" },
      {
        type: "row",
        id: "writing",
        gap: 8,
        align: "center",
        children: [
          { type: "button", id: "write", label: "Write with AI", progress: true, onClick: "create.write" },
          d.by ? { type: "text", id: "by", text: d.by, style: "muted" } : { type: "spacer", id: "noby" },
        ],
      },
      d.error ? { type: "text", id: "error", text: d.error, style: "error", selectable: true } : { type: "spacer", id: "noerror" },
      {
        type: "row",
        id: "buttons",
        gap: 8,
        justify: "end",
        children: [
          { type: "button", id: "cancel", label: "Cancel", onClick: "create.cancel" },
          { type: "button", id: "go", label: "Create pull request", style: "primary", progress: true, onClick: "create.go" },
        ],
      },
    ],
  };
}

async function git(cwd, args, timeout = 5) {
  const r = await glitched.run("git", args, { cwd, timeout });
  return { ok: r.status === 0, out: r.stdout, err: reason(r.stderr || r.stdout, r.status), timedOut: r.status === -1 };
}

// gh answers missing when it isn't installed, as glitched.run says.
async function gh(cwd, args, stdin = "", timeout = 15) {
  try {
    const r = await glitched.run("gh", args, { cwd, timeout, stdin, env: GH_ENV });
    return { ok: r.status === 0, out: r.stdout, err: (r.stderr || r.stdout).trim(), timedOut: r.status === -1 };
  } catch (e) {
    if (/isn't installed/.test(e.message)) return { ok: false, missing: true, err: e.message };
    throw e;
  }
}

// reason is the line of git's output that says why it failed: not the
// "To <remote>" a push starts with, nor its hints. A push the remote
// turned down says why it did.
function reason(output, status) {
  const lines = output.split(/[\r\n]+/).map((l) => l.trim()).filter(Boolean);
  const rejected = lines.map((l) => /^! \[(remote )?rejected\] .*\((.+)\)$/.exec(l)).find(Boolean);
  let why;
  if (rejected && (rejected[2] === "fetch first" || rejected[2] === "non-fast-forward")) {
    why = "the remote has commits this branch doesn't; pull them, then push again";
  } else if (rejected) {
    const said = lines.find((l) => l.startsWith("remote: ") && l.length > 8);
    why = "the remote refused it (" + rejected[2] + ")" + (said ? ": " + said.slice(8).trim() : "");
  } else {
    const line = lines.find((l) => !/^(To |hint: )/.test(l));
    why = line ? line.replace(/^(fatal|error): /, "") : "git exited " + status;
  }
  return short(why);
}

function short(s) {
  const chars = [...s];
  return chars.length > 200 ? chars.slice(0, 199).join("") + "…" : s;
}

// ghReason is why a gh run failed, in a line.
function ghReason(r) {
  if (r.timedOut) return "GitHub didn't answer in time";
  if (/rate limit/i.test(r.err)) return "GitHub's rate limit was reached; try again in a few minutes";
  const line = r.err.split(/[\r\n]+/).map((l) => l.trim()).find(Boolean);
  return short(line || "gh failed");
}

function signedOut(err) {
  return /not logged in|gh auth login|authenticat|bad credentials/i.test(err);
}

async function config(cwd, key) {
  const r = await git(cwd, ["config", "--get", key]);
  return r.ok ? r.out.trim() : "";
}

// target is the one ref a push updates: the branch's upstream, or origin's
// branch of the same name when it tracks none. The push names it in full,
// with no +, so the repository's push config (pushRemote, push.default,
// remote.*.push, a forced refspec) can't send it anywhere else, or force it.
async function target(cwd, branch) {
  const remote = await config(cwd, "branch." + branch + ".remote");
  const merge = await config(cwd, "branch." + branch + ".merge");
  if (remote === ".") return { noPush: "the branch tracks a local branch" };
  if (remote && merge.startsWith("refs/heads/")) {
    return { push: { remote, branch: merge.slice("refs/heads/".length), track: false } };
  }
  const remotes = await git(cwd, ["remote"]);
  if (!remotes.ok || !remotes.out.split("\n").includes("origin")) return { noPush: "the repository has no origin" };
  return { push: { remote: "origin", branch, track: true } };
}

// github is the owner/name of a github.com remote, null for any other.
function github(url) {
  const m = /^(?:https:\/\/(?:[^@/]+@)?github\.com\/|ssh:\/\/git@github\.com\/|git@github\.com:)([\w.-]+)\/([\w.-]+?)(?:\.git)?\/?$/.exec(url.trim());
  return m ? m[1] + "/" + m[2] : null;
}

class Unread extends Error {}

// read is the workspace's Repo, from the first answer that settles it. A
// read GitHub fails throws Unread.
async function read(ws, bases) {
  const head = await git(ws, ["rev-parse", "--abbrev-ref", "HEAD"]);
  const branch = head.out.trim();
  if (!head.ok || branch === "HEAD") return HIDDEN;
  const { push } = await target(ws, branch);
  if (!push) return HIDDEN;
  const url = await git(ws, ["remote", "get-url", push.remote]);
  const slug = url.ok ? github(url.out) : null;
  if (!slug) return HIDDEN;
  const auth = await gh(ws, ["auth", "status", "--hostname", "github.com"]);
  if (auth.missing) return { kind: "gh", problem: "missing" };
  if (!auth.ok) {
    if (signedOut(auth.err)) return { kind: "gh", problem: "signed-out" };
    throw new Unread(ghReason(auth));
  }
  if (!bases[slug]) {
    const r = await gh(ws, ["repo", "view", slug, "--json", "defaultBranchRef"]);
    const name = r.ok ? JSON.parse(r.out).defaultBranchRef?.name : "";
    if (!name) throw new Unread(r.ok ? "GitHub didn't say the repository's default branch" : ghReason(r));
    bases[slug] = name;
  }
  const base = bases[slug];
  if (push.branch === base) return HIDDEN;
  const list = await gh(ws, ["pr", "list", "--repo", slug, "--head", push.branch, "--state", "all", "--limit", "10", "--json", PR_FIELDS]);
  if (!list.ok) throw new Unread(ghReason(list));
  const pr = pick(JSON.parse(list.out) ?? [], push.branch);
  const ahead = await count(ws, push.remote, base);
  const status = await git(ws, ["status", "--porcelain", "--untracked-files=all"]);
  const dirty = status.ok ? status.out.split("\n").filter(Boolean).length : 0;
  if (pr) return { kind: "pr", branch, slug, ahead, dirty, pr };
  const remoteHead = await git(ws, ["rev-parse", "--verify", "-q", "refs/remotes/" + push.remote + "/" + push.branch]);
  const local = await git(ws, ["rev-parse", "HEAD"]);
  return { kind: "ready", branch, base, slug, ahead, dirty, pushed: remoteHead.ok && remoteHead.out.trim() === local.out.trim() };
}

// count is how many commits HEAD has that the base hasn't, by the remote's
// base, else a local one; 0 without either.
async function count(ws, remote, base) {
  for (const ref of ["refs/remotes/" + remote + "/" + base, "refs/heads/" + base]) {
    const r = await git(ws, ["rev-list", "--count", ref + "..HEAD"]);
    if (r.ok) return Number(r.out.trim());
  }
  return 0;
}

// pick is the branch's pull request: an open one first, then the newest.
function pick(prs, head) {
  const mine = prs.filter((p) => p.headRefName === head);
  mine.sort((a, b) => (a.state === "OPEN" ? 0 : 1) - (b.state === "OPEN" ? 0 : 1) || b.number - a.number);
  const p = mine[0];
  if (!p) return null;
  return {
    number: p.number,
    title: p.title,
    url: p.url,
    state: p.state === "OPEN" ? (p.isDraft ? "draft" : "open") : p.state === "MERGED" ? "merged" : "closed",
    review: { APPROVED: "approved", CHANGES_REQUESTED: "changes", REVIEW_REQUIRED: "required" }[p.reviewDecision] ?? null,
    conflicts: p.mergeable === "CONFLICTING" ? true : p.mergeable === "MERGEABLE" ? false : null,
    checks: (p.statusCheckRollup || []).map(check),
  };
}

const FAILED = ["FAILURE", "ERROR", "TIMED_OUT", "CANCELLED", "ACTION_REQUIRED", "STARTUP_FAILURE"];

// check is one of a rollup's: a CheckRun's status and conclusion, or a
// StatusContext's state.
function check(c) {
  const done = c.conclusion || (c.state && c.state !== "PENDING" && c.state !== "EXPECTED" ? c.state : "");
  let status;
  if (FAILED.includes(done)) status = "failed";
  else if (done === "SUCCESS") status = "passed";
  else if (done) status = "skipped";
  else if (c.status === "IN_PROGRESS" || c.state === "PENDING") status = "running";
  else status = "waiting";
  const url = c.detailsUrl || c.targetUrl || "";
  return { name: c.name || c.context || "check", status, ...(url.startsWith("https://") ? { url } : {}) };
}

// refresh reads the shown workspace again. A failed read keeps the last
// pull request, saying why it couldn't refresh.
async function refresh(state, shown) {
  if (!shown) return { state };
  const bases = { ...state.bases };
  const was = state.repos[shown.workspace]?.repo;
  let repo;
  try {
    repo = await read(shown.workspace, bases);
  } catch (e) {
    if (!(e instanceof Unread)) throw e;
    repo = was?.kind === "pr" ? { ...was, stale: e.message } : was ?? HIDDEN;
  }
  return { state: { ...state, bases, repos: { ...state.repos, [shown.workspace]: { repo, at: Date.now() } } } };
}

function shownBy(key, context) {
  return context?.local && context.workspace ? { key, workspace: context.workspace } : null;
}

// due is whether the shown workspace's read is old: a minute with a pull
// request open, five otherwise.
function due(state) {
  const r = state.shown && state.repos[state.shown.workspace];
  if (!r) return state.shown != null;
  const open = r.repo.kind === "pr" && (r.repo.pr.state === "open" || r.repo.pr.state === "draft");
  return Date.now() - r.at >= (open ? OPEN_EVERY : IDLE_EVERY);
}

// update answers what happened. A call the app refuses, as a grant taken
// back, is told in a toast.
export async function update(args) {
  try {
    return await act(args);
  } catch (e) {
    await glitched.toast("Pull requests", e.message).catch(() => {});
    return {};
  }
}

async function act({ state, event, values }) {
  if (event.kind === "timer") return due(state) ? refresh(state, state.shown) : {};
  if (event.kind === "app") {
    switch (event.type) {
      case "place.shown": {
        const shown = shownBy(event.data.key, event.data.context);
        const next = { ...state, shown, note: null };
        const r = shown && state.repos[shown.workspace];
        return !shown || (r && Date.now() - r.at < FRESH) ? { state: next } : refresh(next, shown);
      }
      case "thread.files.changed":
        return state.shown && event.data.thread === state.shown.key ? refresh(state, state.shown) : {};
      case "window.focused":
        return event.data.focused && state.shown ? refresh(state, state.shown) : {};
    }
    return {};
  }
  if (event.kind !== "ui" || !event.place) return {};
  const key = event.place.key;
  const ctx = event.place.context;
  const shown = shownBy(key, ctx);
  const repo = shown && state.repos[shown.workspace]?.repo;
  switch (event.handler) {
    case "popover.toggle":
      return { state: { ...state, open: state.open === key ? null : key, passed: false, note: null } };
    case "popover.close":
      return { state: { ...state, open: null } };
    case "passed.toggle":
      return { state: { ...state, passed: !state.passed } };
    case "refresh":
      return refresh({ ...state, note: null }, shown);
    case "github":
      if (repo?.kind === "pr") await glitched.openURL(repo.pr.url);
      return {};
    case "copy":
      if (repo?.kind === "pr") {
        await glitched.clipboard.write(repo.pr.url);
        await glitched.toast("Link copied", repo.pr.url);
      }
      return {};
    case "ready": {
      if (repo?.kind !== "pr") return {};
      const r = await gh(shown.workspace, ["pr", "ready", String(repo.pr.number), "--repo", repo.slug]);
      if (!r.ok) return { state: { ...state, note: "gh pr ready failed: " + (r.missing ? r.err : ghReason(r)) } };
      return refresh({ ...state, note: null }, shown);
    }
    case "fix":
      if (repo?.kind !== "pr") return {};
      await glitched.composer.draft(fixPrompt(repo.pr), { thread: key });
      return { state: { ...state, open: null } };
    case "create.open":
      if (repo?.kind !== "ready") return {};
      return { state: { ...state, dialog: await draft(key, shown.workspace, repo, ctx.title) } };
    case "create.cancel":
      return { state: { ...state, dialog: null } };
    case "create.write": {
      const d = edited(state.dialog, values);
      return d ? { state: { ...state, dialog: await write(d, ctx.title) } } : {};
    }
    case "create.go": {
      const d = edited(state.dialog, values);
      if (!d) return {};
      const done = await create(d);
      if (done.error) return { state: { ...state, dialog: { ...d, error: done.error, ...(done.committed ? { dirty: 0, commit: false } : {}) } } };
      const next = await refresh({ ...state, dialog: null }, shown);
      if (done.url) await glitched.toast("Opened a pull request", done.url);
      return next;
    }
  }
  return {};
}

function fixPrompt(pr) {
  const failed = pr.checks.filter((c) => c.status === "failed");
  return [
    "Checks are failing on pull request #" + pr.number + " (" + pr.title + "): " + pr.url,
    "",
    ...failed.map((c) => "- " + c.name + (c.url ? ": " + c.url : "")),
    "",
    "Run `gh pr checks " + pr.number + "` to see them, and `gh run view <run-id> --log-failed` for a failed run's log. Fix what fails, then commit and push.",
  ].join("\n");
}

// edited is the dialog with what its fields hold now.
function edited(d, values) {
  if (!d) return null;
  const v = values || {};
  return {
    ...d,
    title: String(v.title ?? d.title),
    body: String(v.body ?? d.body),
    base: String(v.base ?? d.base).trim() || d.base,
    draft: v.draft ?? d.draft,
    commit: v.commit ?? d.commit,
    error: null,
  };
}

// draft is the dialog as it opens: the only commit's subject as the
// title, else the thread's title, and the commits under Summary.
async function draft(key, ws, repo, threadTitle) {
  const { push } = await target(ws, repo.branch);
  const log = await git(ws, ["log", "--format=%s", "refs/remotes/" + push.remote + "/" + repo.base + "..HEAD"]);
  const subjects = log.ok ? log.out.split("\n").filter(Boolean) : [];
  const title = subjects.length === 1 ? subjects[0] : threadTitle || repo.branch;
  const body = "## Summary\n\n" + (subjects.length ? subjects : [title]).map((s) => "- " + s).join("\n") + "\n";
  return {
    key,
    workspace: ws,
    slug: repo.slug,
    branch: repo.branch,
    base: repo.base,
    to: push.remote + "/" + push.branch,
    track: push.track,
    pushed: repo.pushed,
    dirty: repo.dirty,
    title,
    body,
    draft: false,
    commit: repo.dirty > 0,
    by: null,
    error: null,
  };
}

function cut(s, n) {
  return s.length > n ? s.slice(0, n) + "\n[cut]" : s;
}

// write asks a model for the title and description, from what the pull
// request would hold: its commits, its files and its diff from where it
// leaves the base, uncommitted changes included.
async function write(d, threadTitle) {
  const remote = d.to.slice(0, d.to.indexOf("/"));
  const mb = await git(d.workspace, ["merge-base", "refs/remotes/" + remote + "/" + d.base, "HEAD"]);
  const from = mb.ok ? mb.out.trim() : "HEAD";
  const log = mb.ok ? await git(d.workspace, ["log", "--format=%s%n%n%b", from + "..HEAD"]) : { out: "" };
  const stat = await git(d.workspace, ["diff", "--stat", from]);
  const patch = await git(d.workspace, ["diff", from], 10);
  const status = d.dirty > 0 ? await git(d.workspace, ["status", "--porcelain", "--untracked-files=all"]) : { out: "" };
  const prompt = [
    "Write the title and description of a GitHub pull request from " + d.branch + " into " + d.base + ".",
    "The thread that made the changes is titled: " + (threadTitle || d.branch),
    "",
    "Commits:",
    cut(log.out.trim() || "(none yet)", 12000),
    "",
    "Files changed:",
    cut(stat.out.trim(), 12000),
    ...(status.out.trim() ? ["", "Uncommitted, to be committed with it:", cut(status.out.trim(), 4000)] : []),
    "",
    "Diff:",
    cut(patch.out, 40000),
  ].join("\n");
  let a;
  try {
    a = await glitched.models.ask(prompt, { system: SYSTEM });
  } catch (e) {
    return { ...d, error: unwritten(e) };
  }
  const w = written(a.text);
  if (!w) return { ...d, error: "The model's answer wasn't a title and description. Try again, or write them yourself." };
  return { ...d, title: w.title, body: w.body, by: "Written by " + (await modelName(a)) };
}

function unwritten(e) {
  switch (e.code) {
    case "unsupported":
      return "The provider of your default model can't write this on its own yet. Pick a Claude model for new threads, or write it yourself.";
    case "unavailable":
      return "No model can write this here: Claude Code isn't installed. Write it yourself, or install Claude Code.";
    case "denied":
      return "Writing needs the models:ask grant, in Settings › Plugins.";
    case "busy":
      return "The last write isn't done yet.";
    case "timeout":
      return "The model didn't answer in time. Try again.";
  }
  return "Couldn't write it: " + short(e.message);
}

// written is the title and body of the model's answer, which may come
// fenced as code.
function written(text) {
  const s = text.indexOf("{");
  const e = text.lastIndexOf("}");
  if (s < 0 || e < s) return null;
  try {
    const w = JSON.parse(text.slice(s, e + 1));
    return typeof w.title === "string" && w.title.trim() && typeof w.body === "string" ? { title: w.title.trim(), body: w.body } : null;
  } catch {
    return null;
  }
}

// modelName is the model's name as the model picker shows it, else its id.
async function modelName(a) {
  const list = await glitched.models.list().catch(() => null);
  const m = list?.models.find((m) => m.provider === a.provider && m.id === a.model);
  if (!m) return a.model;
  return m.family && !m.name.startsWith(m.family) ? m.family + " " + m.name : m.name;
}

// create commits if asked, pushes to the one ref the branch pushes to,
// never forced, then opens the pull request, its body on stdin. A step
// that fails says what was done before it.
async function create(d) {
  const title = d.title.trim();
  if (!title) return { error: "Write a title." };
  const ws = d.workspace;
  const done = [];
  if (d.commit && d.dirty > 0) {
    const add = await git(ws, ["add", "-A"]);
    if (!add.ok) return { error: "git add: " + add.err };
    const commit = await git(ws, ["commit", "-q", "-m", title]);
    if (!commit.ok) return { error: "git commit: " + commit.err };
    done.push("Committed " + (await git(ws, ["rev-parse", "--short", "HEAD"])).out.trim());
  }
  const committed = done.length > 0;
  const { push, noPush } = await target(ws, d.branch);
  if (!push) return { committed, error: said(done, "can't push: " + noPush) };
  const to = push.remote + "/" + push.branch;
  const args = ["push", "--no-follow-tags", ...(push.track ? ["-u"] : []), push.remote, "refs/heads/" + d.branch + ":refs/heads/" + push.branch];
  const pushed = await git(ws, args, 20);
  if (!pushed.ok) return { committed, error: said(done, "push to " + to + " failed: " + (pushed.timedOut ? "it took over 20 seconds" : pushed.err)) };
  done.push("pushed");
  const r = await gh(ws, ["pr", "create", "--repo", d.slug, "--head", push.branch, "--base", d.base, "--title", title, "--body-file", "-", ...(d.draft ? ["--draft"] : [])], d.body, 30);
  if (r.ok) return { url: r.out.trim().split("\n").pop() };
  if (/already exists/i.test(r.err)) return { exists: true };
  return { committed, error: said(done, "gh pr create failed: " + (r.missing ? r.err : ghReason(r))) };
}

// said joins what was done with what failed: "Committed abc1234 and
// pushed; gh pr create failed: …".
function said(done, failed) {
  const s = done.length === 0 ? failed : done.join(" and ") + "; " + failed;
  return s[0].toUpperCase() + s.slice(1);
}

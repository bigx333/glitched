// The pull request of a thread's branch, in the thread's header, read with
// git and gh as you. Each workspace has one Repo, read in one place (read)
// and drawn from in another (header):
//
//   { kind: "hidden" }                       nothing to show
//   { kind: "gh", problem }                  a GitHub remote gh can't answer for: "missing" or "signed-out"
//   { kind: "ready", branch, ref, slug, base, ahead, dirty, pushed }   no pull request yet
//   { kind: "pr", branch, ref, slug, ahead, dirty, pr, stale? }        its pull request
//
// branch, ref and slug are its head: the local branch, the branch it
// pushes to and the GitHub repository that's in. ahead is null when the
// base isn't fetched, dirty when git status failed. pr is { number, title,
// url, state, review, conflicts, checks }, each check { name, status,
// url? }; stale says why the last read failed.
//
// A Target is where a push of a branch goes, as Create shows and pushes it:
// { remote, url, ref, slug, track }, or { why } there's none.

type PRState = "open" | "draft" | "merged" | "closed";
type ReviewState = "approved" | "changes" | "required";
type CheckStatus = "failed" | "running" | "waiting" | "passed" | "skipped";

interface Check {
  name: string;
  status: CheckStatus;
  url?: string;
}

interface PR {
  number: number;
  title: string;
  url: string;
  state: PRState;
  review: ReviewState | null;
  conflicts: boolean | null;
  checks: Check[];
}

interface Head {
  branch: string;
  ref: string;
  slug: string;
}

type Repo =
  | { kind: "hidden" }
  | { kind: "gh"; problem: "missing" | "signed-out" }
  | { kind: "ready"; branch: string; ref: string; slug: string; base: string; ahead: number | null; dirty: number | null; pushed: boolean }
  | { kind: "pr"; branch: string; ref: string; slug: string; ahead: number | null; dirty: number | null; pr: PR; stale?: string };

interface Target {
  remote: string;
  url: string;
  ref: string;
  slug: string;
  track: boolean;
}

type TargetResult = Target | { why: string };

interface Dialog {
  key: string;
  workspace: string;
  branch: string;
  target: Target;
  base: string;
  pushed: boolean;
  ahead: number | null;
  dirty: number | null;
  title: string;
  body: string;
  draft: boolean;
  commit: boolean;
  by: string | null;
  error: string | null;
}

interface ModelInfo {
  provider: string;
  id: string;
  family: string;
  name: string;
}

interface Shown {
  key: string;
  workspace: string;
}

interface State {
  shown: Shown | null;
  repos: Record<string, { repo: Repo; at: number }>;
  bases: Record<string, string>;
  models: ModelInfo[] | null;
  open: string | null;
  passed: boolean;
  note: string | null;
  dialog: Dialog | null;
}

interface GitResult {
  ok: boolean;
  out: string;
  err: string;
}

interface GHResult {
  ok: boolean;
  out?: string;
  err: string;
  timedOut?: boolean;
  missing?: boolean;
}

interface RollupItem {
  name?: string;
  context?: string;
  status?: string;
  conclusion?: string;
  state?: string;
  detailsUrl?: string;
  targetUrl?: string;
}

interface RawPR {
  number: number;
  title: string;
  url: string;
  state: string;
  isDraft: boolean;
  reviewDecision?: string;
  mergeable?: string;
  statusCheckRollup?: RollupItem[];
  headRefName: string;
  headRepository?: { name?: string };
  headRepositoryOwner?: { login?: string };
  baseRefName?: string;
}

function errMessage(e: unknown): string {
  return e instanceof Error ? e.message : String(e);
}

function errCode(e: unknown): string | undefined {
  return typeof e === "object" && e !== null && "code" in e && typeof e.code === "string" ? e.code : undefined;
}

const GH_ENV: Record<string, string> = { GH_PROMPT_DISABLED: "1" };
const HIDDEN: Repo = { kind: "hidden" };
const FRESH = 10 * 1000;
const OPEN_EVERY = 55 * 1000;
const IDLE_EVERY = 5 * 60 * 1000 - 5 * 1000;
const MAX_ROWS = 12;
const PR_FIELDS = "number,title,url,state,isDraft,reviewDecision,mergeable,statusCheckRollup,headRefName,headRepository,headRepositoryOwner,baseRefName";
const PAGES = [10, 100];
const MAX_TITLE = 256;
const MAX_BODY = 30000;

// The app waits 30 s for an update to answer, and drops a late answer
// while what it ran goes on, so each update runs on BUDGET: every run takes
// at most what is left of it, and Create keeps RECHECK of it to look again
// for the pull request it may have opened.
const BUDGET = 27 * 1000;
const RECHECK = 8;
let deadline = 0;
const LATE: GHResult = { ok: false, out: "", err: "it ran out of time; try again", timedOut: true };

// Late is a git run that timed out, or had no time left to run: what it
// would have read isn't known.
class Late extends Error {}

function left(): number {
  return (deadline - Date.now()) / 1000;
}

const SYSTEM =
  'You write GitHub pull request titles and descriptions. Answer with one JSON object and nothing else: {"title": "...", "body": "..."}. ' +
  'The title is one line under 72 characters, in the imperative. The body is GitHub Markdown: "## Summary", a few bullets of what changed and why, ' +
  'then "## Testing", how the change was checked, or "Not stated." when the changes don\'t say.';

export function init(): Update<State> {
  return { state: { shown: null, repos: {}, bases: {}, models: null, open: null, passed: false, note: null, dialog: null } };
}

// header draws a copy from its workspace's Repo, and nothing until one is read.
export function place({ state, items }: PlaceArgs<State, ThreadRecord>): (Widget | null)[] {
  return items.map(({ key, context, compact }) => {
    const repo = context?.local && context.workspace ? state.repos[context.workspace]?.repo : null;
    return repo ? header(repo, key, state, Boolean(compact)) : null;
  });
}

// header is a copy's pill; compact, as a narrow header asks, it keeps to
// its icon, and its label names it to assistive tools.
function header(repo: Repo, key: string, state: State, compact: boolean): Widget | null {
  const open = state.open === key;
  const popover = (width: number, children: Widget[]) => ({
    active: open,
    open,
    onClick: "popover.toggle",
    onClose: "popover.close",
    popover: { type: "column" as const, width, padding: 12, gap: 10, children },
  });
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
          ...(compact ? [] : [{ type: "text" as const, text: "PR", color: "$warning" as const }]),
        ],
        ...popover(300, ghProblem(repo.problem)),
      };
    case "ready": {
      if (repo.ahead === 0 && repo.dirty === 0) return null;
      const button: Widget = { type: "button", id: "create", style: "outline", icon: "pull-request", label: "Create PR", tooltip: "Create a pull request", compact, onClick: "create.open" };
      const d = state.dialog;
      return d && d.key === key ? { type: "row", gap: 8, children: [button, createDialog(d)] } : button;
    }
    case "pr": {
      const pr = repo.pr;
      const mark = checksMark(pr.checks);
      const live = pr.state === "open" || pr.state === "draft";
      return {
        type: "button",
        id: "pr",
        style: "ghost",
        label: "Pull request #" + pr.number,
        tooltip: "Pull request #" + pr.number + ": " + STATES[pr.state].label + (mark ? ", checks " + mark.word : ""),
        gap: 4,
        children: [
          { type: "icon", name: "pull-request", color: live && mark ? mark.color : STATES[pr.state].color },
          ...(compact ? [] : [{ type: "text" as const, text: "#" + pr.number }]),
        ],
        ...popover(360, prPopover(repo, state)),
      };
    }
  }
  return null;
}

const STATES: Record<PRState, { label: string; badge: "success" | "default" | "accent" | "danger"; color: Color }> = {
  open: { label: "Open", badge: "success", color: "$success" },
  draft: { label: "Draft", badge: "default", color: "$text.muted" },
  merged: { label: "Merged", badge: "accent", color: "$accent" },
  closed: { label: "Closed", badge: "danger", color: "$danger" },
};

const REVIEWS: Record<ReviewState, string> = { approved: "Approved", changes: "Changes requested", required: "Awaiting review" };

const CHECKS: Record<CheckStatus, { label: string; icon: string; color: Color }> = {
  failed: { label: "Failed", icon: "close", color: "$danger" },
  running: { label: "Running", icon: "clock-circle", color: "$warning" },
  waiting: { label: "Waiting", icon: "clock-circle", color: "$text.muted" },
  passed: { label: "Passed", icon: "check-circle", color: "$success" },
  skipped: { label: "Skipped", icon: "check", color: "$text.muted" },
};

// checksMark is what a pull request's checks come to, which tints its
// pill while it's open.
function checksMark(checks: Check[]): { word: string; color: Color } | null {
  if (checks.some((c) => c.status === "failed")) return { word: "failing", color: "$danger" };
  if (checks.some((c) => c.status === "running" || c.status === "waiting")) return { word: "running", color: "$warning" };
  if (checks.some((c) => c.status === "passed")) return { word: "passed", color: "$success" };
  return null;
}

function ghProblem(problem: "missing" | "signed-out"): Widget[] {
  const text =
    problem === "missing"
      ? "Install the GitHub CLI to see and open pull requests: `brew install gh`, then `gh auth login`."
      : "Sign gh in to GitHub: run `gh auth login` in a terminal, then Refresh.";
  return [
    { type: "markdown", text },
    { type: "row", justify: "end", children: [{ type: "button", id: "refresh", label: "Refresh", onClick: "refresh" }] },
  ];
}

function checkRow(c: Check): Widget {
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

function rows(checks: Check[], more: string): Widget[] {
  const out = checks.slice(0, MAX_ROWS).map(checkRow);
  if (checks.length > MAX_ROWS) out.push({ type: "text", text: "and " + (checks.length - MAX_ROWS) + " more " + more, style: "muted" });
  return out;
}

function prPopover(repo: Extract<Repo, { kind: "pr" }>, state: State): Widget[] {
  const pr = repo.pr;
  const badges: Widget[] = [{ type: "badge", text: STATES[pr.state].label, style: STATES[pr.state].badge }];
  if (pr.review) badges.push({ type: "badge", text: REVIEWS[pr.review], style: "pill" });
  if (pr.conflicts) badges.push({ type: "badge", text: "Has conflicts", style: "warning" });
  const attention = pr.checks.filter((c) => c.status === "failed" || c.status === "running" || c.status === "waiting");
  attention.sort((a, b) => (a.status === "failed" ? 0 : 1) - (b.status === "failed" ? 0 : 1));
  const done = pr.checks.filter((c) => c.status === "passed" || c.status === "skipped");
  const checks: Widget[] = [];
  if (pr.checks.length === 0) checks.push({ type: "text", text: "No checks reported.", style: "muted" });
  checks.push(...rows(attention, "running or failed"));
  if (done.length > 0) {
    checks.push({ type: "row", id: "passed", onClick: "passed.toggle", children: [{ type: "text", text: (state.passed ? "Hide " : "Show ") + done.length + " passed", style: "muted" }] });
    if (state.passed) checks.push(...rows(done, "passed"));
  }
  const buttons: Widget[] = [
    { type: "button", id: "github", label: "Open on GitHub", onClick: "github" },
    { type: "button", id: "copy", label: "Copy link", onClick: "copy" },
    { type: "button", id: "refresh", label: "Refresh", onClick: "refresh" },
  ];
  if (pr.state === "draft") buttons.push({ type: "button", id: "ready", label: "Ready for review", progress: true, onClick: "ready" });
  if (pr.checks.some((c) => c.status === "failed")) buttons.push({ type: "button", id: "fix", label: "Ask the agent to fix", style: "primary", onClick: "fix" });
  const lines: Widget[] = [];
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

function createDialog(d: Dialog): Widget {
  const files = d.dirty === 1 ? "1 changed file" : d.dirty + " changed files";
  const t = d.target;
  const to = t.remote + "/" + t.ref;
  const where = d.pushed && !d.dirty ? d.branch + " is pushed to " + to + "." : "Pushes " + d.branch + " to " + to + (t.track ? ", which it starts tracking." : ".");
  const unknown =
    (d.ahead === null ? " " + t.remote + "/" + d.base + " isn't fetched here, so the commits it adds aren't known." : "") +
    (d.dirty === null ? " git couldn't say which files changed, so nothing is committed." : "");
  return {
    type: "dialog",
    id: "create-dialog",
    title: "Create pull request",
    open: true,
    onClose: "create.cancel",
    children: [
      { type: "text", id: "what", text: "Opens a pull request on " + t.slug + " from " + d.branch + " into the base below. " + where + unknown, style: "muted" },
      { type: "input", id: "title", label: "Title", value: d.title },
      { type: "input", id: "body", label: "Description", value: d.body, multiline: true },
      { type: "input", id: "base", label: "Base", value: d.base },
      { type: "toggle", id: "draft", label: "Open as draft", value: d.draft },
      ...(d.dirty !== null && d.dirty > 0 ? [{ type: "toggle" as const, id: "commit", label: "Commit " + files + " first", value: d.commit }] : []),
      {
        type: "row",
        id: "writing",
        gap: 8,
        align: "center",
        children: [
          { type: "button", id: "write", label: "Write with AI", progress: true, onClick: "create.write" },
          ...(d.by ? [{ type: "text" as const, id: "by", text: d.by, style: "muted" as const }] : []),
        ],
      },
      ...(d.error ? [{ type: "text" as const, id: "error", text: d.error, style: "error" as const, selectable: true }] : []),
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

// git runs git within the update's time, and throws Late for a run that
// timed out or had none left.
async function git(cwd: string, args: string[], most = 5): Promise<GitResult> {
  const timeout = Math.min(most, left());
  if (timeout < 0.5) throw new Late(LATE.err);
  const r = await glitched.run("git", args, { cwd, timeout });
  if (r.status === -1) throw new Late("git " + args[0] + " didn't finish in time");
  return { ok: r.status === 0, out: r.stdout, err: reason(r.stderr || r.stdout, r.status) };
}

// gh answers missing when it isn't installed, as glitched.run says.
async function gh(cwd: string, args: string[], stdin = "", most = 15): Promise<GHResult> {
  const timeout = Math.min(most, left());
  if (timeout < 0.5) return LATE;
  try {
    const r = await glitched.run("gh", args, { cwd, timeout, stdin, env: GH_ENV });
    return { ok: r.status === 0, out: r.stdout, err: (r.stderr || r.stdout).trim(), timedOut: r.status === -1 };
  } catch (e) {
    const msg = errMessage(e);
    if (/isn't installed/.test(msg)) return { ok: false, missing: true, err: msg };
    throw e;
  }
}

// reason is the line of git's output that says why it failed: not the
// "To <remote>" a push starts with, nor its hints. A push the remote
// turned down says why it did.
function reason(output: string, status: number): string {
  const lines = output.split(/[\r\n]+/).map((l) => l.trim()).filter(Boolean);
  const rejected = lines.map((l) => /^! \[(remote )?rejected\] .*\((.+)\)$/.exec(l)).find(Boolean);
  let why: string;
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

function short(s: string): string {
  const chars = [...s];
  return chars.length > 200 ? chars.slice(0, 199).join("") + "…" : s;
}

// ghReason is why a gh run failed, in a line.
function ghReason(r: GHResult): string {
  if (r === LATE) return LATE.err;
  if (r.timedOut) return "GitHub didn't answer in time";
  if (/rate limit/i.test(r.err)) return "GitHub's rate limit was reached; try again in a few minutes";
  const line = r.err.split(/[\r\n]+/).map((l) => l.trim()).find(Boolean);
  return short(line || "gh failed");
}

function signedOut(err: string): boolean {
  return /not logged in|gh auth login|authenticat|bad credentials/i.test(err);
}

async function config(cwd: string, key: string): Promise<string> {
  const r = await git(cwd, ["config", "--get", key]);
  return r.ok ? r.out.trim() : "";
}

// checkedOut is the branch HEAD is on, "" when it's detached.
async function checkedOut(ws: string): Promise<string> {
  const r = await git(ws, ["symbolic-ref", "--quiet", "--short", "HEAD"]);
  return r.ok ? r.out.trim() : "";
}

// pushTarget is the Target of branch: the one ref a push updates, the
// branch's upstream or origin's branch of the same name when it tracks
// none, and the remote's one push URL as git resolves it, rewrites
// included. The push names the ref in full, with no +, so the repository's
// push config (pushRemote, push.default, remote.*.push, a forced refspec)
// can't send it anywhere else, or force it.
async function pushTarget(ws: string, branch: string): Promise<TargetResult> {
  let remote = await config(ws, "branch." + branch + ".remote");
  const merge = await config(ws, "branch." + branch + ".merge");
  if (remote === ".") return { why: "the branch tracks a local branch" };
  let ref = merge.startsWith("refs/heads/") ? merge.slice("refs/heads/".length) : "";
  const track = !(remote && ref);
  if (track) {
    const remotes = await git(ws, ["remote"]);
    if (!remotes.ok || !remotes.out.split("\n").includes("origin")) return { why: "the repository has no origin" };
    [remote, ref] = ["origin", branch];
  }
  const r = await git(ws, ["remote", "get-url", "--push", "--all", remote]);
  const urls = r.ok ? r.out.split("\n").map((u) => u.trim()).filter(Boolean) : [];
  if (urls.length > 1) return { why: remote + " pushes to " + urls.length + " places, and Create pushes to one" };
  const slug = urls.length === 1 ? github(urls[0]) : null;
  if (!slug) return { why: remote + " doesn't push to github.com" };
  return { remote, url: urls[0], ref, slug, track };
}

// drift says how where d's branch pushes now differs from the Target its
// dialog showed, "" when it doesn't.
async function drift(d: Dialog): Promise<string> {
  const was = d.target;
  const now = await pushTarget(d.workspace, d.branch);
  if ("why" in now) return "can't push: " + now.why;
  if (now.slug !== was.slug) return "can't push: " + now.remote + " pushes to " + now.slug + " now, not " + was.slug;
  if (now.remote !== was.remote || now.ref !== was.ref) return "can't push: " + d.branch + " pushes to " + now.remote + "/" + now.ref + " now, not " + was.remote + "/" + was.ref;
  if (now.url !== was.url) return "can't push: " + now.remote + "'s push URL changed";
  return "";
}

// github is the owner/name of a github.com remote, null for any other.
function github(url: string): string | null {
  const m = /^(?:https:\/\/(?:[^@/]+@)?github\.com\/|ssh:\/\/git@github\.com\/|git@github\.com:)([\w.-]+)\/([\w.-]+?)(?:\.git)?\/?$/.exec(url.trim());
  return m ? m[1] + "/" + m[2] : null;
}

// Unread is a read GitHub failed, of the head it got to.
class Unread extends Error {
  head: Head;
  constructor(message: string, head: Head) {
    super(message);
    this.head = head;
  }
}

// sameHead is whether two Repos, or Unread's head, are of one head.
function sameHead(a: Head, b: Head): boolean {
  return a.branch === b.branch && a.ref === b.ref && a.slug === b.slug;
}

// read is the workspace's Repo, from the first answer that settles it. A
// workspace git can't run in, as one whose folder is gone, shows nothing.
// A read GitHub fails throws Unread, one git times out in Late; one gh is
// signed out of says so. The last read's pull request, was, stays till a
// read of it by number says it's gone or another head's: a list may leave
// it out while GitHub's search catches up.
async function read(ws: string, bases: Record<string, string>, was: Repo | undefined): Promise<Repo> {
  let branch: string;
  try {
    branch = await checkedOut(ws);
  } catch (e) {
    if (errCode(e) === "refused") return HIDDEN;
    throw e;
  }
  if (!branch) return HIDDEN;
  const t = await pushTarget(ws, branch);
  if ("why" in t) return HIDDEN;
  const { ref, slug } = t;
  const fetched = await git(ws, ["remote", "get-url", t.remote]);
  if (fetched.ok && fetched.out.trim() === t.url) {
    const remoteHead = await git(ws, ["symbolic-ref", "--quiet", "--short", "refs/remotes/" + t.remote + "/HEAD"]);
    if (remoteHead.ok && remoteHead.out.trim() === t.remote + "/" + ref) return HIDDEN;
  }
  const failed = (r: GHResult): Repo => {
    if (signedOut(r.err)) return { kind: "gh", problem: "signed-out" };
    throw new Unread(ghReason(r), { branch, ref, slug });
  };
  const auth = await gh(ws, ["auth", "status", "--active", "--hostname", "github.com"]);
  if (auth.missing) return { kind: "gh", problem: "missing" };
  if (!auth.ok) return failed(auth);
  if (!bases[slug]) {
    const r = await gh(ws, ["repo", "view", slug, "--json", "defaultBranchRef"]);
    if (!r.ok) return failed(r);
    const parsed = JSON.parse(r.out ?? "{}") as { defaultBranchRef?: { name?: string } };
    const name = parsed.defaultBranchRef?.name;
    if (!name) throw new Unread("GitHub didn't say the repository's default branch", { branch, ref, slug });
    bases[slug] = name;
  }
  const base = bases[slug];
  if (ref === base) return HIDDEN;
  let pr: PR | null = null;
  lists: for (const state of ["open", "all"]) {
    for (const limit of PAGES) {
      const list = await gh(ws, ["pr", "list", "--repo", slug, "--head", ref, "--state", state, "--limit", String(limit), "--json", PR_FIELDS]);
      if (!list.ok) return failed(list);
      const prs = (JSON.parse(list.out ?? "null") as RawPR[] | null) ?? [];
      pr = pick(prs, ref, slug);
      if (pr) break lists;
      if (prs.length < limit) break;
    }
  }
  if (!pr && was?.kind === "pr" && sameHead(was, { branch, ref, slug })) {
    const view = await gh(ws, ["pr", "view", String(was.pr.number), "--repo", slug, "--json", PR_FIELDS]);
    if (!view.ok && !/Could not resolve to a PullRequest/i.test(view.err)) return failed(view);
    pr = view.ok ? pick([JSON.parse(view.out ?? "{}") as RawPR], ref, slug) : null;
  }
  const ahead = await count(ws, t.remote, base);
  const status = await git(ws, ["status", "--porcelain", "--untracked-files=all"]);
  const dirty = status.ok ? status.out.split("\n").filter(Boolean).length : null;
  if (pr) return { kind: "pr", branch, ref, slug, ahead, dirty, pr };
  const pushedTo = await git(ws, ["rev-parse", "--verify", "-q", "refs/remotes/" + t.remote + "/" + ref]);
  const local = await git(ws, ["rev-parse", "HEAD"]);
  return { kind: "ready", branch, ref, slug, base, ahead, dirty, pushed: pushedTo.ok && pushedTo.out.trim() === local.out.trim() };
}

// count is how many commits HEAD has that the base hasn't, by the remote's
// base, else a local one; null without either.
async function count(ws: string, remote: string, base: string): Promise<number | null> {
  for (const ref of ["refs/remotes/" + remote + "/" + base, "refs/heads/" + base]) {
    const r = await git(ws, ["rev-list", "--count", ref + "..HEAD"]);
    if (r.ok) return Number(r.out.trim());
  }
  return null;
}

const REVIEW_MAP: Record<string, ReviewState> = {
  APPROVED: "approved",
  CHANGES_REQUESTED: "changes",
  REVIEW_REQUIRED: "required",
};

// pick is the pull request of the branch head of the repository slug, not
// a fork's of the same name: an open one first, then the newest.
function pick(prs: RawPR[], head: string, slug: string): PR | null {
  const from = (p: RawPR) => (p.headRepositoryOwner?.login + "/" + p.headRepository?.name).toLowerCase();
  const mine = prs.filter((p) => p.headRefName === head && from(p) === slug.toLowerCase());
  mine.sort((a, b) => (a.state === "OPEN" ? 0 : 1) - (b.state === "OPEN" ? 0 : 1) || b.number - a.number);
  const p = mine[0];
  if (!p) return null;
  return {
    number: p.number,
    title: p.title,
    url: p.url,
    state: p.state === "OPEN" ? (p.isDraft ? "draft" : "open") : p.state === "MERGED" ? "merged" : "closed",
    review: (p.reviewDecision && REVIEW_MAP[p.reviewDecision]) || null,
    conflicts: p.mergeable === "CONFLICTING" ? true : p.mergeable === "MERGEABLE" ? false : null,
    checks: (p.statusCheckRollup || []).map(check),
  };
}

const FAILED = ["FAILURE", "ERROR", "TIMED_OUT", "CANCELLED", "ACTION_REQUIRED", "STARTUP_FAILURE"];
const SKIPPED = ["SKIPPED", "NEUTRAL"];

// check is one of a rollup's: a CheckRun's status and conclusion, or a
// StatusContext's state. Only SUCCESS passes: STALE, an empty conclusion
// on a finished run and one GitHub adds later wait.
function check(c: RollupItem): Check {
  const done = c.conclusion || (c.state && c.state !== "PENDING" && c.state !== "EXPECTED" ? c.state : "");
  let status: CheckStatus;
  if (FAILED.includes(done)) status = "failed";
  else if (done === "SUCCESS") status = "passed";
  else if (SKIPPED.includes(done)) status = "skipped";
  else if (c.status === "IN_PROGRESS" || c.state === "PENDING") status = "running";
  else status = "waiting";
  const url = c.detailsUrl || c.targetUrl || "";
  return { name: c.name || c.context || "check", status, ...(url.startsWith("https://") ? { url } : {}) };
}

// refresh reads the shown workspace again. A failed read keeps the last
// Repo, a pull request saying why it couldn't refresh, while its head is
// still the workspace's. One git didn't answer keeps what it has, due to be
// read at the next timer; so does too little of the update's time left to
// read. A popover open on a header that changes kind, or has none, closes.
// A workspace that can open a pull request reads the models' names too,
// once, for Write with AI to say which wrote it.
async function refresh(state: State, shown: Shown | null): Promise<Update<State>> {
  if (!shown || left() < 3) return { state };
  const ws = shown.workspace;
  const bases = { ...state.bases };
  const was = state.repos[ws]?.repo;
  let repo: Repo;
  try {
    repo = await read(ws, bases, was);
  } catch (e) {
    if (e instanceof Late) return { state: was ? { ...state, repos: { ...state.repos, [ws]: { repo: was, at: 0 } } } : state };
    if (!(e instanceof Unread)) throw e;
    const staleHead = was && (was.kind === "ready" || was.kind === "pr") ? was : null;
    repo = !was || !staleHead || !sameHead(staleHead, e.head) ? HIDDEN : was.kind === "pr" ? { ...was, stale: e.message } : was;
  }
  const models = repo.kind === "ready" && !state.models ? await names() : state.models;
  const popover = repo.kind === was?.kind && (repo.kind === "pr" || repo.kind === "gh");
  const open = state.open === shown.key && !popover ? null : state.open;
  return { state: { ...state, open, bases, models, repos: { ...state.repos, [ws]: { repo, at: Date.now() } } } };
}

// names is the models the model picker lists, as it names them, null when
// the app doesn't say.
async function names(): Promise<ModelInfo[] | null> {
  const list = await glitched.models.list().catch(() => null);
  return list?.models?.map((m) => ({ provider: m.provider, id: m.id, family: m.family, name: m.family && !m.name.startsWith(m.family) ? m.family + " " + m.name : m.name })) ?? null;
}

function shownBy(key: string, context?: ThreadRecord): Shown | null {
  return context?.local && context.workspace ? { key, workspace: context.workspace } : null;
}

// due is whether the shown workspace's read is old: a minute with a pull
// request open, five otherwise.
function due(state: State): boolean {
  const r = state.shown && state.repos[state.shown.workspace];
  if (!r) return state.shown != null;
  const open = r.repo.kind === "pr" && (r.repo.pr.state === "open" || r.repo.pr.state === "draft");
  return Date.now() - r.at >= (open ? OPEN_EVERY : IDLE_EVERY);
}

// update answers what happened. A call the app refuses, as a grant taken
// back, is told in a toast.
export async function update(args: UpdateArgs<State, ThreadRecord>): Promise<Update<State>> {
  deadline = Date.now() + BUDGET;
  try {
    return await act(args);
  } catch (e) {
    glitched.toast("Pull requests", errMessage(e)).catch(() => {});
    return {};
  }
}

async function act({ state, event, values }: UpdateArgs<State, ThreadRecord>): Promise<Update<State>> {
  if (event.kind === "timer") return due(state) ? refresh(state, state.shown) : {};
  if (event.kind === "app") {
    switch (event.type) {
      case "place.shown": {
        const shown = shownBy(event.data.key, event.data.context);
        if (shown && state.shown?.key === shown.key && state.shown.workspace === shown.workspace) return {};
        const next: State = { ...state, shown, note: null };
        const r = shown && state.repos[shown.workspace];
        return !shown || (r && Date.now() - r.at < FRESH) ? { state: next } : refresh(next, shown);
      }
      case "place.hidden": {
        const key = event.data.key;
        return { state: { ...state, shown: state.shown?.key === key ? null : state.shown, open: state.open === key ? null : state.open } };
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
  const repo = shown ? state.repos[shown.workspace]?.repo : undefined;
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
      if (!shown || repo?.kind !== "pr") return {};
      if (!(await still(shown.workspace, repo))) return moved(state, shown);
      const r = await gh(shown.workspace, ["pr", "ready", String(repo.pr.number), "--repo", repo.slug]);
      if (!r.ok) return { state: { ...state, note: "gh pr ready failed: " + (r.missing ? r.err : ghReason(r)) } };
      return refresh({ ...state, note: null }, shown);
    }
    case "fix":
      if (!shown || repo?.kind !== "pr") return {};
      if (!(await still(shown.workspace, repo))) return moved(state, shown);
      await glitched.composer.draft(fixPrompt(repo.pr), { thread: key });
      return { state: { ...state, open: null } };
    case "create.open": {
      if (!shown || repo?.kind !== "ready") return {};
      const d = await draft(key, shown.workspace, repo, ctx?.title);
      return "why" in d ? refresh(state, shown) : { state: { ...state, dialog: d } };
    }
    case "create.cancel":
      return { state: { ...state, dialog: null } };
    case "create.write": {
      const d = edited(state.dialog, values);
      if (!d) return {};
      try {
        return { state: { ...state, dialog: await write(d, ctx?.title, state.models ?? []) } };
      } catch (e) {
        if (!(e instanceof Late)) throw e;
        return { state: { ...state, dialog: { ...d, by: null, error: "Reading the changes took too long: " + e.message + ". Try again." } } };
      }
    }
    case "create.go": {
      const d = edited(state.dialog, values);
      if (!d) return {};
      const done = await create(d);
      if ("error" in done) return { state: { ...state, dialog: { ...d, error: done.error, ...(done.committed ? { dirty: 0, commit: false } : {}) } } };
      if (done.url || done.tracking) {
        const saidText = [done.url, done.tracking ? done.tracking[0].toUpperCase() + done.tracking.slice(1) : ""].filter(Boolean).join("\n");
        glitched.toast(done.url ? "Opened a pull request" : "Pull request", saidText).catch(() => {});
      }
      const t = d.target;
      const newRepo: Repo = { kind: "pr", branch: d.branch, ref: t.ref, slug: t.slug, ahead: d.ahead, dirty: d.commit ? 0 : d.dirty, pr: done.pr };
      return { state: { ...state, dialog: null, repos: { ...state.repos, [d.workspace]: { repo: newRepo, at: Date.now() } } } };
    }
  }
  return {};
}

// still is whether the workspace's head is repo's yet: the branch checked
// out, the ref it pushes to and the repository that's in.
async function still(ws: string, repo: Head): Promise<boolean> {
  const branch = await checkedOut(ws);
  if (branch === "") return false;
  const target = await pushTarget(ws, branch);
  if ("why" in target) return false;
  return sameHead(repo, { branch, ref: target.ref, slug: target.slug });
}

// moved reads a workspace again whose head isn't its pull request's any
// more, saying so.
function moved(state: State, shown: Shown): Promise<Update<State>> {
  return refresh({ ...state, note: "The workspace's branch isn't this pull request's any more." }, shown);
}

function fixPrompt(pr: PR): string {
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
function edited(d: Dialog | null, values: Values | undefined): Dialog | null {
  if (!d) return null;
  const v: Partial<Values> = values || {};
  return {
    ...d,
    title: String(v.title ?? d.title),
    body: String(v.body ?? d.body),
    base: String(v.base ?? d.base).trim() || d.base,
    draft: typeof v.draft === "boolean" ? v.draft : d.draft,
    commit: typeof v.commit === "boolean" ? v.commit : d.commit,
    error: null,
  };
}

// draft is the dialog as it opens: the only commit's subject as the
// title, else the thread's title, and the commits under Summary. It holds
// the Target it shows, which Create pushes to or to nothing. why says why
// there's none to draw.
async function draft(key: string, ws: string, repo: Extract<Repo, { kind: "ready" }>, threadTitle?: string): Promise<Dialog | { why: string }> {
  const target = await pushTarget(ws, repo.branch);
  if ("why" in target || !sameHead(repo, { branch: repo.branch, ...target })) return { why: "why" in target ? target.why : "the branch moved" };
  const log = await git(ws, ["log", "--format=%s", "refs/remotes/" + target.remote + "/" + repo.base + "..HEAD"]);
  const subjects = log.ok ? log.out.split("\n").filter(Boolean) : [];
  const title = subjects.length === 1 ? subjects[0] : threadTitle || repo.branch;
  const body = "## Summary\n\n" + (subjects.length ? subjects : [title]).map((s) => "- " + s).join("\n") + "\n";
  return {
    key,
    workspace: ws,
    branch: repo.branch,
    target,
    base: repo.base,
    pushed: repo.pushed,
    ahead: repo.ahead,
    dirty: repo.dirty,
    title,
    body,
    draft: false,
    commit: repo.dirty !== null && repo.dirty > 0,
    by: null,
    error: null,
  };
}

function cut(s: string, n: number): string {
  return s.length > n ? s.slice(0, n) + "\n[cut]" : s;
}

// write asks a model for the title and description, from what the pull
// request would hold: its commits, its files and its diff from where it
// leaves the base, uncommitted changes included. models are the names the
// model picker gave the last refresh.
async function write(d: Dialog, threadTitle: string | undefined, models: ModelInfo[]): Promise<Dialog> {
  const mb = await git(d.workspace, ["merge-base", "refs/remotes/" + d.target.remote + "/" + d.base, "HEAD"]);
  const from = mb.ok ? mb.out.trim() : "HEAD";
  const log = mb.ok ? await git(d.workspace, ["log", "--format=%s%n%n%b", from + "..HEAD"]) : { out: "" };
  const stat = await git(d.workspace, ["diff", "--stat", from]);
  const patch = await git(d.workspace, ["diff", from], 10);
  const status = d.dirty !== 0 ? await git(d.workspace, ["status", "--porcelain", "--untracked-files=all"]) : { out: "" };
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
  const failed = (error: string): Dialog => ({ ...d, by: null, error });
  const wait = left() - 1;
  if (wait < 1) return failed("Reading the changes took the time there was to ask a model. Try again.");
  let a: { text: string; provider: string; model: string };
  try {
    a = await glitched.models.ask(prompt, { system: SYSTEM, timeout: wait });
  } catch (e) {
    return failed(unwritten(e));
  }
  const w = written(a.text);
  if (!w) return failed("The model's answer wasn't a title and description. Try again, or write them yourself.");
  const over = bytes(w.title) > MAX_TITLE ? "title is over " + MAX_TITLE : bytes(w.body) > MAX_BODY ? "description is over " + MAX_BODY : "";
  if (over) return failed("The model's " + over + " bytes, so yours are kept. Try again, or write them yourself.");
  return { ...d, title: w.title, body: w.body, by: "Written by " + modelName(a, models) };
}

// bytes is how long s is in UTF-8, as GitHub and the app's fields count it.
function bytes(s: string): number {
  let n = 0;
  for (const ch of s) {
    const c = ch.codePointAt(0) ?? 0;
    n += c < 0x80 ? 1 : c < 0x800 ? 2 : c < 0x10000 ? 3 : 4;
  }
  return n;
}

function unwritten(e: unknown): string {
  switch (errCode(e)) {
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
  return "Couldn't write it: " + short(errMessage(e));
}

// written is the title and body of the model's answer, which may come
// fenced as code.
function written(text: string): { title: string; body: string } | null {
  const s = text.indexOf("{");
  const e = text.lastIndexOf("}");
  if (s < 0 || e < s) return null;
  try {
    const w = JSON.parse(text.slice(s, e + 1)) as { title?: unknown; body?: unknown };
    return typeof w.title === "string" && w.title.trim() && typeof w.body === "string" ? { title: w.title.trim(), body: w.body } : null;
  } catch {
    return null;
  }
}

// modelName is the model's name as the model picker shows it, else its
// provider's family and its id.
function modelName(a: { provider: string; model: string }, models: ModelInfo[]): string {
  const m = models.find((x) => x.provider === a.provider && x.id === a.model);
  if (m) return m.name;
  return (models.find((x) => x.provider === a.provider)?.family || a.provider) + " (" + a.model + ")";
}

type CreateResult = { url: string; tracking: string; pr: PR } | { error: string; committed?: boolean };

// create commits if asked, on the dialog's branch only, pushes that one
// commit by the remote's name, so git resolves its push URL as the dialog's
// read did, never forced and with no submodules, then opens
// the pull request, its body on stdin. A step that fails says what was
// done before it, each step done as soon as git says so. A pull request
// GitHub may have opened, as when gh's answer is lost, is looked for; pr
// is the one opened.
async function create(d: Dialog): Promise<CreateResult> {
  const title = d.title.trim();
  if (!title) return { error: "Write a title." };
  const ws = d.workspace;
  const t = d.target;
  const to = t.remote + "/" + t.ref;
  const done: string[] = [];
  let committed = false;
  const away = async () => {
    const now = await checkedOut(ws);
    return now === d.branch ? "" : "the workspace is on " + (now || "no branch") + " now, not " + d.branch;
  };
  try {
    let why = (await away()) || (await drift(d));
    if (why) return { error: said([], why) };
    let sha: string;
    if (d.commit && d.dirty !== null && d.dirty > 0) {
      const add = await git(ws, ["add", "-A"]);
      if (!add.ok) return { error: "git add: " + add.err };
      why = await away();
      if (why) return { error: said([], why + "; the changes are staged, not committed") };
      const commit = await git(ws, ["commit", "-q", "-m", title]);
      if (!commit.ok) return { error: "git commit: " + commit.err };
      committed = true;
      done.push("Committed");
      sha = (await git(ws, ["rev-parse", "HEAD"])).out.trim();
      done[0] = "Committed " + sha.slice(0, 7);
    } else {
      sha = (await git(ws, ["rev-parse", "--verify", "-q", "refs/heads/" + d.branch])).out.trim();
    }
    why = await drift(d);
    if (why) return { committed, error: said(done, why) };
    const tip = (await git(ws, ["rev-parse", "--verify", "-q", "refs/heads/" + d.branch])).out.trim();
    if (!sha || tip !== sha) return { committed, error: said(done, d.branch + " moved to " + (tip.slice(0, 7) || "nothing") + " meanwhile, so nothing was pushed") };
    const pushed = await git(ws, ["push", "--no-follow-tags", "--no-recurse-submodules", t.remote, sha + ":refs/heads/" + t.ref], 20);
    if (!pushed.ok) return { committed, error: said(done, "push to " + to + " failed: " + pushed.err) };
    done.push("pushed");
    const tracking = await follow(d, sha);
    if (tracking) done[done.length - 1] = "pushed, though " + tracking;
    const wait = Math.min(30, left() - RECHECK);
    if (wait < 1) return { committed, error: said(done, "the time to open the pull request ran out; try again") };
    const r = await gh(ws, ["pr", "create", "--repo", t.slug, "--head", t.ref, "--base", d.base, "--title", title, "--body-file", "-", ...(d.draft ? ["--draft"] : [])], d.body, wait);
    const url = r.ok ? (r.out ?? "").trim().split("\n").pop() ?? "" : "";
    const number = Number(/\/pull\/(\d+)$/.exec(url)?.[1]);
    if (number) return { url, tracking, pr: { number, title, url, state: d.draft ? "draft" : "open", review: null, conflicts: null, checks: [] } };
    if (!r.ok && (r.missing || refused(r))) return { committed, error: said(done, "gh pr create failed: " + (r.missing ? r.err : ghReason(r))) };
    const pr = await opened(d);
    if (pr) return { url, tracking, pr };
    return { committed, error: said(done, "GitHub didn't say whether it opened the pull request (" + (r.ok ? "gh said " + short(url) : ghReason(r)) + "), and doesn't list one; Refresh, then try again") };
  } catch (e) {
    if (!(e instanceof Late)) throw e;
    return { committed, error: said(done, e.message) };
  }
}

// follow has git show the remote's ref at what was pushed, as a push by
// the remote's name would, and the branch track it if the dialog said it
// starts to. It says what it couldn't do, "" when it did it all.
async function follow(d: Dialog, sha: string): Promise<string> {
  const t = d.target;
  const to = t.remote + "/" + t.ref;
  const not = (why: string) => (t.track ? d.branch + " doesn't track " + to + " yet: " : to + " isn't updated here: ") + why;
  try {
    const noted = await git(d.workspace, ["update-ref", "refs/remotes/" + to, sha]);
    if (!noted.ok) return not(noted.err);
    if (!t.track) return "";
    const up = await git(d.workspace, ["branch", "--set-upstream-to=" + to, d.branch]);
    return up.ok ? "" : not(up.err);
  } catch (e) {
    if (!(e instanceof Late)) throw e;
    return not(e.message);
  }
}

// refused is whether a failed gh pr create is GitHub turning it down, as
// against a failure that leaves unknown whether it opened one: a timeout,
// a connection lost, GitHub failing on its side. One that exists already
// is looked for too.
function refused(r: GHResult): boolean {
  if (r.timedOut || /already exists/i.test(r.err)) return false;
  return /GraphQL: |HTTP 4\d\d|No commits between/i.test(r.err) && !/HTTP 5\d\d|EOF|timed? ?out|connection|something went wrong/i.test(r.err);
}

// opened is the open pull request from d's head into its base, if GitHub
// lists one.
async function opened(d: Dialog): Promise<PR | null> {
  const t = d.target;
  const list = await gh(d.workspace, ["pr", "list", "--repo", t.slug, "--head", t.ref, "--base", d.base, "--state", "open", "--json", PR_FIELDS]);
  if (!list.ok) return null;
  return pick(((JSON.parse(list.out ?? "null") as RawPR[] | null) ?? []).filter((p) => p.baseRefName === d.base), t.ref, t.slug);
}

// said joins what was done with what failed: "Committed abc1234 and
// pushed; gh pr create failed: …".
function said(done: string[], failed: string): string {
  const s = done.length === 0 ? failed : done.join(" and ") + "; " + failed;
  return s[0].toUpperCase() + s.slice(1);
}

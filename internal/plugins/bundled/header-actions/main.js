// The thread header's action row, as Glitched draws it by default. It is a
// plugin like any other: copy it in Settings › Plugins, and the copy draws
// the row in its place.

const panels = [
  { id: "terminal", icon: "terminal", tooltip: "Toggle terminal  ⌘J" },
  { id: "diff", icon: "pull-request", tooltip: "Toggle diff  ⌘D" },
  { id: "preview", icon: "global", tooltip: "Toggle preview  ⇧⌘P" },
];

const editors = [
  { value: "cursor", label: "Cursor" },
  { value: "vscode", label: "VS Code" },
  { value: "zed", label: "Zed" },
  { separator: true },
  { value: "finder", label: "Finder" },
];

const MAX_FILES = 12;

export function init() {
  return { state: { dialog: null } };
}

function panelButton(p, ctx) {
  return {
    type: "button",
    id: p.id,
    style: "ghost",
    icon: p.icon,
    label: p.tooltip,
    tooltip: p.tooltip,
    active: ctx.panels[p.id],
    badge: p.id === "diff" && ctx.changedFiles > 0 ? String(Math.min(ctx.changedFiles, 999)) : undefined,
    onClick: { handler: "panel", payload: p.id },
  };
}

function commitDialog(d) {
  const shown = d.files.slice(0, MAX_FILES).map((f, i) => ({ type: "text", id: "file" + i, text: f, style: "code" }));
  if (d.files.length > MAX_FILES) shown.push({ type: "text", id: "more", text: "and " + (d.files.length - MAX_FILES) + " more", style: "muted" });
  const where = d.push ? "pushes to " + d.push.remote + "/" + d.push.branch + (d.push.track ? ", which it starts tracking" : "") : "stays here: " + d.noPush;
  return {
    type: "dialog",
    id: "commit-dialog",
    title: "Commit & push",
    open: true,
    onClose: "commit.cancel",
    children: [
      { type: "text", id: "what", text: "Commits every change below, new files included, on " + d.branch + ", and " + where + ".", style: "muted" },
      { type: "column", id: "files", gap: 2, children: shown },
      { type: "input", id: "message", label: "Message", value: d.message, onSubmit: "commit.go" },
      { type: "text", id: "hooks", text: "Your git hooks don't run for a plugin's git.", style: "muted" },
      d.error ? { type: "text", id: "error", text: d.error, style: "error", selectable: true } : { type: "spacer", id: "noerror" },
      {
        type: "row",
        id: "buttons",
        gap: 8,
        justify: "end",
        children: [
          { type: "button", id: "cancel", label: "Cancel", onClick: "commit.cancel" },
          { type: "button", id: "go", label: "Commit & push", style: "primary", progress: true, onClick: "commit.go" },
        ],
      },
    ],
  };
}

function row(ctx, key, state) {
  const children = [];
  if (!ctx.local) return { type: "row", gap: 2, children: [panelButton(panels[2], ctx)] };
  children.push({
    type: "menu",
    id: "open",
    style: "outline",
    icon: "code",
    label: "Open",
    tooltip: "Open in",
    compact: ctx.narrow,
    options: editors,
    onChange: "open",
  });
  children.push({
    type: "button",
    id: "commit",
    style: "outline",
    icon: "upload-minimalistic",
    label: "Commit & push",
    tooltip: "Commit & push",
    compact: ctx.narrow,
    onClick: "commit.open",
  });
  children.push({ type: "row", id: "panels", gap: 2, padding: 2, children: panels.map((p) => panelButton(p, ctx)) });
  if (state.dialog && state.dialog.key === key) children.push(commitDialog(state.dialog));
  return { type: "row", gap: 8, children };
}

export function place({ state, items }) {
  return items.map((it) => (it.context ? row(it.context, it.key, state) : null));
}

async function git(cwd, args, timeout = 5) {
  const r = await glitched.run("git", args, { cwd, timeout });
  return { ok: r.status === 0, out: r.stdout, err: reason(r.stderr || r.stdout, r.status), timedOut: r.status === -1 };
}

// reason is the line of git's output that says why it failed, short
// enough for a toast: not the "To <remote>" a push starts with, nor its
// hints. A push the remote turned down says why it did.
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
  const chars = [...why];
  return chars.length > 200 ? chars.slice(0, 199).join("") + "…" : why;
}

// status reads what a commit would take: the branch, where it pushes, and
// the changed files. It says why not, when a commit can't be made.
async function status(cwd) {
  const s = await git(cwd, ["status", "--porcelain=v1", "--branch", "--untracked-files=all"]);
  if (!s.ok) return { error: s.err };
  const lines = s.out.split("\n").filter(Boolean);
  const head = (lines.shift() || "").replace(/^## /, "");
  if (head.startsWith("HEAD (no branch)")) return { error: "HEAD is detached: check out a branch to commit on." };
  const branch = head.replace(/^No commits yet on /, "").split("...")[0].split(" ")[0];
  if (lines.some((l) => /^(DD|AU|UD|UA|DU|AA|UU)/.test(l))) return { error: "Resolve the merge's conflicts first." };
  if (lines.length === 0) return { error: "Nothing to commit." };
  return { branch, files: lines, ...(await target(cwd, branch)) };
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

async function commitAndPush(d, message) {
  const add = await git(d.workspace, ["add", "-A"]);
  if (!add.ok) return { error: "git add: " + add.err };
  const commit = await git(d.workspace, ["commit", "-q", "-m", message]);
  if (!commit.ok) return { error: "git commit: " + commit.err };
  const sha = (await git(d.workspace, ["rev-parse", "--short", "HEAD"])).out.trim();
  const files = d.files.length === 1 ? "1 file" : d.files.length + " files";
  if (!d.push) return { title: "Committed " + sha, description: files + " on " + d.branch + "; not pushed: " + d.noPush };
  const to = d.push.remote + "/" + d.push.branch;
  const args = ["push", "--no-follow-tags", ...(d.push.track ? ["-u"] : []), d.push.remote, "refs/heads/" + d.branch + ":refs/heads/" + d.push.branch];
  const push = await git(d.workspace, args, 20);
  if (!push.ok) {
    const why = push.timedOut ? "it took over 20 seconds" : push.err;
    return { title: "Committed " + sha + ", not pushed", description: "Push to " + to + " failed: " + why + ". The commit stays on " + d.branch + "." };
  }
  return { title: "Pushed " + sha, description: files + " to " + to };
}

// update answers what the user did. A call the app refuses, as a grant
// taken back or git missing, is told in a toast: the row stays.
export async function update(args) {
  try {
    return await act(args);
  } catch (e) {
    await glitched.toast("Header actions", e.message).catch(() => {});
    return { state: { ...args.state, dialog: null } };
  }
}

async function act({ state, event, values }) {
  if (event.kind !== "ui") return {};
  const ctx = event.place?.context;
  switch (event.handler) {
    case "panel":
      await glitched.panels.toggle(event.payload);
      return {};
    case "open":
      await glitched.openIn(event.value, event.place.key);
      return {};
    case "commit.open": {
      const workspace = ctx?.thread?.workspace;
      if (!workspace) return {};
      const s = await status(workspace);
      if (s.error) {
        await glitched.toast("Can't commit", s.error);
        return {};
      }
      return { state: { ...state, dialog: { key: event.place.key, workspace, message: ctx.thread.title || "", ...s } } };
    }
    case "commit.cancel":
      return { state: { ...state, dialog: null } };
    case "commit.go": {
      const d = state.dialog;
      if (!d) return {};
      const message = String(values.message ?? d.message).trim();
      if (!message) return { state: { ...state, dialog: { ...d, error: "Write a message for the commit." } } };
      const done = await commitAndPush(d, message);
      if (done.error) return { state: { ...state, dialog: { ...d, message, error: done.error } } };
      await glitched.toast(done.title, done.description);
      return { state: { ...state, dialog: null } };
    }
  }
  return {};
}

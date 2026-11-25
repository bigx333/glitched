// Local utilities in a thread's menu. Copy branch name reads the branch
// with git in the thread's workspace; Open in editor asks Glitched for the
// first editor it finds, as the app's own menu item did.
export async function command({ id, args }) {
  const thread = args?.thread;
  if (!thread?.local || !thread.workspace) return;
  if (id === "copy-branch") {
    const r = await glitched.run("git", ["branch", "--show-current"], { cwd: thread.workspace, timeout: 5 });
    const branch = r.stdout.trim();
    if (r.status !== 0) {
      await glitched.toast("No branch to copy", (r.stderr || r.stdout).trim() || "git couldn't read the branch");
      return;
    }
    if (!branch) {
      await glitched.toast("No branch to copy", "HEAD is detached.");
      return;
    }
    await glitched.clipboard.write(branch);
    await glitched.toast("Copied", branch);
    return;
  }
  if (id === "open-editor") await glitched.openIn("editor", thread.id);
}

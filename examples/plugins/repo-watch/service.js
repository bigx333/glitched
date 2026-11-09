// The headless half, run by the engine with no window. Its timer checks the
// repository each minute, through git as you, and keeps each check in its
// database; its window asks it with glitched.service.call.

async function check() {
  const folder = host.setting("folder");
  if (!folder) return { error: "Choose a repository in Settings › Plugins." };
  const r = await glitched.run("git", ["status", "--porcelain=v1", "--branch"], { cwd: folder });
  if (r.status !== 0) return { error: r.stderr.trim() || `git exited ${r.status}` };
  const lines = r.stdout.split("\n").filter(Boolean);
  const branch = (lines[0] ?? "").replace(/^## /, "").split("...")[0];
  const changed = lines.length - 1;
  await glitched.db.exec("CREATE TABLE IF NOT EXISTS checks (at INTEGER, branch TEXT, changed INTEGER)");
  await glitched.db.exec("INSERT INTO checks VALUES (?, ?, ?)", [Date.now(), branch, changed]);
  return { branch, changed };
}

export async function timer() {
  await check();
  return {};
}

export async function serve({ method }) {
  switch (method) {
    case "check":
      return check();
    case "history":
      await glitched.db.exec("CREATE TABLE IF NOT EXISTS checks (at INTEGER, branch TEXT, changed INTEGER)");
      return glitched.db.query("SELECT at, branch, changed FROM checks ORDER BY at DESC LIMIT 10");
  }
  throw new Error("no method " + method);
}

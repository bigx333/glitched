// ax.js drives one process's window through the macOS accessibility tree.
// Run by glv: osascript -l JavaScript ax.js <pid> <command> [args...]
//
//   tree [filter]          every element: role | name | description | value
//   find <text>            the first element whose name or description starts
//                          with text: role, name, and frame as x,y,w,h
//   press <text> [role]    AXPress on the first match (role defaults to any)
//   set <role> <text>      focus the first element of role and set its value
//   value <role>           the value of the first element of role
//   has <text>             exit 0 when some element's name, description or
//                          value contains text, 1 otherwise
//   front                  bring the window to the front (see front below)
//   key <keys>             front, then a keystroke: cmd+k, cmd+shift+l, escape
//   menu <text>            press a menu item of the open context menu or the
//                          menu bar (Glitched draws its menus as AXMenuItem)

function run(argv) {
  const [pid, cmd, ...args] = argv;
  const se = Application("System Events");
  const procs = se.processes.whose({ unixId: Number(pid) });
  if (procs.length === 0) throw new Error("no process " + pid);
  const proc = procs[0];
  if (proc.windows.length === 0) throw new Error("process " + pid + " has no window");
  const all = () => {
    let els = [];
    for (let i = 0; i < proc.windows.length; i++) els = els.concat(proc.windows[i].entireContents());
    return els;
  };
  const s = (f) => { try { return String(f() ?? ""); } catch (e) { return ""; } };
  const info = (e) => ({ role: s(() => e.role()), name: s(() => e.name()), desc: s(() => e.description()), value: s(() => e.value()) });
  const match = (role, text) => all().find((e) => {
    const i = info(e);
    if (role && i.role !== role) return false;
    return text === undefined || i.name.startsWith(text) || i.desc.startsWith(text);
  });
  const need = (role, text) => {
    const e = match(role, text);
    if (!e) throw new Error("no element " + (role || "") + " " + JSON.stringify(text ?? ""));
    return e;
  };
  // front brings the window forward and returns once it really is in front.
  // Keystrokes and pointer events go to whatever app is frontmost, and macOS
  // hands focus back to the app the user is in within about half a second,
  // so send them right after this returns. proc.frontmost() only echoes the
  // request; ask which process really is in front.
  const front = () => {
    proc.frontmost = true;
    for (let i = 0; i < 20; i++) {
      if (se.processes.whose({ frontmost: true })[0].unixId() === Number(pid)) return;
      delay(0.05);
    }
    throw new Error("pid " + pid + " did not come to the front; not sending input");
  };
  switch (cmd) {
    case "tree":
      return all().map(info)
        .filter((i) => !args[0] || [i.role, i.name, i.desc, i.value].join(" ").includes(args[0]))
        .filter((i) => i.name || i.desc || i.value || i.role !== "AXStaticText")
        .map((i) => [i.role, i.name, i.desc, i.value].join(" | ")).join("\n");
    case "find": {
      const e = need(undefined, args[0]);
      const [x, y] = e.position(), [w, h] = e.size();
      return [info(e).role, info(e).name || info(e).desc, [x, y, w, h].join(",")].join(" | ");
    }
    case "press":
      need(args[1], args[0]).actions["AXPress"].perform();
      return "pressed " + args[0];
    case "set": {
      const e = need(args[0]);
      e.focused = true;
      e.value = args[1];
      return "set " + args[0];
    }
    case "value":
      return s(() => need(args[0]).value());
    case "has": {
      const hit = all().some((e) => { const i = info(e); return [i.name, i.desc, i.value].some((v) => v.includes(args[0])); });
      if (!hit) throw new Error("nothing shows " + JSON.stringify(args[0]));
      return "has " + args[0];
    }
    case "front":
      front();
      return "front " + pid;
    case "key": {
      const parts = args[0].toLowerCase().split("+");
      const key = parts.pop();
      const mods = { cmd: "command down", shift: "shift down", alt: "option down", opt: "option down", ctrl: "control down" };
      const using = parts.map((m) => { if (!mods[m]) throw new Error("unknown modifier " + m); return mods[m]; });
      const codes = { return: 36, enter: 36, escape: 53, esc: 53, tab: 48, delete: 51, space: 49, left: 123, right: 124, down: 125, up: 126 };
      front();
      if (codes[key] !== undefined) se.keyCode(codes[key], { using });
      else se.keystroke(key, { using });
      return "key " + args[0];
    }
    case "menu": {
      const e = all().concat(proc.menuBars.length ? proc.menuBars[0].entireContents() : [])
        .find((e) => info(e).role === "AXMenuItem" && (info(e).name.startsWith(args[0]) || info(e).desc.startsWith(args[0])));
      if (!e) throw new Error("no menu item " + JSON.stringify(args[0]));
      e.actions["AXPress"].perform();
      return "chose " + args[0];
    }
  }
  throw new Error("unknown command " + cmd);
}

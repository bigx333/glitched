// A focus timer in each thread's header. The block's end lives in storage,
// so a command (the palette, or cmd+shift+f) and the header's button start
// the same block. A 30 s timer moves the minutes on.

const minutes = () => Number(host.setting("length")) || 25;
const ends = () => host.storage.get("ends") ?? 0;

function left(now) {
  return Math.max(0, Math.ceil((ends() - now) / 60000));
}

export function init() {
  return { state: { now: Date.now() }, effects: status() };
}

// The setting's status says what the timer does now, beside its switch.
function status() {
  const on = left(Date.now()) > 0;
  return [{ type: "setting.status", setting: "show", status: on ? { badge: "Running", tone: "success", text: `${left(Date.now())} minutes left.` } : null }];
}

export function place({ state, items }) {
  if (host.setting("show") !== "true") return items.map(() => null);
  const n = left(state.now);
  return items.map(() =>
    n > 0
      ? { type: "row", gap: 6, children: [{ type: "badge", text: `${n} min`, style: "accent" }, { type: "button", id: "stop", label: "Stop", onClick: "stop" }] }
      : { type: "button", id: "start", label: `Focus ${minutes()} min`, onClick: "start" },
  );
}

function start(thread) {
  host.storage.set("ends", Date.now() + minutes() * 60000);
  if (thread) host.storage.set("log", [...(host.storage.get("log") ?? []), { thread, at: Date.now() }].slice(-50));
}

// The Focus tab, in a thread's panel: the blocks started in this thread.
export function view({ context }) {
  const blocks = (host.storage.get("log") ?? []).filter((b) => b.thread === context.id);
  return {
    body: {
      type: "column",
      gap: 8,
      children: [
        { type: "text", id: "about", text: `Focus blocks in this thread: ${blocks.length}`, style: "heading" },
        ...blocks.map((b, i) => ({ type: "text", id: `b${i}`, text: new Date(b.at).toISOString().slice(11, 16), style: "muted" })),
      ],
    },
  };
}

export function update({ state, event }) {
  const now = Date.now();
  if (event.kind === "ui" && event.handler === "start") start(event.place?.key);
  if (event.kind === "ui" && event.handler === "stop") host.storage.set("ends", 0);
  if (event.kind === "timer" && ends() > 0 && left(now) === 0) {
    host.storage.set("ends", 0);
    const effects = host.setting("end") === "notify" ? [{ type: "notify", title: "Focus block done" }] : [];
    return { state: { now }, effects: [...effects, ...status()] };
  }
  return { state: { now }, effects: status() };
}

export function command({ id }) {
  if (id !== "start") throw new Error("unknown command: " + id);
  start();
  return { effects: [{ type: "toast", title: `Focus for ${minutes()} minutes` }] };
}

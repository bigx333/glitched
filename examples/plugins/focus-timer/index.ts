/// <reference path="../glitched.d.ts" />

// A focus timer in each thread's header. The block's end lives in storage,
// so a command (the palette, or cmd+shift+f) and the header's button start
// the same block. A 30 s timer moves the minutes on.

type State = { now: number };
type BlockEntry = { thread: string; at: number };

let settings: Record<string, string | number | boolean> = {};
const minutes = (): number => (typeof settings.length === "number" ? settings.length : 25);
const ends = (): number => {
  const v = host.storage.get("ends");
  return typeof v === "number" ? v : 0;
};

function left(now: number): number {
  return Math.max(0, Math.ceil((ends() - now) / 60000));
}

export async function init(): Promise<Update<State>> {
  settings = await glitched.settings.get(); // typed: a number, a boolean
  await status();
  return { state: { now: Date.now() } };
}

// The setting's status says what the timer does now, beside its switch.
async function status(): Promise<void> {
  const n = left(Date.now());
  await glitched.settings.status("show", n > 0 ? { badge: "Running", tone: "success", text: `${n} minutes left.` } : null);
}

export function place({ state, items }: PlaceArgs<State, ThreadRecord>): (Widget | null)[] {
  if (settings.show === false) return items.map(() => null);
  const n = left(state.now);
  return items.map((): Widget =>
    n > 0
      ? { type: "row", gap: 6, children: [{ type: "badge", text: `${n} min`, style: "accent" }, { type: "button", id: "stop", label: "Stop", onClick: "stop" }] }
      : { type: "button", id: "start", label: `Focus ${minutes()} min`, onClick: "start" },
  );
}

function start(thread?: string): void {
  host.storage.set("ends", Date.now() + minutes() * 60000);
  if (thread) {
    const raw = host.storage.get("log");
    const log = Array.isArray(raw) ? (raw as BlockEntry[]) : [];
    host.storage.set("log", [...log, { thread, at: Date.now() }].slice(-50));
  }
}

// The Focus tab, in a thread's panel: the blocks started in this thread.
export function view({ context }: ViewArgs<State>): View {
  const raw = host.storage.get("log");
  const log = Array.isArray(raw) ? (raw as BlockEntry[]) : [];
  const blocks = log.filter((b) => b.thread === context?.id);
  return {
    body: {
      type: "column",
      gap: 8,
      children: [
        { type: "text", id: "about", text: `Focus blocks in this thread: ${blocks.length}`, style: "heading" },
        ...blocks.map((b, i): Widget => ({ type: "text", id: `b${i}`, text: new Date(b.at).toISOString().slice(11, 16), style: "muted" })),
      ],
    },
  };
}

export async function update({ event }: UpdateArgs<State, ThreadRecord>): Promise<Update<State>> {
  const now = Date.now();
  if (event.kind === "app" && event.type === "setting.changed") settings = await glitched.settings.get();
  if (event.kind === "ui" && event.handler === "start") start(event.place?.key);
  if (event.kind === "ui" && event.handler === "stop") host.storage.set("ends", 0);
  if (event.kind === "timer" && ends() > 0 && left(now) === 0) {
    host.storage.set("ends", 0);
    if (settings.end === "notify") await glitched.notify("Focus block done");
  }
  await status();
  return { state: { now } };
}

export async function command({ id }: CommandArgs): Promise<void> {
  if (id !== "start") throw new Error("unknown command: " + id);
  settings = await glitched.settings.get();
  start();
  await glitched.toast(`Focus for ${minutes()} minutes`);
}

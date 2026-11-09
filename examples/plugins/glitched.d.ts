// glitched.d.ts: the types of Glitched's plugin contract, "glitched"
// version 1, for a plugin's editor. Reference it from a TypeScript plugin:
//
//   /// <reference path="./glitched.d.ts" />
//
// docs/plugins.md is the contract; this file only types it.

/** What a handler is: its name, or its name and a payload of your own. */
type Handler = string | { handler: string; payload?: unknown };

/** Theme tokens: a plugin follows the user's theme, never raw colors. */
type Color = "$text" | "$text.muted" | "$text.faint" | "$accent" | "$success" | "$warning" | "$danger" | "$border" | "$surface";

/** A choice of a select, tabs or menu: a string is both value and label. */
type Option = string | { value: string; label?: string };

/** Containers lay out their children. */
interface Layout {
  id?: string;
  children?: Widget[];
  /** Space between the children, 0 to 64 points. */
  gap?: number;
  /** Space around the children, 0 to 64 points. */
  padding?: number;
  align?: "start" | "center" | "end" | "stretch";
  justify?: "start" | "center" | "end" | "between";
  /** Take the room the parent leaves. */
  grow?: boolean;
}

type Widget =
  | ({ type: "column" | "row"; background?: Color } & Layout)
  | ({ type: "card"; title?: string } & Layout)
  | {
      type: "text";
      id?: string;
      text: string;
      style?: "body" | "muted" | "heading" | "title" | "code" | "error" | "success";
      color?: Color;
      selectable?: boolean;
    }
  | { type: "markdown"; id?: string; text: string }
  | { type: "badge"; id?: string; text: string; style?: "default" | "accent" | "success" | "warning" | "danger"; color?: Color }
  | { type: "divider" | "spacer" | "spinner"; id?: string }
  | { type: "icon"; id?: string; /** One of the app's icons, or "plugin:<file>.svg". */ name: string; color?: Color }
  | {
      type: "image";
      id?: string;
      /** "plugin:<file>" (PNG, JPEG or SVG) in the plugin's folder. */
      src: string;
      width?: number;
      height?: number;
      radius?: number;
    }
  | {
      type: "table";
      id?: string;
      columns: { id: string; title: string; width?: number }[];
      rows: { key: string; cells: Record<string, string> }[];
    }
  | { type: "button"; id: string; label: string; style?: "default" | "primary" | "danger"; disabled?: boolean; onClick?: Handler }
  | {
      type: "input";
      id: string;
      label?: string;
      placeholder?: string;
      value?: string;
      /** The plugin holds the value: once it took the user's changes, value is the value. */
      controlled?: boolean;
      multiline?: boolean;
      disabled?: boolean;
      onChange?: Handler;
      /** Enter, in a single-line input. */
      onSubmit?: Handler;
    }
  | { type: "toggle"; id: string; label: string; value?: boolean; controlled?: boolean; disabled?: boolean; onChange?: Handler }
  | { type: "select"; id: string; label?: string; options: Option[]; value?: string; controlled?: boolean; disabled?: boolean; onChange?: Handler }
  | { type: "tabs"; id: string; options: Option[]; value?: string; controlled?: boolean; disabled?: boolean; onChange?: Handler }
  | { type: "menu"; id: string; label: string; options: Option[]; disabled?: boolean; /** The chosen option's value is the event's. */ onChange?: Handler }
  | { type: "dialog"; id: string; title: string; open?: boolean; children?: Widget[]; /** Escape, or a click outside. */ onClose?: Handler };

/** What view answers. */
interface View {
  title?: string;
  subtitle?: string;
  body: Widget;
}

/** The values of the view's inputs, toggles, selects and tabs, by id. */
type Values = Record<string, string | boolean>;

/** A thread, as a plugin sees it: without threads:read, its id alone. */
interface ThreadRecord {
  id: string;
  title?: string;
  workspace?: string;
  status?: "idle" | "working" | "completed" | "waiting";
  machine?: string;
  updatedAt?: string;
}

type UIEvent = {
  kind: "ui";
  type: "click" | "change" | "submit" | "close";
  handler: string;
  /** The id of the widget. */
  node: string;
  /** An input's text, a toggle's state, a select's, tab's or menu's value. */
  value?: string | boolean;
  payload?: unknown;
  /** Set in a place: which copy, and the context it was drawn with. */
  place?: { id: string; at: Slot; key: string; context?: ThreadRecord };
};

type EffectEvent = {
  kind: "effect";
  /** The effect's id; absent when it had none, which only a failure sends. */
  id?: string;
  effect: string;
  ok: boolean;
  result?: unknown;
  error?: string;
  /** The capability is undeclared or not granted. */
  denied?: boolean;
};

/** An app event the plugin hears (its manifest's glitched.events), or a timer. */
type AppEvent =
  | { kind: "app"; type: "thread.changed"; data: ThreadRecord | { id: string; deleted: true } }
  | { kind: "app"; type: "thread.message"; data: { thread: string; role: "user" | "assistant"; text: string } }
  | { kind: "app"; type: "setting.changed"; data: { id: string; value: unknown; was: unknown } }
  | { kind: "app"; type: "theme.changed"; data: { appearance: "light" | "dark" } }
  | { kind: "app"; type: "command"; data: { id: string } }
  | { kind: "timer"; id: string };

type GlitchedEvent = UIEvent | EffectEvent | AppEvent;

/** An effect asks the app to do something; id names its answer. */
type Effect = { id?: string } & (
  | { type: "toast"; title: string; description?: string }
  | { type: "url.open"; url: string }
  | { type: "page.open"; page: string }
  | { type: "tab.open"; tab: string }
  | { type: "setting.status"; setting: string; status: { badge?: string; tone?: "neutral" | "success" | "warning" | "danger"; text?: string } | null }
  | { type: "clipboard.write"; text: string }
  | { type: "notify"; title: string; body?: string }
  | { type: "composer.draft"; text: string; workspace?: string }
  | { type: "threads.list" }
  | { type: "threads.open"; thread: string }
  | { type: "threads.create"; prompt: string; title?: string; workspace?: string }
  | { type: "threads.send"; thread: string; text: string }
);

/** What init, update, command and a service's timer answer. Leave state out to keep it. */
interface Update<S> {
  state?: S;
  effects?: Effect[];
}

/** Glitched's slots. Stable ones keep their name and context across updates. */
type Slot = "thread.header" | "thread.row" | "sidebar.footer";

interface InitArgs {
  /** The page's id; "" for the app session; a tab's id in a tab. */
  page: string;
  tab?: string;
  /** A tab's thread. */
  context?: ThreadRecord;
}
interface ViewArgs<S> extends InitArgs {
  state: S;
  values: Values;
}
interface UpdateArgs<S> extends InitArgs {
  state: S;
  event: GlitchedEvent;
  values: Values;
}
/** place draws every copy of a place in one call: a widget or null each, in order. */
interface PlaceArgs<S> {
  id: string;
  at: Slot;
  state: S;
  items: { key: string; context?: ThreadRecord }[];
}
interface CommandArgs {
  id: string;
  args?: unknown;
}
/** An agent's call of a tool the manifest lists. */
interface ToolArgs {
  name: string;
  args: Record<string, unknown>;
  call: { thread: string };
}
/** A window's call of its service: glitched.service.call(method, args). */
interface ServeArgs {
  method: string;
  args: unknown;
}

/** What glitched.run answers. A run past its timeout has status -1. */
interface RunResult {
  status: number;
  stdout: string;
  stderr: string;
}

/** The glitched global: awaitable calls into Glitched. Each fails with err.code "denied" without its grant. */
declare const glitched: {
  /** Run a program the manifest's glitched.programs names, as you, with no shell. Needs programs. */
  run(program: string, args?: string[], options?: { cwd?: string; stdin?: string; timeout?: number; env?: Record<string, string> }): Promise<RunResult>;
  /** The plugin's own SQLite database: parameters are always bound. */
  db: {
    exec(sql: string, params?: unknown[]): Promise<{ changes: number; last_id: number }>;
    query<Row = Record<string, unknown>>(sql: string, params?: unknown[]): Promise<Row[]>;
  };
  /** Its service, in this machine's engine. Needs service. */
  service: {
    call<T = unknown>(method: string, args?: unknown, options?: { timeout?: number }): Promise<T>;
  };
};

/** mygo-plugin's host global: the subset Glitched plugins use most. */
declare const host: {
  readonly platform: string;
  readonly dataDir: string;
  language(): string;
  /** A setting's value as text: "true", "25", the chosen option's value. */
  setting(key: string): string;
  /** Whether the plugin has a capability now: declared, and granted if it needs to be. */
  capability(name: string): boolean;
  openURL(url: string): void;
  /** A small JSON store; glitched.db holds more. */
  storage: {
    get(key: string): unknown;
    set(key: string, value: unknown): void;
    remove(key: string): void;
  };
  call<T = unknown>(name: string, args?: unknown): Promise<T>;
  functions(): string[];
};

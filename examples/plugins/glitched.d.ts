// glitched.d.ts: the types of Glitched's plugin contract, "glitched"
// version 2, for a plugin's editor. Reference it from a TypeScript plugin:
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
  | { type: "button"; id: string; label: string; style?: "default" | "primary" | "danger"; disabled?: boolean; tooltip?: string; onClick?: Handler }
  | ({ type: "button"; style: "ghost"; /** It shows only its icon. */ icon: string; /** On, as a panel's toggle. */ active?: boolean; /** Up to 4 characters over its corner. */ badge?: string } & DrawnButton)
  | ({ type: "button"; style: "outline"; icon?: string; /** The icon alone. */ compact?: boolean } & DrawnButton)
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
  | {
      type: "menu";
      id: string;
      label: string;
      options: Option[];
      disabled?: boolean;
      tooltip?: string;
      /** outline: a hairline pill with a chevron, as the header's Open in. */
      style?: "default" | "outline";
      /** An outline menu's. */
      icon?: string;
      compact?: boolean;
      /** The chosen option's value is the event's. */
      onChange?: Handler;
    }
  | { type: "dialog"; id: string; title: string; open?: boolean; children?: Widget[]; /** Escape, or a click outside. */ onClose?: Handler };

/** A ghost or outline button: drawn as Glitched's header draws its own. */
interface DrawnButton {
  id: string;
  /** Its text, and its name to assistive tools when it has no tooltip. */
  label: string;
  /** Shown on hover, and its name to assistive tools. */
  tooltip?: string;
  disabled?: boolean;
  onClick?: Handler;
}

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
  place?: { id: string; at: Slot | Component; key: string; context?: ThreadRecord | HeaderActionsContext };
};

/** An app event the plugin hears (its manifest's glitched.events), or a timer. */
type AppEvent =
  | { kind: "app"; type: "thread.changed"; data: ThreadRecord | { id: string; deleted: true } }
  | { kind: "app"; type: "thread.message"; data: { thread: string; role: "user" | "assistant"; text: string } }
  | { kind: "app"; type: "setting.changed"; data: { id: string; value: unknown; was: unknown } }
  | { kind: "app"; type: "theme.changed"; data: { appearance: "light" | "dark" } }
  | { kind: "app"; type: "command"; data: { id: string } }
  | { kind: "app"; type: "grant.changed"; data: { capability: string; granted: boolean } }
  | { kind: "timer"; id: string };

type GlitchedEvent = UIEvent | AppEvent;

/** What init and update answer. Leave state out to keep it. */
interface Update<S> {
  state?: S;
}

/** Glitched's slots. Stable ones keep their name and context across updates. */
type Slot = "thread.header" | "thread.row" | "sidebar.footer";

/**
 * Glitched's components: a place with `replaces: true` at one draws it in
 * place of the built-in plugin, once ui.replace is granted. Needs
 * contractVersion 2. approval.card, question.card, composer.access,
 * composer.send, settings.plugins, settings.servers and
 * confirm.destructive are off limits: a plugin that names one doesn't load.
 */
type Component = "thread.header.actions";

/** The context of thread.header.actions, the buttons at the right of a thread's header. */
interface HeaderActionsContext {
  thread: ThreadRecord;
  /** The thread's workspace is on this machine. */
  local: boolean;
  /** The chat is under 700 points wide: the built-in shows icons only. */
  narrow: boolean;
  panels: PanelState;
  /** The files the agent changed, which the diff toggle counts. */
  changedFiles: number;
}

/** Which of the panels beside the chat are open. */
interface PanelState {
  terminal: boolean;
  diff: boolean;
  preview: boolean;
}

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
  at: Slot | Component;
  state: S;
  /** A component's copies have its context: HeaderActionsContext for thread.header.actions. */
  items: { key: string; context?: ThreadRecord | HeaderActionsContext }[];
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
  toast(title: string, description?: string): Promise<void>;
  openURL(url: string): Promise<void>;
  pages: { open(page: string): Promise<void> };
  tabs: { open(tab: string): Promise<void> };
  /** The panels beside the chat. Needs ui.place. */
  panels: {
    /** Opens or closes one; open says which way, else it flips. */
    toggle(panel: "terminal" | "diff" | "preview", open?: boolean): Promise<PanelState>;
    state(): Promise<PanelState>;
  };
  /** Opens the thread's workspace, on this machine, in an app. Needs threads:read. */
  openIn(app: "cursor" | "vscode" | "zed" | "finder", thread: string): Promise<void>;
  settings: {
    /** Its settings by id, typed: a switch's boolean, a number's number. */
    get(): Promise<Record<string, string | number | boolean>>;
    status(setting: string, status: { badge?: string; tone?: "neutral" | "success" | "warning" | "danger"; text?: string } | null): Promise<void>;
  };
  clipboard: { write(text: string): Promise<void> };
  notify(title: string, body?: string): Promise<void>;
  composer: { draft(text: string, workspace?: string): Promise<void> };
  threads: {
    list(): Promise<ThreadRecord[]>;
    open(thread: string): Promise<void>;
    create(prompt: string, options?: { title?: string; workspace?: string }): Promise<{ thread: string }>;
    send(thread: string, text: string): Promise<{ message: string }>;
  };
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

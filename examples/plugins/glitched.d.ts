// glitched.d.ts: the types of Glitched's plugin contract, "glitched"
// version 1, for a plugin's editor. Reference it from a TypeScript plugin:
//
//   /// <reference path="./glitched.d.ts" />
//
// docs/plugins.md is the contract; this file only types it. The host global
// (storage, fetch, openURL, ...) is mygo-plugin's; its declarations are
// spec/host.d.ts in github.com/yxxbc/mygo-plugin, and the subset Glitched
// plugins use most is declared at the end of this file.

/** What a handler is: its name, or its name and a payload of your own. */
type Handler = string | { handler: string; payload?: unknown };

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
  | ({ type: "column" } & Layout)
  | ({ type: "row" } & Layout)
  | ({ type: "card"; title?: string } & Layout)
  | {
      type: "text";
      id?: string;
      text: string;
      style?: "body" | "muted" | "heading" | "title" | "code" | "error" | "success";
      selectable?: boolean;
    }
  | { type: "markdown"; id?: string; text: string }
  | { type: "badge"; id?: string; text: string; style?: "default" | "accent" | "success" | "warning" | "danger" }
  | { type: "divider"; id?: string }
  | { type: "spacer"; id?: string }
  | { type: "spinner"; id?: string }
  | {
      type: "button";
      id: string;
      label: string;
      style?: "default" | "primary" | "danger";
      disabled?: boolean;
      onClick?: Handler;
    }
  | {
      type: "input";
      id: string;
      label?: string;
      placeholder?: string;
      /** Applied when it differs from the value the last view gave. */
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
  | {
      type: "select";
      id: string;
      label?: string;
      options: (string | { value: string; label?: string })[];
      value?: string;
      controlled?: boolean;
      disabled?: boolean;
      onChange?: Handler;
    };

/** What view answers. */
interface View {
  title?: string;
  subtitle?: string;
  body: Widget;
}

/** The values of the view's inputs, toggles and selects, by id. */
type Values = Record<string, string | boolean>;

type UIEvent = {
  kind: "ui";
  type: "click" | "change" | "submit";
  handler: string;
  /** The id of the widget. */
  node: string;
  /** An input's text, a toggle's state, a select's value. */
  value?: string | boolean;
  payload?: unknown;
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

type GlitchedEvent = UIEvent | EffectEvent;

/** An effect asks the app to do something; id names its answer. */
type Effect = { id?: string } & (
  | { type: "toast"; title: string; description?: string }
  | { type: "url.open"; url: string }
  | { type: "page.open"; page: string }
  | { type: "clipboard.write"; text: string }
  | { type: "notify"; title: string; body?: string }
  | { type: "composer.draft"; text: string; workspace?: string }
  | { type: "threads.list" }
  | { type: "threads.open"; thread: string }
  | { type: "threads.create"; prompt: string; title?: string; workspace?: string }
  | { type: "threads.send"; thread: string; text: string }
);

/** threads.list's result, newest first. */
interface Thread {
  id: string;
  title: string;
  workspace: string;
  status: "idle" | "working" | "completed" | "waiting";
  machine: string;
  updatedAt: string;
}

/** What init and update answer. Leave state out to keep it. */
interface Update<S> {
  state?: S;
  effects?: Effect[];
}

interface InitArgs {
  page: string;
}
interface ViewArgs<S> {
  page: string;
  state: S;
  values: Values;
}
interface UpdateArgs<S> {
  page: string;
  state: S;
  event: GlitchedEvent;
  values: Values;
}
interface CommandArgs {
  id: string;
  args?: unknown;
}

declare const host: {
  readonly platform: string;
  readonly dataDir: string;
  language(): string;
  /** Whether the user granted, or the plugin may use, a capability. */
  capability(name: string): boolean;
  openURL(url: string): void;
  storage: {
    get(key: string): unknown;
    set(key: string, value: unknown): void;
    remove(key: string): void;
  };
};

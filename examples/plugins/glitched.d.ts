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

/** 0 to 64 points: every side; top and bottom, then the sides; or top, right, bottom and left. */
type Spacing = number | [number, number] | [number, number, number, number];

/** Containers lay out their children. */
interface Layout {
  id?: string;
  children?: Widget[];
  /** Space between the children, 0 to 64 points. */
  gap?: number;
  /** Space around the children. */
  padding?: Spacing;
  align?: "start" | "center" | "end" | "stretch";
  justify?: "start" | "center" | "end" | "between";
  /** Take the room the parent leaves. */
  grow?: boolean;
}

/** A row's or column's frame. */
interface Box {
  /** A border one point wide. */
  border?: Color;
  /** The border's opacity, 0 to 1. */
  borderOpacity?: number;
  /** One radius for every corner; a background alone rounds them by 8. */
  radius?: number;
  /** Each corner's radius: top-left, top-right, bottom-right, bottom-left. */
  corners?: [number, number, number, number];
  /** At most this share of the parent's width, 1 to 100; 0 is none. */
  maxWidthPercent?: number;
  /** Its width and height, in points; 0 is as its children need. */
  width?: number;
  height?: number;
}

type Widget = {
  /** In a list's row only: drawn while the row is pointed at (hover), or while it isn't (idle). */
  when?: "hover" | "idle";
} & (
  | ({ type: "column" | "row"; background?: Color; /** A row's children run onto more lines. */ wrap?: boolean } & Box & Layout)
  | ({
      type: "column" | "row";
      /** A list row: lit under the pointer, clicked as a whole. */
      id: string;
      onClick: Handler;
      background?: Color;
      wrap?: boolean;
      /** A row's: Glitched's menu on a right click, "thread:<id>" or "project:<key>". Needs threads:read. */
      contextMenu?: string;
    } & Box & Layout)
  | {
      /** A project's mark: its icon, white on its color, as projects.list gives them. */
      type: "tile";
      id?: string;
      icon: string;
      /** #rrggbb. */
      color: string;
      /** 8 to 64 points. */
      width: number;
    }
  | ({ type: "card"; title?: string; /** list: Settings' list of rows, a line between its children. */ style?: "default" | "list" } & Layout)
  | {
      type: "text";
      id?: string;
      text: string;
      style?: "body" | "muted" | "strong" | "heading" | "title" | "code" | "error" | "success";
      size?: "small";
      /** 8 to 48 points, over its style's. */
      fontSize?: number;
      /** 100 to 900, over its style's. */
      weight?: number;
      color?: Color;
      italic?: boolean;
      selectable?: boolean;
      padding?: Spacing;
      grow?: boolean;
    }
  | { type: "markdown"; id?: string; text: string }
  | { type: "badge"; id?: string; text: string; style?: "default" | "accent" | "success" | "warning" | "danger" | "count"; color?: Color }
  /** The header's hairline pill, as its workspace and tokens; the tooltip is also its name to assistive tools. */
  | { type: "badge"; id?: string; text: string; style: "pill"; tooltip?: string }
  | { type: "divider"; id?: string; /** vertical: a line that stands up in its row, as tall as the row. */ style?: "vertical" }
  | { type: "spacer" | "spinner"; id?: string }
  | { type: "icon"; id?: string; /** One of the app's icons, or "plugin:<file>.svg". */ name: string; color?: Color; /** 8 to 48 points. */ fontSize?: number }
  | { type: "keys"; id?: string; /** Keycaps, 1 to 8, as ["⌘", "K"]. */ keys: string[] }
  | { type: "link"; id?: string; text: string; /** http or https, opened in the browser. */ href: string }
  | {
      type: "image";
      id?: string;
      /** "plugin:<file>" (PNG, JPEG or SVG) in the plugin's folder, an https address with the network grant, or "app:icon", Glitched's icon. */
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
  | {
      type: "button";
      id: string;
      label: string;
      style?: "default" | "primary" | "danger";
      disabled?: boolean;
      tooltip?: string;
      /** Once clicked, a spinner and no clicks till the plugin has drawn after the click. */
      progress?: boolean;
      onClick?: Handler;
    }
  | ({ type: "button"; /** Settings' secondary button. */ style: "bordered"; icon?: string } & DrawnButton)
  | ({ type: "button"; style: "ghost"; /** It shows only its icon. */ icon: string } & GhostButton)
  | ({ type: "button"; style: "ghost"; /** Drawn in place of an icon, as a project's chip: text, tile, icon, badge and spacer only. */ children: Widget[]; gap?: number } & GhostButton)
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
      /** search: a magnifier and a clear button. */
      style?: "default" | "search";
      onChange?: Handler;
      /** Enter, in a single-line input. */
      onSubmit?: Handler;
    }
  | {
      type: "toggle";
      id: string;
      /** A switch's is only its name to assistive tools. */
      label: string;
      /** switch: Settings' switch. */
      style?: "default" | "switch";
      value?: boolean;
      controlled?: boolean;
      disabled?: boolean;
      onChange?: Handler;
    }
  | { type: "select"; id: string; label?: string; options: Option[]; value?: string; controlled?: boolean; disabled?: boolean; /** 40 to 480 points. */ width?: number; onChange?: Handler }
  | {
      type: "tabs";
      id: string;
      options: Option[];
      value?: string;
      controlled?: boolean;
      disabled?: boolean;
      onChange?: Handler;
      /** control: Settings' segmented control. */
      style?: "default" | "control";
      /** Its name to assistive tools. */
      label?: string;
    }
  | {
      /** A dot of its color, ringed while active, as Appearance's accents. */
      type: "swatch";
      id: string;
      /** #rrggbb. */
      color: string;
      label: string;
      active?: boolean;
      onClick?: Handler;
    }
  | {
      type: "tabs";
      id: string;
      /** Drawn as the diff panel's Unified and Split switch. */
      style: "segmented";
      options: (string | { value: string; label?: string; disabled?: boolean; tooltip?: string })[];
      value?: string;
      controlled?: boolean;
      disabled?: boolean;
      onChange?: Handler;
    }
  | {
      /** A list's row. */
      type: "item";
      id: string;
      icon: string;
      text: string;
      /** code: a monospace text. */
      style?: "code";
      label?: string;
      /** Selected. */
      active?: boolean;
      /** After the text. */
      trailing?: Widget[];
      /** In place of trailing while the pointer is over it. */
      actions?: Widget[];
      onClick?: Handler;
    }
  | NativeWidget
  | ListWidget
  | {
      type: "menu";
      id: string;
      label: string;
      /** A separator is a line between two options. */
      options: (Option | { separator: true })[];
      disabled?: boolean;
      tooltip?: string;
      /** outline: a hairline pill with a chevron, as the header's Open in. ghost: drawn as a ghost button, with its children, as the composer's workspace. */
      style?: "default" | "outline" | "ghost";
      /** A ghost menu's, in place of an icon: text, tile, icon and spacer only. */
      children?: Widget[];
      gap?: number;
      padding?: number | number[];
      /** An outline menu's. */
      icon?: string;
      compact?: boolean;
      /** The chosen option's value is the event's. */
      onChange?: Handler;
    }
  | { type: "dialog"; id: string; title: string; open?: boolean; children?: Widget[]; /** Escape, or a click outside. */ onClose?: Handler }
);

/**
 * A long list: the view names every item, as data, and Glitched draws the
 * rows in view, asking the plugin's rows export for those alone. Glitched
 * keeps the choice, which a click, Up, Down, Home and End move.
 */
interface ListWidget {
  type: "list";
  id: string;
  /** Its name to assistive tools. */
  label: string;
  /** Up to 20,000, in order. */
  items: ListItem[];
  /** An item's height when it names none: 8 to 400 points, 32 if left out. */
  rowHeight?: number;
  /** Fill the room the parent leaves; else give a height. */
  grow?: boolean;
  /** As tall as its rows, up to this, past which it scrolls: 8 to 2000 points. */
  height?: number;
  /** Space between the rows, 0 to 64 points. */
  gap?: number;
  padding?: Spacing;
  /** The key of the item chosen, "" for none; never a header's. */
  value?: string;
  /** The plugin holds the choice: once it took the user's moves, the view's value is the choice. */
  controlled?: boolean;
  /** The id of a single-line input of the view, with no onSubmit and no other list, whose Up and Down move the choice and whose Enter submits it. */
  input?: string;
  /** A click submits the row as well as choosing it, as a palette's. */
  submitOnClick?: boolean;
  /** The row under the pointer becomes the choice, as a palette's. */
  chooseOnHover?: boolean;
  /** Up from the first item goes to the last, Down from the last to the first; Home and End stop at the ends. */
  wrapAround?: boolean;
  /** The choice moved: the event's value is its key. */
  onChange?: Handler;
  /** Enter, Cmd-Enter (Control-Enter off macOS), a double click, or a click with submitOnClick: the value is the key, and modifiers the keys held. */
  onSubmit?: Handler;
}

interface ListItem {
  /** 1 to 256 bytes, unique in the list: the choice, rows and events name the item by it. */
  key: string;
  /** Its name to assistive tools, 1 to 1024 bytes. */
  label: string;
  /** Changes when its row must be drawn again. A new view draws the rows in view again too. */
  version?: string;
  /** 8 to 400 points. */
  height?: number;
  /** Heads a section: never chosen, and the keys move past it. */
  header?: boolean;
  /** A thread's id: a right click opens Glitched's menu of it. Needs threads:read. */
  thread?: string;
}

/**
 * One of Glitched's own views, placed in the plugin's layout and drawn by
 * Glitched, only in the components it names. Any other name, a locked part
 * of the UI included, refuses the view.
 */
type NativeWidget = {
  type: "native";
  id?: string;
  /** Fill the room the parent leaves. */
  grow?: boolean;
} & (
  | {
      /** The diff of a file the thread changed, in thread.panel.diff. */
      name: "diff.body";
      props?: { path?: string };
      /** Fired with {path, remaining} as a change is marked reviewed. */
      onChange?: Handler;
    }
  | {
      /** A theme's preview and name, in settings.page.appearance. */
      name: "theme.card";
      props: { theme: string };
      /** Fired with the theme's name as it is clicked. */
      onChange?: Handler;
    }
  | {
      /** The composer itself, in composer: placed exactly once, in rows, columns and cards alone. */
      name: "composer.input";
    }
  | {
      /** What a timeline row says, live, with nothing around it, in timeline.message: placed exactly once, in rows, columns and cards alone. */
      name: "transcript.body";
      /** bare: only what the row says, with nothing around it; the copy draws the bubble, note and outline. Without, the row as Glitched drew it before. */
      props?: { bare?: boolean };
      /** A bare body's: insets the words inside their own text, so it selects with them. */
      padding?: number | number[];
    }
  | {
      /** A failed command's Retry and Dismiss, in timeline.message, 8 points after what is before it; nothing in any other row, taking no room of its own, though its row's gap and its own grow still do. */
      name: "transcript.retry";
    }
  | {
      /** A message's actions, in message.toolbar, as Glitched draws them: Copy; Edit & resend, a message of yours'; Retry, under the last reply that says something of a turn of yours that failed, was stopped or was given up on, or under your message when the turn said nothing. Nothing, and no room or gap, where the message has no such action. */
      name: "message.copy" | "message.edit" | "message.retry";
    }
  | {
      /** The command a plugin put in message.actions at index, of the context's items; nothing at an index no command has. */
      name: "message.item";
      props: { index: number };
    }
  | {
      /** When the message was sent, or the reply came, in message.toolbar, in the user's formats: the time today, the day too before, the year too in another year, and how long a turn ran under its last reply; the day written out in its tooltip. Nothing for a message not sent yet. */
      name: "message.time";
    }
  | {
      /** The header's button back to a thread above the one shown, in thread.header.breadcrumb; nothing for a thread not among the context's ancestors. */
      name: "thread.crumb";
      props: { thread: string };
    }
  | {
      /** The title of the thread shown, in thread.header.breadcrumb, whose layout must place it once. */
      name: "thread.heading";
    }
  | {
      /**
       * In a list's row of sidebar.body, about the thread its item names:
       * the row's actions (place it with when: "hover"), the field that
       * renames it while the user does, its project's tile with its state
       * and jokes, or its children's toggle.
       */
      name: "thread.row.actions" | "thread.rename" | "thread.tile" | "thread.children";
    }
  | {
      /** In a list's row of sidebar.body, palette or agents.body: what the item's thread is doing, as a mark or, with dot, the agents panel's dot. */
      name: "thread.status";
      props?: { dot?: boolean };
    }
  | {
      /** In composer.model, in a list's row or not: a model's mark, the copy's model's or, with family, that family's; size is its side, 18 by default. */
      name: "model.tile";
      props?: { family?: string; size?: number };
    }
  | {
      /**
       * In composer.model: the effort's name in its color; fast mode's
       * lightning while it's on, nothing while it's off; or the effort's
       * slider under its name and fast mode's switch.
       */
      name: "model.level" | "model.zap" | "model.effort";
    }
);

/** A ghost or outline button: drawn as Glitched's header draws its own. */
interface DrawnButton {
  id: string;
  /** Its text, and its name to assistive tools when it has no tooltip. */
  label: string;
  /** Shown on hover, and its name to assistive tools. */
  tooltip?: string;
  disabled?: boolean;
  /** Dims it, 0.2 to 1. */
  opacity?: number;
  onClick?: Handler;
}

interface GhostButton extends DrawnButton {
  /** On, as a panel's toggle. */
  active?: boolean;
  /** Up to 4 characters over its corner. */
  badge?: string;
  size?: "small";
  /** A theme token, or #rrggbb. */
  color?: Color | string;
  padding?: Spacing;
  /** Glitched's menu on a right click: "thread:<id>" or "project:<key>". Needs threads:read. */
  contextMenu?: string;
  /** A panel hanging from the button while open: a column, 120 to 480 points wide. */
  popover?: { type: "column"; width: number } & Box & Layout;
  open?: boolean;
  /** Escape or a click outside closed the popover: clear open in your state. */
  onClose?: Handler;
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
  /** Where the sidebar files it. */
  section?: "inbox" | "working" | "snoozed" | "settled";
  /** How the user left it, what was just done here counted. */
  disposition?: Disposition;
  unread?: boolean;
  /** The thread that started it; that one's line, the furthest first; the threads it started, newest first. */
  parent?: string;
  ancestors?: string[];
  children?: string[];
  /** The approvals and questions it waits on. */
  requests?: number;
  /** What its own runs used. */
  tokens?: number;
  /** Its workspace is on this machine, where glitched.run runs. */
  local?: boolean;
}

interface Disposition {
  state: "inbox" | "snoozed" | "settled";
  /** RFC 3339, a snooze's. */
  until?: string;
}

/** One of the command palette's actions, as commands.list gives it. */
interface CommandRecord {
  /** Names it to commands.run. */
  id: string;
  /** The palette's heading over it. */
  section: string;
  label: string;
  /** Its shortcut's keycaps. */
  hint?: string;
  /** One of the app's icons, by name. */
  icon: string;
  /** Words a search finds it by, whole, not shown. */
  also?: string;
}

/** A project, as projects.list gives it. */
interface ProjectRecord {
  /** Names it to projects.filter and in a contextMenu. */
  key: string;
  workspace: string;
  machine?: string;
  name: string;
  /** Its mark: an icon of the app's, and #rrggbb. */
  icon: string;
  color: string;
  /** The sidebar shows it, filtered to some projects. */
  filtered?: boolean;
}

type UIEvent = {
  kind: "ui";
  type: "click" | "change" | "submit" | "close";
  handler: string;
  /** The id of the widget. */
  node: string;
  /** An input's text, a toggle's state, a select's, tab's or menu's value, a list's key. */
  value?: string | boolean | { path: string; remaining: number };
  payload?: unknown;
  /** Set in a place: which copy, and the context it was drawn with. */
  place?: { id: string; at: Slot | Component; key: string; context?: ThreadRecord | ComponentContext };
  /** Set for a widget in a list's row: the list's id and the row's key. */
  row?: { list: string; key: string };
  /** A list's submit: the keys held. cmd is Command on macOS, Control elsewhere. */
  modifiers?: ("cmd" | "shift" | "alt" | "ctrl")[];
};

/** An app event the plugin hears (its manifest's glitched.events), or a timer. */
type AppEvent =
  | { kind: "app"; type: "thread.changed"; data: ThreadRecord | { id: string; deleted: true } }
  | { kind: "app"; type: "thread.moved"; data: { thread: string; from: string; to: string } }
  | { kind: "app"; type: "thread.message"; data: { thread: string; role: "user" | "assistant"; text: string } }
  | { kind: "app"; type: "thread.files.changed"; data: { thread: string; files: string[] } }
  | { kind: "app"; type: "setting.changed"; data: { id: string; value: unknown; was: unknown } }
  | { kind: "app"; type: "theme.changed"; data: { appearance: "light" | "dark" } }
  | { kind: "app"; type: "window.focused"; data: { focused: boolean } }
  | { kind: "app"; type: "place.shown"; data: { id: string; at: Slot; key: string; context?: ThreadRecord } }
  | { kind: "app"; type: "place.hidden"; data: { id: string; at: Slot; key: string } }
  | { kind: "app"; type: "command"; data: { id: string } }
  | { kind: "app"; type: "grant.changed"; data: { capability: string; granted: boolean } }
  | { kind: "timer"; id: string };

type GlitchedEvent = UIEvent | AppEvent;

/** The args of a command chosen in a menu of Glitched's (glitched.menus): thread.menu's, project.menu's, message.actions'. */
type MenuArgs =
  | { thread: ThreadRecord }
  | { project: { name: string; /** With threads:read. */ workspace?: string; /** With threads:read. */ machine?: string } }
  | { thread: ThreadRecord; message: { role: "user" | "assistant"; /** With messages:read. */ text?: string } };

/** What init and update answer. Leave state out to keep it. */
interface Update<S> {
  state?: S;
}

/** Glitched's slots. Stable ones keep their name and context across updates. */
type Slot = "thread.header" | "thread.row" | "sidebar.footer";

/**
 * Glitched's components: a place with `replaces: true` at one draws it in
 * place of the built-in plugin, once ui.replace is granted. Needs
 * contractVersion 2. approval.card, question.card, settings.plugins,
 * settings.servers, settings.providers and confirm.destructive are off
 * limits: a plugin that names one doesn't load. composer.send needs
 * threads:write, composer.access threads:access, and composer.workspace,
 * thread.header.breadcrumb and thread.header.usage threads:read, as well.
 */
type Component =
  | "thread.header.actions"
  | "thread.panel.diff"
  | "thread.row.actions"
  | "sidebar.projects"
  | "hero.heading"
  | "thread.empty"
  | "settings.page.general"
  | "settings.page.appearance"
  | "settings.page.about"
  | "settings.page.keybindings"
  | "composer"
  | "composer.send"
  | "composer.access"
  | "composer.workspace"
  | "composer.model"
  | "timeline.message"
  | "message.toolbar"
  | "thread.header.breadcrumb"
  | "thread.header.usage";

/** The settings glitched.app.settings reads and sets, by settings.json's names. */
interface AppSettings {
  theme: string;
  accent: string;
  newThread: string;
  defaultModel: string;
  timestamps: string;
  confirmDelete: boolean;
  streaming: boolean;
  notifications: boolean;
}

/** The context of settings.page.general. */
interface GeneralPageContext {
  settings: AppSettings;
  /** What each choice may be. */
  choices: { newThread: string[]; defaultModel: string[]; timestamps: string[] };
}

/** The context of settings.page.appearance: the theme and the accent. */
interface AppearancePageContext {
  settings: AppSettings;
  /** The themes' names, System first. */
  themes: string[];
  /** Each accent's color as #rrggbb. */
  accents: { name: string; color: string }[];
}

/** The context of thread.empty, a thread with no messages. */
interface ThreadEmptyContext {
  thread: ThreadRecord;
}

/** The context of settings.page.about. hero.heading's is {}. */
interface AboutPageContext {
  version: string;
  toolkit: string;
  /** Check for updates runs. */
  checking: boolean;
  /** The repository's address. */
  source: string;
}

/** The context of settings.page.keybindings. */
interface KeybindingsPageContext {
  keybindings: { label: string; keys: string[] }[];
}

type ComponentContext =
  | HeaderActionsContext
  | DiffPanelContext
  | ThreadEmptyContext
  | GeneralPageContext
  | AppearancePageContext
  | AboutPageContext
  | KeybindingsPageContext
  | RowActionsContext
  | ProjectStripContext
  | ComposerContext
  | ComposerSendContext
  | ComposerAccessContext
  | ComposerModelContext
  | TimelineMessageContext
  | MessageToolbarContext
  | HeaderBreadcrumbContext
  | HeaderUsageContext
  | Record<string, never>;

/**
 * The context of timeline.message: which row a copy is and what it is
 * doing, never what it says, which transcript.body draws. Its layout must
 * place transcript.body once.
 */
interface TimelineMessageContext {
  /** The row's key, the same while it changes. */
  key: string;
  kind: "user" | "assistant" | "work" | "failed" | "error";
  /** A reply still coming in: its context stays the same till it ends. */
  streaming?: boolean;
  /** Under a message that hasn't simply been sent: queued, sending, cancelled, not sent. */
  note?: string;
  /** The message's send failed. */
  failed?: boolean;
  /** When you sent a message of yours (as the engine took it in, even while it waits, is held or was cancelled), or a reply's first words came, in ISO 8601; left out before the engine takes a message in and for other rows. */
  at?: string;
}

/**
 * The context of message.toolbar: which message's toolbar a copy is and
 * whose message it is, never what it says. Its actions are the natives
 * message.copy, message.edit, message.retry and message.item, and
 * message.time says when it was sent.
 */
interface MessageToolbarContext {
  /** The message's row's key. */
  key: string;
  role: "user" | "assistant";
  /** How many commands plugins put in message.actions, which message.item draws by index. */
  items?: number;
  /** When you sent the message (as the engine took it in), or the reply's first words came, in ISO 8601; left out before the engine takes a message in. */
  at?: string;
  /** How long the turn ran, in milliseconds, under its last reply; left out elsewhere and where its start wasn't seen. */
  took?: number;
}

/** The context of thread.header.breadcrumb, a child thread's way back up around its title: its layout must place the native thread.heading once. Needs threads:read. */
interface HeaderBreadcrumbContext {
  thread: ThreadRecord;
  /** The threads above it, the furthest first: each a thread.crumb's thread. */
  ancestors: { id: string; title: string }[];
  /** The header keeps to icons, beside a panel. */
  narrow: boolean;
}

/** The context of thread.header.usage, the header's pill of tokens. Needs threads:read. */
interface HeaderUsageContext {
  thread: ThreadRecord;
  /** The thread's own tokens. */
  tokens: number;
  /** Its and every thread's under it. */
  total: number;
}

/** The context of composer: its layout must place the native composer.input once. */
interface ComposerContext {
  thread: ThreadRecord;
}

/** The context of composer.send. Glitched keeps its own Stop beside it while the agent works. */
interface ComposerSendContext {
  thread: ThreadRecord;
  /** The composer holds something to send: composer.submit sends it. */
  draft: boolean;
  /** The agent runs a turn, which threads.interrupt stops. */
  working: boolean;
}

/** The context of composer.access. Glitched keeps its own sign of the access beside it. */
interface ComposerAccessContext {
  thread: ThreadRecord;
  /** The level in force, one of levels' names. */
  access: AccessLevel;
  /** What the access picker offers, which threads.setAccess takes. */
  levels: { name: AccessLevel; about: string }[];
}

/** The context of composer.workspace. In a draft, Glitched ends its menu with where the thread could start, Add project and Remove project. */
interface ComposerWorkspaceContext {
  thread: ThreadRecord;
  /** The workspace folder's name. */
  workspace: string;
  /** The machine's name, only while there is more than one. */
  machine?: string;
  /** A new thread's composer, whose workspace the user chooses from Glitched's choices at the end of a ghost menu. */
  draft: boolean;
}

type AccessLevel = "Ask for approval" | "Approve for me" | "Full access";

/**
 * The context of composer.model, the model picker: what its button shows
 * (part "button"), or its popover holds while open (part "panel"). A row of
 * a list with id "families" shows that family's models as it is pointed at
 * or clicked, and one of a list with id "models" chooses that model on a
 * click, where the picker offers it; keys are a family's key and a model's,
 * or, as copies made before there were keys have them, names.
 */
interface ComposerModelContext {
  thread: ThreadRecord;
  part: "button" | "panel";
  /** The thread's own, or in the draft the default a new thread takes: the model's name, and its key. */
  model: string;
  key: string;
  effort: string;
  fast: boolean;
  /** The family whose models the popover shows, by its provider. */
  familyKey: string;
  /** The only family a thread offers, its provider, or "" in the draft. */
  lockKey: string;
  /** familyKey's and lockKey's families' names. */
  family: string;
  lock: string;
  /** The families whose engines report models, and those models, in each engine's order. A model chosen that its engine no longer offers is listed too, about "No longer offered". */
  families: { key: string; name: string; about: string }[];
  /** key is "provider:id"; familyKey the family's key, family its name. */
  models: { key: string; name: string; short: string; about: string; familyKey: string; family: string }[];
  /** How tall Glitched's own popover draws a family's row and a model's, in points, from the interface font on this platform. */
  familyRowHeight: number;
  modelRowHeight: number;
  /** How tall the models' column may be, padding and all, past which its rows scroll: eight rows, less what the window has no room for above the button. */
  modelsMaxHeight: number;
}

/** The context of thread.row.actions, a pointed-at sidebar row's actions. */
interface RowActionsContext {
  thread: ThreadRecord;
  /** The section the row shows in. */
  section: "inbox" | "working" | "snoozed" | "settled";
  /** The times Glitched offers, until in RFC 3339. */
  snooze: { when: string; joke: string; emoji: string; until: string }[];
}

/** The context of sidebar.projects, the sidebar's strip of projects. */
interface ProjectStripContext {
  projects: ProjectRecord[];
  /** The sidebar shows only some projects. */
  filtered: boolean;
  /** Add project's folder picker is open. */
  picking: boolean;
  /** More than one machine: their names tell projects apart. */
  machines: boolean;
}

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

/** The context of thread.panel.diff: a copy for its header, one for its body. */
interface DiffPanelContext {
  thread: ThreadRecord;
  part: "header" | "body";
  /** The thread's files are on this machine. */
  local: boolean;
  /** The files the agent changed, in the order it first changed them. */
  files: { path: string; /** The path as the user reads it: from the workspace. */ name: string; add: number; del: number; reviewed: boolean; /** Its diff is read, which marking it needs. */ read: boolean }[];
  /** The file diff.body shows, "" with none. */
  shown: string;
  /** As shown: split falls back to unified while it doesn't fit. */
  layout: "unified" | "split";
  splitFits: boolean;
  splitWidth: number;
  wrap: boolean;
  /** There is an editor for diff.openFile. */
  canOpen: boolean;
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
/**
 * place draws the copies Glitched asks for in one call: a widget or null
 * each, in order. A copy's drawing may depend only on state, its own key
 * and context, and host.setting: when copies come or change and nothing
 * else does, items holds those copies alone, and the others keep their
 * drawings. Anything else (an event, a timer, a setting, a reload) asks for
 * every copy shown. null leaves a copy empty.
 */
interface PlaceArgs<S> {
  id: string;
  at: Slot | Component;
  state: S;
  /** The copies to draw now: every copy shown after an event, a timer, a setting or a reload, otherwise only those that came or changed. A component's copies have its context: HeaderActionsContext for thread.header.actions, DiffPanelContext for thread.panel.diff, and so on. */
  items: { key: string; context?: ThreadRecord | ComponentContext }[];
}
/** rows draws the rows of a list that Glitched shows: a widget or null each, in the order of keys; null for a key the state no longer has. A row is at most 200 widgets and 16 KiB. */
interface RowsArgs<S> extends InitArgs {
  /** The list's id. */
  list: string;
  /** Up to 200 items' keys. */
  keys: string[];
  state: S;
  /** Set for a list in a place: the copy it is in. */
  place?: { id: string; at: Slot | Component; key: string; context?: ThreadRecord | ComponentContext };
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
  app: {
    /** About's update check: the page shows it runs, then a toast says how it went. */
    checkForUpdates(): Promise<void>;
    settings: {
      get(): Promise<AppSettings>;
      /** A value its page offers, or it refuses. Needs settings:write. */
      set<K extends keyof AppSettings>(key: K, value: AppSettings[K]): Promise<void>;
    };
  };
  /** The diff panel. Each names the thread and, for a file, one it changed. */
  diff: {
    /** Needs ui.place. */
    set(thread: string, options: { shown?: string; layout?: "unified" | "split"; wrap?: boolean }): Promise<void>;
    /** Marks a read file reviewed, or not. Needs threads:read. */
    setReviewed(thread: string, path: string, reviewed?: boolean): Promise<void>;
    /** Opens it in the editor found, for a thread on this machine. Needs threads:read. */
    openFile(thread: string, path: string): Promise<void>;
  };
  settings: {
    /** Its settings by id, typed: a switch's boolean, a number's number. */
    get(): Promise<Record<string, string | number | boolean>>;
    status(setting: string, status: { badge?: string; tone?: "neutral" | "success" | "warning" | "danger"; text?: string } | null): Promise<void>;
  };
  clipboard: { write(text: string): Promise<void> };
  /** Naming a thread, a click opens it; that needs threads:read. */
  notify(title: string, body?: string, options?: { thread?: string }): Promise<void>;
  composer: {
    /** A new thread's draft in workspace, or, with a thread, the draft in its composer (needs threads:read). */
    draft(text: string, where?: string | { thread: string }): Promise<void>;
    /** Sends what the open composer holds, as Send does: the thread's, or a new thread's with "". Needs threads:write. */
    submit(thread: string): Promise<void>;
  };
  threads: {
    list(): Promise<ThreadRecord[]>;
    open(thread: string): Promise<void>;
    create(prompt: string, options?: { title?: string; workspace?: string }): Promise<{ thread: string }>;
    send(thread: string, text: string): Promise<{ message: string }>;
    /** Up to 200 bytes. Needs threads:write. */
    rename(thread: string, title: string): Promise<void>;
    /** Asks the user first, with Glitched's own confirm. Needs threads:write. */
    delete(thread: string): Promise<void>;
    /** Inbox, snoozed till until, or settled, with the row joke of that move (undo, snooze, settle) and its toast. Needs threads:write. */
    setDisposition(thread: string, disposition: Disposition, options?: { animation?: "snooze" | "settle" | "undo" }): Promise<void>;
    /** Needs threads:write. */
    markRead(thread: string): Promise<void>;
    /** Stops its agent, if one runs. Needs threads:write. */
    interrupt(thread: string): Promise<void>;
    /**
     * The model (the id of one of models.list, of the thread's provider),
     * effort (one of that model's efforts) and fast mode of its next turn. Never the access: naming it, or anything
     * else, is refused. Needs threads:write.
     */
    configure(thread: string, options: { model?: string; effort?: string; fast?: boolean }): Promise<void>;
    /** The access of its next turns, one the access picker offers; with "", the default for new threads. Needs threads:access. */
    setAccess(thread: string, access: AccessLevel): Promise<void>;
  };
  commands: {
    /** The command palette's actions, in its order. Needs commands. */
    list(): Promise<CommandRecord[]>;
    /** Does what choosing the action in the palette does, and closes it; only one list names now. Needs commands. */
    run(id: string): Promise<void>;
  };
  models: {
    /** The models the composer's picker offers, and the efforts. */
    list(): Promise<{ models: { provider: string; id: string; name: string; family: string; description: string; efforts: string[] }[]; efforts: string[] }>;
    /**
     * One answer from a model, with no tools, on this machine's engine.
     * Without provider, the provider of the model new threads take; without
     * model, a cheap one of that provider's. timeout is the seconds it may
     * take, 120 at most and by default. Needs models:ask. Fails with
     * err.code "unsupported" (the provider can't), "unavailable" (its CLI
     * isn't installed), "busy" (the plugin's last ask isn't answered),
     * "too_large" (over 200 KB) or "timeout".
     */
    ask(prompt: string, options?: { system?: string; provider?: string; model?: string; timeout?: number }): Promise<{ text: string; provider: string; model: string }>;
  };
  projects: {
    /** In the sidebar's order. Needs threads:read. */
    list(): Promise<ProjectRecord[]>;
    /** The sidebar shows only these; none, or every one, is no filter. Needs threads:read. */
    filter(keys: string[]): Promise<void>;
    /** Opens Add project's folder picker. Needs threads:write. */
    add(): Promise<void>;
    /** Asks the user first, with Glitched's own confirm. Needs threads:write. */
    remove(workspace: string, machine?: string): Promise<void>;
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

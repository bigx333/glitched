// Settings' General, Appearance, About and Keybindings pages, as Glitched
// draws them by default.
// It is a plugin like any other: copy it in Settings › Plugins, and the
// copy draws the pages in its place.

type State = { query: string };

function errorMessage(e: unknown): string {
  return e instanceof Error ? e.message : typeof e === "object" && e !== null && "message" in e ? String((e as { message: unknown }).message) : String(e);
}

function settingRow(title: string, description: string, control: Widget): Widget {
  return {
    type: "row",
    padding: [14, 16],
    gap: 16,
    align: "center",
    children: [
      {
        type: "column",
        grow: true,
        gap: 2,
        children: [
          { type: "text", text: title, weight: 500 },
          ...(description ? [{ type: "text" as const, text: description, fontSize: 12, style: "muted" as const }] : []),
        ],
      },
      control,
    ],
  };
}

function choice(id: string, _label: string, options: string[], value: string, width: number): Widget {
  return { type: "select", id, options, value, width, controlled: true, onChange: id };
}

function toggle(id: string, label: string, value: boolean): Widget {
  return { type: "toggle", id, style: "switch", label, value, controlled: true, onChange: id };
}

function general({ settings: s, choices }: GeneralPageContext): Widget {
  return {
    type: "column",
    gap: 20,
    children: [
      {
        type: "card",
        style: "list",
        title: "THREADS",
        children: [
          settingRow("New thread environment", "Where a new thread runs its agent.", {
            type: "tabs",
            id: "newThread",
            style: "control",
            label: "New thread environment",
            options: choices.newThread,
            value: s.newThread,
            controlled: true,
            onChange: "newThread",
          }),
          settingRow("Default model", "Used for new threads. Each thread can pick its own.", choice("defaultModel", "Default model", choices.defaultModel, s.defaultModel, 200)),
          settingRow("Confirm before deleting threads", "", toggle("confirmDelete", "Confirm before deleting threads", s.confirmDelete)),
        ],
      },
      {
        type: "card",
        style: "list",
        title: "INTERFACE",
        children: [
          settingRow("Timestamps", "How the sidebar shows when a thread last changed.", choice("timestamps", "Timestamps", choices.timestamps, s.timestamps, 160)),
          settingRow("Stream assistant messages", "Show replies as they're written, rather than once done.", toggle("streaming", "Stream assistant messages", s.streaming)),
          settingRow("Desktop notifications", "When a thread finishes or needs approval in the background.", toggle("notifications", "Desktop notifications", s.notifications)),
        ],
      },
    ],
  };
}

function appearance({ settings: s, themes, accents }: AppearancePageContext): Widget {
  return {
    type: "column",
    gap: 20,
    children: [
      {
        type: "card",
        style: "list",
        title: "THEME",
        children: [
          {
            type: "row",
            padding: 16,
            gap: 12,
            children: themes.map((theme, i): Widget => ({ type: "native", id: "theme" + i, name: "theme.card", props: { theme }, grow: true, onChange: "theme" })),
          },
        ],
      },
      {
        type: "card",
        style: "list",
        title: "ACCENT",
        children: [
          settingRow("Accent color", s.accent, {
            type: "row",
            gap: 10,
            children: accents.map((a, i): Widget => ({
              type: "swatch",
              id: "accent" + i,
              color: a.color,
              label: a.name,
              active: a.name === s.accent,
              onClick: { handler: "accent", payload: a.name },
            })),
          }),
        ],
      },
    ],
  };
}

function about(ctx: AboutPageContext): Widget {
  const check: Widget = ctx.checking
    ? { type: "row", gap: 8, children: [{ type: "spinner" }, { type: "text", text: "Checking…", style: "muted" }] }
    : { type: "button", id: "check", style: "bordered", icon: "refresh", label: "Check for updates", onClick: "check" };
  return {
    type: "column",
    gap: 20,
    children: [
      {
        type: "column",
        align: "center",
        justify: "center",
        gap: 10,
        padding: [24, 0],
        children: [
          { type: "image", src: "app:icon", width: 96, height: 96 },
          { type: "text", text: "Glitched", fontSize: 20, weight: 650 },
          { type: "text", text: ctx.version + " · native prototype built with " + ctx.toolkit, style: "muted" },
        ],
      },
      {
        type: "card",
        style: "list",
        children: [
          settingRow("Updates", "Checks for a new version when the app starts.", check),
          settingRow("Source", ctx.source.replace(/^https:\/\//, ""), { type: "link", text: "Open on GitHub", href: ctx.source }),
        ],
      },
    ],
  };
}

function keybindings(ctx: KeybindingsPageContext, query: string): Widget {
  const q = query.toLowerCase();
  const shown = ctx.keybindings.filter((k) => k.label.toLowerCase().includes(q));
  const rows: Widget[] = shown.map((k, i) => ({
    type: "row",
    id: "key" + i,
    padding: [11, 16],
    align: "center",
    children: [
      { type: "text", text: k.label, grow: true },
      { type: "keys", keys: k.keys },
    ],
  }));
  return {
    type: "column",
    gap: 20,
    children: [
      { type: "input", id: "search", style: "search", placeholder: "Search keybindings", value: query, onChange: "search" },
      { type: "card", style: "list", children: rows.length > 0 ? rows : [{ type: "text", id: "none", text: "No keybindings match.", style: "muted", padding: 16 }] },
    ],
  };
}

export function init(): Update<State> {
  return { state: { query: "" } };
}

type SettingsContext = GeneralPageContext | AppearancePageContext | AboutPageContext | KeybindingsPageContext;

export function place({ at, state, items }: PlaceArgs<State, SettingsContext>): (Widget | null)[] {
  return items.map((it) => {
    if (!it.context) return null;
    switch (at) {
      case "settings.page.general":
        return general(it.context as GeneralPageContext);
      case "settings.page.appearance":
        return appearance(it.context as AppearancePageContext);
      case "settings.page.about":
        return about(it.context as AboutPageContext);
    }
    return keybindings(it.context as KeybindingsPageContext, state.query);
  });
}

export async function update({ state, event }: UpdateArgs<State, SettingsContext>): Promise<Update<State>> {
  if (event.kind !== "ui") return {};
  switch (event.handler) {
    case "search":
      return { state: { ...state, query: String(event.value ?? "") } };
    case "theme":
    case "newThread":
    case "defaultModel":
    case "timestamps":
      await glitched.app.settings.set(event.handler, String(event.value ?? "")).catch((e: unknown) => glitched.toast("Settings", errorMessage(e)));
      return {};
    case "confirmDelete":
    case "streaming":
    case "notifications":
      await glitched.app.settings.set(event.handler, event.value === true).catch((e: unknown) => glitched.toast("Settings", errorMessage(e)));
      return {};
    case "accent":
      await glitched.app.settings.set("accent", String(event.payload ?? "")).catch((e: unknown) => glitched.toast("Settings", errorMessage(e)));
      return {};
    case "check":
      await glitched.app.checkForUpdates().catch((e: unknown) => glitched.toast("About", errorMessage(e)));
      return {};
  }
  return {};
}

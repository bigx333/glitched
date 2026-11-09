// Snippets: prompts you reuse. They live in the plugin's storage, so they
// outlast the app; using one drafts it in the composer, or starts a thread
// with it, which needs the threads:write grant. What it does in the app it
// does with glitched's calls, which answer, or fail with err.code
// "denied" without a grant.

function saved() {
  return host.storage.get("snippets") ?? [];
}

function store(snippets) {
  host.storage.set("snippets", snippets);
}

function ordered(snippets) {
  return [...snippets].sort((a, b) => Number(b.pinned) - Number(a.pinned));
}

// recent are the latest threads, or why they can't be shown.
async function recent() {
  try {
    return { threads: (await glitched.threads.list()).slice(0, 5), threadsError: null };
  } catch (e) {
    const why = e.code === "denied" ? "Grant “threads:read” to Snippets in Settings › Plugins to see your threads." : e.message;
    return { threads: null, threadsError: why };
  }
}

export async function init() {
  return { state: { snippets: saved(), adding: false, error: null, notice: null, ...(await recent()) } };
}

function form(state) {
  if (!state.adding) {
    return { type: "button", id: "new", label: "New snippet", style: "primary", onClick: "new" };
  }
  return {
    type: "card",
    id: "form",
    title: "New snippet",
    children: [
      { type: "input", id: "title", label: "Title", placeholder: "Review this branch", onSubmit: "save" },
      { type: "input", id: "prompt", label: "Prompt", placeholder: "What the agent should do", multiline: true },
      {
        type: "select",
        id: "mode",
        label: "Using it",
        value: "draft",
        options: [
          { value: "draft", label: "Drafts it in the composer" },
          { value: "run", label: "Starts a thread right away" },
        ],
      },
      { type: "toggle", id: "pinned", label: "Pin to the top", value: false },
      ...(state.error ? [{ type: "text", id: "form-error", text: state.error, style: "error" }] : []),
      {
        type: "row",
        gap: 8,
        children: [
          { type: "button", id: "save", label: "Save", style: "primary", onClick: "save" },
          { type: "button", id: "cancel", label: "Cancel", onClick: "cancel" },
        ],
      },
    ],
  };
}

function list(state) {
  if (state.snippets.length === 0) {
    return { type: "card", id: "list", title: "Saved", children: [{ type: "text", id: "empty", text: "No snippets yet.", style: "muted" }] };
  }
  return {
    type: "card",
    id: "list",
    title: "Saved",
    children: ordered(state.snippets).map((s) => ({
      type: "row",
      id: "snippet:" + s.id,
      gap: 12,
      children: [
        {
          type: "column",
          grow: true,
          gap: 2,
          children: [
            {
              type: "row",
              gap: 6,
              children: [
                { type: "text", text: s.title, style: "heading" },
                ...(s.pinned ? [{ type: "badge", text: "Pinned", style: "accent" }] : []),
                ...(s.mode === "run" ? [{ type: "badge", text: "Runs", style: "warning" }] : []),
              ],
            },
            { type: "text", text: s.prompt, style: "muted" },
          ],
        },
        { type: "button", id: "use:" + s.id, label: s.mode === "run" ? "Run" : "Draft", onClick: { handler: "use", payload: s.id } },
        { type: "button", id: "copy:" + s.id, label: "Copy", onClick: { handler: "copy", payload: s.id } },
        { type: "button", id: "delete:" + s.id, label: "Delete", style: "danger", onClick: { handler: "delete", payload: s.id } },
      ],
    })),
  };
}

function threads(state) {
  let body;
  if (state.threadsError) {
    body = [{ type: "text", id: "threads-error", text: state.threadsError, style: "error" }];
  } else if (state.threads === null) {
    body = [{ type: "spinner", id: "threads-loading" }];
  } else if (state.threads.length === 0) {
    body = [{ type: "text", id: "threads-none", text: "No threads yet.", style: "muted" }];
  } else {
    body = state.threads.map((t) => ({
      type: "row",
      id: "thread:" + t.id,
      gap: 8,
      children: [
        { type: "badge", text: t.status },
        { type: "text", text: t.title },
        { type: "spacer" },
        { type: "button", id: "open:" + t.id, label: "Open", onClick: { handler: "open", payload: t.id } },
      ],
    }));
  }
  return {
    type: "card",
    id: "threads",
    title: "Recent threads",
    children: [...body, { type: "button", id: "refresh", label: "Refresh", onClick: "refresh" }],
  };
}

export function view({ state }) {
  const notice = state.notice ? [{ type: "text", id: "notice", text: state.notice, style: "success" }] : [];
  if (state.error && !state.adding) notice.push({ type: "text", id: "error", text: state.error, style: "error" });
  return {
    title: "Snippets",
    subtitle: `${state.snippets.length} saved`,
    body: { type: "column", gap: 16, children: [...notice, form(state), list(state), threads(state)] },
  };
}

function find(state, id) {
  return state.snippets.find((s) => s.id === id);
}

async function use(state, s) {
  if (s.mode !== "run") {
    await glitched.composer.draft(s.prompt);
    return {};
  }
  try {
    await glitched.threads.create(s.prompt, { title: s.title });
    return { state: { ...state, error: null, notice: "Started a thread.", ...(await recent()) } };
  } catch (e) {
    const why = e.code === "denied" ? "Grant “threads:write” to Snippets in Settings › Plugins to start threads." : e.message;
    return { state: { ...state, notice: null, error: why } };
  }
}

export async function update({ state, event, values }) {
  if (event.kind === "app" && event.type === "grant.changed") {
    return { state: { ...state, error: null, ...(await recent()) } }; // granted or taken back: ask again
  }
  if (event.kind !== "ui") return {};
  switch (event.handler) {
    case "new":
      return { state: { ...state, adding: true, error: null, notice: null } };
    case "cancel":
      return { state: { ...state, adding: false, error: null } };
    case "save": {
      const title = String(values.title ?? "").trim();
      const prompt = String(values.prompt ?? "").trim();
      if (!prompt) return { state: { ...state, error: "A snippet needs a prompt." } };
      const id = String((host.storage.get("next") ?? 0) + 1);
      host.storage.set("next", Number(id));
      const snippet = { id, title: title || prompt.slice(0, 40), prompt, mode: values.mode, pinned: values.pinned === true };
      const snippets = [...state.snippets, snippet];
      store(snippets);
      return { state: { ...state, snippets, adding: false, error: null, notice: `Saved “${snippet.title}”.` } };
    }
    case "delete": {
      const snippets = state.snippets.filter((s) => s.id !== event.payload);
      store(snippets);
      return { state: { ...state, snippets, notice: null } };
    }
    case "copy": {
      const s = find(state, event.payload);
      if (s) {
        await glitched.clipboard.write(s.prompt);
        await glitched.toast("Copied", s.title);
      }
      return {};
    }
    case "use": {
      const s = find(state, event.payload);
      return s ? use(state, s) : {};
    }
    case "refresh":
      return { state: { ...state, ...(await recent()) } };
    case "open":
      await glitched.threads.open(event.payload);
      return {};
  }
  return {};
}

export async function command({ id }) {
  switch (id) {
    case "new":
      await glitched.pages.open("home");
      return;
    case "draft-pinned": {
      const s = ordered(saved()).find((s) => s.pinned);
      if (!s) await glitched.toast("No pinned snippet");
      else await glitched.composer.draft(s.prompt);
      return;
    }
  }
  throw new Error("unknown command: " + id);
}

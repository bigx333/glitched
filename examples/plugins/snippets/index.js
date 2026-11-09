// Snippets: prompts you reuse. They live in the plugin's storage, so they
// outlast the app; using one drafts it in the composer, or starts a thread
// with it, which needs the threads:write grant.

function saved() {
  return host.storage.get("snippets") ?? [];
}

function store(snippets) {
  host.storage.set("snippets", snippets);
}

function ordered(snippets) {
  return [...snippets].sort((a, b) => Number(b.pinned) - Number(a.pinned));
}

export function init() {
  return {
    state: { snippets: saved(), adding: false, error: null, notice: null, threads: null, threadsError: null },
    effects: [{ type: "threads.list", id: "threads" }],
  };
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

function onUI(state, event, values) {
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
      return s ? { effects: [{ type: "clipboard.write", text: s.prompt }, { type: "toast", title: "Copied", description: s.title }] } : {};
    }
    case "use": {
      const s = find(state, event.payload);
      if (!s) return {};
      if (s.mode === "run") {
        return { state: { ...state, notice: null }, effects: [{ type: "threads.create", id: "run", prompt: s.prompt, title: s.title }] };
      }
      return { effects: [{ type: "composer.draft", id: "draft", text: s.prompt }] };
    }
    case "refresh":
      return { state: { ...state, threads: null, threadsError: null }, effects: [{ type: "threads.list", id: "threads" }] };
    case "open":
      return { effects: [{ type: "threads.open", id: "open", thread: event.payload }] };
  }
  return {};
}

function onEffect(state, event) {
  switch (event.id) {
    case "threads":
      if (!event.ok) {
        const why = event.denied ? "Grant “threads:read” to Snippets in Settings › Plugins to see your threads." : event.error;
        return { state: { ...state, threads: null, threadsError: why } };
      }
      return { state: { ...state, threads: event.result.slice(0, 5), threadsError: null } };
    case "run":
      if (!event.ok) {
        const why = event.denied ? "Grant “threads:write” to Snippets in Settings › Plugins to start threads." : event.error;
        return { state: { ...state, notice: null, error: why } };
      }
      return { state: { ...state, error: null, notice: "Started a thread." }, effects: [{ type: "threads.list", id: "threads" }] };
  }
  return event.ok ? {} : { state: { ...state, error: event.error } };
}

export function update({ state, event, values }) {
  if (event.kind === "app" && event.type === "grant.changed") {
    // Granted or taken back in Settings: ask again.
    return { state: { ...state, threads: null, threadsError: null, error: null }, effects: [{ type: "threads.list", id: "threads" }] };
  }
  return event.kind === "ui" ? onUI(state, event, values) : onEffect(state, event);
}

export function command({ id }) {
  switch (id) {
    case "new":
      return { effects: [{ type: "page.open", page: "home" }] };
    case "draft-pinned": {
      const s = ordered(saved()).find((s) => s.pinned);
      if (!s) return { effects: [{ type: "toast", title: "No pinned snippet" }] };
      return { effects: [{ type: "composer.draft", text: s.prompt }] };
    }
  }
  throw new Error("unknown command: " + id);
}

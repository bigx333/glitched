// The preview browser's chrome. The page itself stays the native web view;
// browse.Page and the agent browser stay core.
export function init() {
  return { state: { address: {} } };
}

const click = (handler, payload) => payload === undefined ? handler : { handler, payload };
const iconButton = (id, icon, tooltip, handler, disabled) => ({
  type: "button", id, style: "ghost", tooltip, disabled: !!disabled, padding: [5],
  onClick: handler, children: [{ type: "icon", id: id + "-icon", name: icon, fontSize: 15 }],
});

export function place({ state, items }) {
  return items.map(({ context }) => {
    if (!context) return { type: "row" };
    if (context.part === "header") return header(state, context);
    if (context.part === "tabs") return tabs(context);
    return body(context);
  });
}

function header(state, ctx) {
  const id = String(ctx.active);
  const typed = state.address[id];
  const address = typed === undefined ? ctx.draft || ctx.url : typed;
  return { type: "row", height: ctx.barHeight, padding: [0, 8 + ctx.barRight, 0, 10], gap: 4, align: "center", children: [
    iconButton("back", "arrow-left", "Back", click("back", ctx.active), !ctx.canBack),
    iconButton("forward", "arrow-right", "Forward", click("forward", ctx.active), !ctx.canForward),
    ctx.loading
      ? iconButton("stop", "close", "Stop", click("stop", ctx.active), false)
      : iconButton("reload", "refresh", "Reload  ⌘R", click("reload", ctx.active), !ctx.page && !ctx.error),
    { type: "input", id: "address", label: "Address", placeholder: "Type an address", value: address, controlled: true, focus: ctx.focus, grow: true, onChange: "address", onSubmit: "navigate" },
    iconButton("new-tab", "plus", "New tab  ⌘T", "newTab", false),
    iconButton("external", "square-share-line", "Open in browser", "external", !ctx.url),
    iconButton("close-preview", "close", "Close preview", "closePreview", false),
  ] };
}

function tabs(ctx) {
  return { type: "row", padding: [0, 8, 6, 8], gap: 4, children: ctx.tabs.map((t) => ({
    type: "row", id: "tab:" + t.id, height: 28, grow: true, radius: 7, gap: 4, padding: [0, 3, 0, 10], align: "center",
    background: t.active ? "$surface" : "", onClick: click("activate", t.id),
    children: [
      ...(t.loading ? [{ type: "spinner", id: "loading:" + t.id }] : []),
      { type: "text", text: t.label, fontSize: 12, singleLine: true, color: t.active ? "$text" : "$text.muted" },
      { type: "spacer" },
      { type: "button", id: "close:" + t.id, style: "ghost", tooltip: "Close tab", padding: [4], onClick: click("closeTab", t.id), children: [{ type: "icon", name: "close", fontSize: 11 }] },
    ],
  })) };
}

function body(ctx) {
  if (ctx.page) return { type: "native", id: "webview", name: "browser.webview" };
  if (ctx.error) return centered([{ type: "text", text: "The preview could not start: " + ctx.error, color: "$text.muted", padding: [24] }]);
  if (!ctx.url) return centered([
    { type: "icon", name: "globe", fontSize: 28, color: "$text.muted" },
    { type: "text", text: "Type an address above", color: "$text.muted" },
    { type: "button", id: "dev", label: "Open " + ctx.devUrl, onClick: click("dev", ctx.active), children: [{ type: "text", text: (ctx.devUrl || "").replace(/^http:\/\//, ""), fontSize: 12 }] },
  ]);
  return centered([{ type: "text", text: "The preview shows here, in the app's window.", color: "$text.muted" }]);
}

function centered(children) {
  return { type: "column", grow: true, align: "center", justify: "center", gap: 10, background: "$surface", children };
}

export async function update({ state, event }) {
  if (event.kind !== "ui" || !event.place) return {};
  const ctx = event.place.context || {};
  const tab = event.payload ?? ctx.active;
  switch (event.handler) {
    case "address": {
      const address = { ...state.address, [String(ctx.active)]: String(event.value ?? "") };
      return { state: { ...state, address } };
    }
    case "navigate":
      await glitched.browser.navigate(ctx.active, event.value ?? state.address[String(ctx.active)] ?? ctx.draft);
      return {};
    case "back": await glitched.browser.back(tab); return {};
    case "forward": await glitched.browser.forward(tab); return {};
    case "reload": await glitched.browser.reload(tab); return {};
    case "stop": await glitched.browser.stop(tab); return {};
    case "newTab": await glitched.browser.newTab(""); return {};
    case "external": if (ctx.url) await glitched.openURL(ctx.url); return {};
    case "closePreview": await glitched.panels.toggle("preview", false); return {};
    case "activate": await glitched.browser.activateTab(tab); return {};
    case "closeTab": await glitched.browser.closeTab(tab); return {};
    case "dev": await glitched.browser.navigate(tab, ctx.devUrl); return {};
  }
  return {};
}

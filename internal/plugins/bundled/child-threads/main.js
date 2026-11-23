// A child thread's parts of the header, as Glitched draws them by default.
// The breadcrumb is the way back up: each thread above the one shown, the
// furthest first, as the native thread.crumb (the button Glitched draws,
// which goes back to it), with a chevron after each, beside a panel only
// the parent's, then the native thread.heading, the thread's title, which
// it must place once. The usage pill, after the workspace's, meters how
// full the thread's context is once its agent said, and before that is
// the thread's tokens, and with children, everything under it rolled up.
// Copy it in Settings › Plugins to change them.

// tokens is n as Glitched writes it: whole under a thousand, else in
// thousands to a tenth, a tie (as 1,250) to the even tenth.
function tokens(n) {
  if (n < 1000) return String(n);
  return ((n % 1000 === 250 ? n - 50 : n) / 1000).toFixed(1) + "k";
}

function breadcrumb({ ancestors, narrow }) {
  const up = narrow && ancestors.length > 1 ? ancestors.slice(-1) : ancestors;
  return {
    type: "row",
    gap: 8,
    children: [
      ...up.flatMap((a) => [
        { type: "native", id: "crumb-" + a.id, name: "thread.crumb", props: { thread: a.id } },
        { type: "icon", name: "alt-arrow-right", fontSize: 11, color: "$text.faint" },
      ]),
      { type: "native", id: "title", name: "thread.heading" },
    ],
  };
}

// contextTokens is n as the meter writes it: whole under a thousand, else
// in whole thousands, else in millions to a tenth, a half rounded up.
function contextTokens(n) {
  if (n < 1000) return String(n);
  const k = Math.floor((n + 500) / 1000);
  if (k < 1000) return k + "k";
  const tenths = Math.floor((n + 50000) / 100000);
  return tenths % 10 === 0 ? tenths / 10 + "m" : Math.floor(tenths / 10) + "." + (tenths % 10) + "m";
}

// usage is the thread's tokens, with its children's rolled up, until its
// agent says how full the context is; then a meter of it, amber from 75%
// and red from 90%, with the tokens in its tooltip.
function usage({ tokens: own, total, context: c }) {
  const children = total !== own;
  if (!c) {
    if (!children) return { type: "badge", style: "pill", text: tokens(own) + " tokens", tooltip: "This thread's tokens" };
    return {
      type: "badge",
      style: "pill",
      text: tokens(own) + " · " + tokens(total) + " with children",
      tooltip: "This thread " + tokens(own) + ", its child threads " + tokens(total - own),
    };
  }
  const tip = children ? "This thread " + tokens(own) + ", its child threads " + tokens(total - own) : "This thread's tokens " + tokens(own);
  const used = Math.max(0, c.used || 0);
  if (c.stale) {
    return { type: "badge", style: "pill", text: "Compacted", value: 0, color: "$text.faint", tooltip: "Context compacted. Its size shows after the next reply.\n" + tip };
  }
  if (!(c.window > 0)) {
    return { type: "badge", style: "pill", text: contextTokens(used) + " in context", tooltip: contextTokens(used) + " tokens in context. Context limit not reported.\n" + tip };
  }
  const share = used / c.window;
  const pct = Math.floor((used * 100 + Math.floor(c.window / 2)) / c.window);
  const node = {
    type: "badge",
    style: "pill",
    text: pct + "%",
    value: Math.round(Math.min(1, share) * 1e4) / 1e4,
    tooltip: "Context window " + contextTokens(used) + " / " + contextTokens(c.window) + " tokens, " + Math.max(0, 100 - pct) + "% left\n" + tip,
  };
  if (pct >= 90) node.color = "$danger";
  else if (pct >= 75) node.color = "$warning";
  return node;
}

export function place({ at, items }) {
  return items.map(({ context }) => {
    if (!context) return null;
    return at === "thread.header.usage" ? usage(context) : breadcrumb(context);
  });
}

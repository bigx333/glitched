// A child thread's parts of the header, as Glitched draws them by default.
// The breadcrumb is the way back up: each thread above the one shown, the
// furthest first, as the native thread.crumb (the button Glitched draws,
// which goes back to it), with a chevron after each, beside a panel only
// the parent's, then the native thread.heading, the thread's title, which
// it must place once. The usage pill,
// after the workspace's, is the thread's tokens, and with children,
// everything under it rolled up. Copy it in Settings › Plugins to change
// them.

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

function usage({ tokens: own, total }) {
  if (total === own) return { type: "badge", style: "pill", text: tokens(own) + " tokens", tooltip: "This thread's tokens" };
  return {
    type: "badge",
    style: "pill",
    text: tokens(own) + " · " + tokens(total) + " with children",
    tooltip: "This thread " + tokens(own) + ", its child threads " + tokens(total - own),
  };
}

export function place({ at, items }) {
  return items.map(({ context }) => {
    if (!context) return null;
    return at === "thread.header.usage" ? usage(context) : breadcrumb(context);
  });
}

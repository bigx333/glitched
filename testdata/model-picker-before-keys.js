// The model picker, as Glitched draws it by default. Its button shows the
// model's tile and name, the effort in its color, fast mode's lightning
// while it's on, and a chevron. Its popover lists the families of models
// beside the models of the one pointed at, as a submenu, over the effort
// slider with fast mode's switch. Glitched keeps the button, its glow, the
// popover, and what a row does: pointing at a family shows its models, and
// a click on a model chooses it. Copy it in Settings › Plugins, and the
// copy draws the picker in its place.

const GAP = 1;
const PAD = 6;

export function init() {
  return { state: {} };
}

const native = (name, props) => ({ type: "native", name, ...(props ? { props } : {}) });

function button(ctx) {
  const children = [
    native("model.tile", { size: 18 }),
    { type: "text", text: ctx.model, fontSize: 12, singleLine: true },
    native("model.level"),
  ];
  if (ctx.fast) children.push(native("model.zap"));
  children.push({ type: "icon", name: "alt-arrow-down", fontSize: 11 });
  return { type: "row", gap: 6, children };
}

// The models' column is as tall as the longest family's list, whichever
// shows: the popover keeps its size as the pointer moves between families.
function modelsHeight(ctx) {
  const counts = new Map();
  for (const m of ctx.models) counts.set(m.family, (counts.get(m.family) ?? 0) + 1);
  const n = Math.max(0, ...counts.values());
  return 2 * PAD + n * ctx.modelRowHeight + Math.max(0, n - 1) * GAP;
}

function panel(ctx) {
  const families = ctx.families.filter((f) => !ctx.lock || f.name === ctx.lock);
  const models = ctx.models.filter((m) => m.family === ctx.family);
  return {
    type: "column",
    children: [
      {
        type: "row",
        align: "stretch",
        children: [
          {
            type: "column",
            width: 150,
            padding: [PAD],
            children: [
              {
                type: "list",
                id: "families",
                label: "Families",
                noKeys: true,
                height: 400,
                rowHeight: ctx.familyRowHeight,
                gap: GAP,
                value: families.some((f) => f.name === ctx.family) ? ctx.family : undefined,
                controlled: true,
                items: families.map((f) => ({ key: f.name, label: f.name + " " + f.about })),
              },
            ],
          },
          { type: "divider", style: "vertical" },
          {
            type: "column",
            grow: true,
            height: modelsHeight(ctx),
            padding: [PAD],
            children: [
              {
                type: "list",
                id: "models",
                label: "Models",
                noKeys: true,
                height: 400,
                rowHeight: ctx.modelRowHeight,
                gap: GAP,
                value: models.some((m) => m.name === ctx.model) ? ctx.model : undefined,
                controlled: true,
                items: models.map((m) => ({ key: m.name, label: m.short + " " + m.about, version: m.name === ctx.model ? "chosen" : "" })),
              },
            ],
          },
        ],
      },
      { type: "divider" },
      native("model.effort"),
    ],
  };
}

export function place({ items: copies }) {
  return copies.map((it) => {
    if (!it.context) return null;
    return it.context.part === "button" ? button(it.context) : panel(it.context);
  });
}

export function rows({ list, keys, place }) {
  const ctx = place.context;
  return keys.map((key) => {
    if (list === "families") {
      const f = ctx.families.find((f) => f.name === key);
      if (!f) return null;
      return {
        type: "row",
        grow: true,
        gap: 8,
        padding: [6, 8],
        children: [
          native("model.tile", { family: f.name, size: 22 }),
          {
            type: "column",
            grow: true,
            gap: 1,
            children: [
              { type: "text", text: f.name, weight: 500, color: "$text" },
              { type: "text", text: f.about, fontSize: 11, color: "$text.muted" },
            ],
          },
          { type: "icon", name: "alt-arrow-right", fontSize: 11 },
        ],
      };
    }
    const m = ctx.models.find((m) => m.name === key);
    if (!m) return null;
    const children = [
      {
        type: "column",
        grow: true,
        gap: 1,
        children: [
          { type: "text", text: m.short, weight: 500, color: "$text", singleLine: true },
          { type: "text", text: m.about, fontSize: 11.5, color: "$text.muted", singleLine: true },
        ],
      },
    ];
    if (m.name === ctx.model) children.push({ type: "icon", name: "check", fontSize: 14, color: "$accent" });
    return { type: "row", grow: true, gap: 10, padding: [7, 10], children };
  });
}

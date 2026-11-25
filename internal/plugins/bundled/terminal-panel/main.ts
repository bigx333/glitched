// The native terminal body is libghostty's live terminal. The plugin owns
// the chrome around it: its icon, the shell's title, and Close.
export function place({ items }: PlaceArgs<Record<string, never>, TerminalPanelContext>): (Widget | null)[] {
  return items.map(({ context }) => {
    if (!context) return null;
    if (context.part === "body") return { type: "native", id: "body", name: "terminal.body", grow: true };
    return {
      type: "row",
      height: 30,
      padding: [4, 8, 4, 12],
      gap: 6,
      align: "center",
      children: [
        { type: "icon", id: "icon", name: "terminal", fontSize: 12, color: "$text.muted" },
        { type: "text", id: "title", text: context.title || "zsh", fontSize: 11.5, color: "$text.muted", singleLine: true },
        { type: "row", grow: true },
        { type: "button", id: "close", style: "ghost", icon: "close", compact: true, tooltip: "Close terminal", onClick: "close" },
      ],
    };
  });
}

export async function update({ event }: UpdateArgs<Record<string, never>, TerminalPanelContext>): Promise<Update<Record<string, never>>> {
  if (event.kind === "ui" && event.handler === "close") await glitched.panels.toggle("terminal", false);
  return {};
}

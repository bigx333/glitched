# Command palette

The palette is a searchable list of actions: new threads, the panels, light/dark, every Settings page, and the threads. The user opens it from `Search` or `⌘K`, filters it by typing, and runs a row to perform that action.

## Sub-features

- `palette-open` opens it with an `Actions` section (`New thread ⌘N`, `New thread in <project>`, `Switch model`, `Toggle diff panel ⌘D`, `Toggle terminal ⌘J`, `Toggle preview ⇧⌘P`, `Toggle sidebar ⌘B`, `Toggle light / dark ⇧⌘L`, `Settings › …`).
- `palette-filter` narrows the rows to the query. A leading `>` limits it to actions.
- `palette-run` runs the chosen row and closes the palette.

## How to get to it (user POV)

- `Search` in the sidebar, `⌘K`, or Thread › Search… in the menu bar.
- Type to filter. Click a row, or use ↑/↓ and Return.

## Driving it with glv

Preconditions:

- Baseline state.

- **Open.** Run `$G press Search AXButton`, then `$G tree AXRow`. The rows start with `Actions New thread ⌘N`. Run `$G shot palette-open`.
- **Filter.** Run `$G set AXTextField "appear"`, then `$G tree "Settings ›"`. Only `Settings › Appearance` remains.
- **Run.** Run `$G press "Settings › Appearance" AXButton`, then `$G wait "Theme, accent color" 10` and `$G shot palette-ran`. The Appearance settings page shows, and `$G has "Actions"` no longer matches. Proven on 2026-10-26.
- **Return.** Run `$G press "Back to app" AXButton`.

## Gotchas

- `$G set AXTextField` targets the first text field. Run it only while the palette is open, or it may hit another field.
- ↑/↓/Return and `⌘K` need the window in front. Pressing the row's button is the background-safe route.
- Thread rows appear only once threads exist. Seed one through [start a thread](./start-thread.md) to verify jumping to a thread.

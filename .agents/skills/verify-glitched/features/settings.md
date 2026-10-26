# Settings and appearance

Settings replaces the chat with pages listed in its own sidebar: General, Appearance, Providers, Servers, Keybindings, Archived threads, About. Appearance picks the theme (System, Light, Dark, Mocha, Dawn), the accent color and the glass tint. Themes also live in the menu bar and the palette.

## Sub-features

- `settings-open` opens Settings › General (`Defaults for new threads and how the app behaves.`).
- `settings-nav` switches pages from the Settings sidebar, and `Back to app` returns to the chat.
- `settings-theme` applies a theme. The window repaints in its colors.
- `settings-general` toggles General's switches (`Stream assistant messages`, `Desktop notifications`, `Confirm before deleting threads`).
- `settings-archived` lists archived threads with restore (see [thread menu](./thread-menu.md)).

## How to get to it (user POV)

- `Settings` at the bottom of the sidebar, `⌘,`, or Thread › Settings… in the menu bar.
- The palette's `Settings › <page>` rows.
- Themes: Appearance › Theme, View › Appearance › <theme> in the menu bar, or the palette's `Toggle light / dark` (`⇧⌘L`).

## Driving it with glv

Preconditions:

- Baseline state.

- **Open from the menu bar.** Run `$G menu "Settings"`, then `$G wait "Defaults for new threads" 10`. `$G tree AXButton` starts `Back to app`, `General`, `Appearance`, …. Proven on 2026-10-26.
- **Open from the sidebar.** Run `$G press "Back to app" AXButton`, then `$G press Settings AXButton`. The same General page shows.
- **Appearance.** Run `$G press Appearance AXButton`, then `$G wait "THEME" 10` and `$G shot settings-before`.
- **Theme.** Run `$G press Mocha AXButton`, then `$G shot settings-mocha`. Compare the two screenshots: the background and sidebar take Catppuccin Mocha colors.
- **Theme from the menu bar.** Run `$G menu Dawn`, then `$G shot settings-dawn`. The window turns light (Rose Pine Dawn).
- **Back.** Run `$G press "Back to app" AXButton`. `$G has "What should we build?"` succeeds.

## Gotchas

- Settings aren't saved to disk: they reset on every launch. "Persists across restart" isn't a behavior to verify.
- `$G press Settings` alone can match `Settings › …` palette rows while the palette is open. Close the palette first.
- Light and Dark theme buttons may be named by their swatch. Run `$G tree THEME` and check the buttons near it before assuming labels.
- Servers pairing needs a second engine with a token from `glitched pair`. It isn't seeded, so report it as not verified unless you start one (`$G cli` drives only this run's engine).

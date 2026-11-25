// Themes adds no UI of its own: plugin.json's glitched.themes are its
// contribution. This module only keeps the plugin runtime valid.
export function init(): Update<Record<string, never>> {
  return { state: {} };
}

// The built-in picker is the native control, keeping Full access behind
// Glitched's hold-to-grant gesture and haptics. A copy can arrange it, or
// draw its own simpler picker and call threads.setAccess.
export function place({ items }: PlaceArgs<Record<string, never>, ComposerAccessContext>): Widget[] {
  return items.map(() => ({ type: "native", id: "access", name: "composer.access.picker" }));
}

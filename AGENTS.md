# AGENTS.md

## Prefer Plugins Over Core

When implementing new features or functionality, always prefer building them as plugins (e.g., under `internal/plugins/bundled/` for built-in features) instead of adding feature logic directly to the core app or engine.

- Extending the core with general-purpose APIs, capabilities, events, slots, components, or hooks so that a plugin can implement the feature is expected and encouraged.
- Keep feature-specific UI, workflows, commands, tools, and background services in plugins rather than hardcoding them in core.
- See `docs/plugins.md` for the plugin contract, capabilities, and architecture.

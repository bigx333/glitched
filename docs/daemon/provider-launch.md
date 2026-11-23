# How an engine starts each provider's program

Each engine keeps, in `engine.json` under its state folder, how it starts each provider's CLI on its own machine. Settings › Providers edits these settings for this machine or for any paired server that is online. You can also edit the file by hand. The engine reads the file when it starts.

```json
"providers": {
  "claude": {"path": "~/bin/claude", "home": "~/.claude-work", "args": "--chrome"},
  "codex": {"home": "/srv/codex", "args": "-c features.image_generation=true"},
  "antigravity": {"path": "/opt/agy/bin/agy"}
}
```

- `path` is the program. It must be absolute or start with `~`. If it is empty, the engine looks for the program on PATH and then in `~/.local/bin`.
- `home` is the program's own folder. The engine passes it as `CLAUDE_CONFIG_DIR` for Claude Code or `CODEX_HOME` for Codex on every launch: turns, model probes, slash-command listings, one-shot asks and the version check. The folder must exist. Antigravity documents no such variable, so it takes no `home`. Threads whose sessions are in the old folder start a new conversation after a change.
- `args` are more arguments, written with shell quoting. Claude Code gets them before Glitched's own flags. Codex gets them after `app-server`. Antigravity gets them after `acp --disable-telemetry`. The engine refuses the flags that Glitched sets itself: the stream protocol, the permission mode or sandbox the access level picks, the MCP config, the session and, for Codex, `--listen`. It reads them as the CLIs do: `-cKEY=value`, `--flag=value`, and config keys with spaces or quotes around them all count. A bare `--` is refused because it would turn Glitched's own flags into plain arguments. `''` is an empty argument, as in a shell. Settings › Providers lists the reserved flags.

A setting saved from Settings › Providers is checked first. The program must exist, be executable and answer `--version`; the folder must exist; the arguments must parse and must not include a reserved flag. Saving takes effect for the next launch without a restart, and running turns keep their old launch. The keys `claude_path`, `codex_path` and `agy_path` from older files are still read as `path`. A relative value in them is resolved the way older engines ran it, from where the engine started or on PATH, and is read as that absolute path. A path set in `providers` must be absolute or start with `~`. Keys the engine does not know are kept when it rewrites the file, including unknown keys inside a provider's entry and entries for providers it doesn't run. `glitched serve` writes back only the settings its flags set, so it never overwrites a launch a client saved.

The settings travel over the engine's own API (`provider.launches` and `provider.launch`). The unix socket serves them to this machine's user, and the tailnet listeners serve them to a client holding a paired token. That is the same reach as starting a thread. Plugins have no route to them.

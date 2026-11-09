# Plugins

A Glitched plugin is a folder with a `plugin.json` and a JavaScript or TypeScript module, run with [mygo-plugin](https://github.com/yxxbc/mygo-plugin) (Glitched pins a fork of it: see [Upstream](#upstream-and-the-fork)). A plugin can:

- add pages to the sidebar, tabs to a thread's panel, and widgets to Glitched's own views (each thread row, a thread's header);
- hear what happens in the app (threads changing, new messages, settings) and wake on timers;
- act in the app through effects: start and message threads, draft in the composer, notify, copy;
- offer tools to Glitched's agents;
- do real work outside its sandbox, behind your grants: run the programs it names (`git`, `gh`), keep a SQLite database, reach the network, and run a headless service with no window open.

Everything a plugin draws is data. Glitched checks it and draws it with its own components. A plugin reaches the app only through what its manifest declares and you grant.

| What | Version |
| --- | --- |
| App contract (`contract`, `contractVersion`) | `glitched` 1 |
| Widget tree (`internal/plugins/widget.Version`) | 1 |
| Runtime | github.com/bigx333/mygo-plugin `v0.6.1-glitched.3`, a fork of mygo-plugin's `main` (see `go.mod`) |

A plugin that names another contract, or a newer `contractVersion`, does not load. One that names none loads.

## Install and develop

Glitched reads `~/.glitched/plugins` (`$GLITCHED_DIR/plugins`), one folder per plugin. Your choices go to `~/.glitched/plugins.json`: which plugins are off, your grants, settings and consents. A plugin's data is in `~/.glitched/plugin-data/<id>`. It runs from a read-only snapshot of its signed files in `~/.glitched/plugin-snapshots`.

```sh
mkdir -p ~/.glitched/plugins
cp -R examples/plugins/snippets ~/.glitched/plugins/
# Settings › Plugins: "Trust as mine" on Snippets, then grant what it asks for.
```

**Writing your own.**

1. Put the folder in the plugins folder, or link it there: `ln -s ~/src/my-plugin ~/.glitched/plugins/my-plugin`.
2. In **Settings › Plugins**, press **Trust as mine**. Glitched signs it with this device's key (`~/.glitched/plugin-key`).
3. Edit and save. Glitched signs your plugin again and reloads it within a second, and **keeps its pages' state**. A save that doesn't load keeps the last good code running, and Settings says why. A change to `plugin.json` reloads the plugin whole: its pages start over.
4. A plugin that fails to load says why under its name in Settings. A page whose `init`, `view` or `update` fails shows the error, with **Start over**.

`examples/plugins/glitched.d.ts` types the whole contract. In TypeScript, reference it with `/// <reference path="./glitched.d.ts" />` (copy it next to your module). A `main` ending in `.ts` compiles as TypeScript. A module is one file: bundle first if you need several.

**Samples** in `examples/plugins`:

| Sample | Shows |
| --- | --- |
| `hello` | a stock mygo-plugin page |
| `counter` | a native view in TypeScript: buttons, a controlled input, a toggle, a section on a condition |
| `snippets` | a form, storage, effects, the denial path, grants, commands |
| `row-status` | a badge on each thread row, from permission-filtered records, counting the changes it hears |
| `focus-timer` | a widget in a thread's header, a panel tab, settings with a status, a timer, a command with a shortcut |
| `word-tools` | tools for agents, with a database |
| `repo-watch` | a headless service on a timer that runs `git`, keeps its checks in SQLite, and answers its page |

## plugin.json

```json
{
  "id": "focus-timer",
  "name": "Focus timer",
  "version": "0.1.0",
  "main": "index.js",
  "contract": "glitched",
  "contractVersion": 1,
  "capabilities": ["ui.place", "notify"],
  "permissions": { "network": ["api.example.com"] },
  "contributes": {
    "pages": [{ "id": "home", "title": "Focus", "icon": "clock" }],
    "commands": [{ "id": "start", "title": "Start a focus block" }]
  },
  "glitched": {
    "places": [{ "id": "timer", "at": "thread.header" }],
    "tabs": [{ "id": "log", "title": "Focus", "icon": "clock-circle" }],
    "timers": [{ "id": "tick", "every": 30 }],
    "events": ["setting.changed"],
    "settings": [{ "id": "length", "kind": "number", "label": "Length", "min": 5, "max": 90, "default": 25 }],
    "tools": [{ "name": "count_words", "description": "…", "input": { "type": "object" } }],
    "programs": ["git"],
    "service": "service.js",
    "shortcuts": [{ "command": "start", "key": "cmd+shift+f" }]
  }
}
```

mygo-plugin reads the top level: `id`, `name`, `version`, `main`, `contract`, `contractVersion`, `capabilities`, `permissions.network`, `contributes.pages` and `contributes.commands`. Glitched's own additions live in the `glitched` block, so mygo-plugin's fields keep their meaning:

| Field | What | Needs |
| --- | --- | --- |
| `places` | widgets at Glitched's [slots](#places-and-slots) | `ui.place` |
| `tabs` | tabs in a thread's panel | `ui.place` |
| `timers` | `{id, every}` (seconds, at least 30): the service's `timer` export if it has a service, else a `timer` event to its app session | |
| `events` | the [app events](#app-events) it hears | the event's capability |
| `settings` | [settings](#settings) drawn in Settings › Plugins | |
| `tools` | tools for agents: `name` (`^[a-z][a-z0-9_]{0,40}$`, no `__`), `description`, `input` (a JSON Schema of type object) | `tools` |
| `programs` | the programs it runs, each a name, never a path | `programs` |
| `service` | its headless module, a file in its folder | `service` |
| `shortcuts` | `{command, key}`: `key` is modifiers and a key, as `cmd+shift+f`; it needs cmd, ctrl or alt | |

A part whose capability isn't declared, an unknown capability, slot or event, or a bad field stops the plugin from loading, and Settings says which.

Icons (pages, tabs, the `icon` widget) are names of Glitched's icons (`check`, `clock-circle`, `git-branch`, `terminal`, `folder`, `magnifer`, …: the files of `icons/`, and `sparkles`, `code`, `globe`, `file`, `note`, `inbox`, `clock`, `list`, `settings`, `zap`, `folder`, `message`), or an SVG of the plugin's (`"plugin:icon.svg"`, 32 KB at most), drawn in the text's color.

## How it runs

```mermaid
flowchart LR
  subgraph Window["Glitched's window (GUI process)"]
    P[Pages and tabs<br/>one session each] --> R[Renderer]
    A[App session<br/>places, app events, timers] --> R
  end
  subgraph Engine["Engine (embedded, or glitched serve)"]
    S[Services<br/>serve, tools, timers]
  end
  M[Agent's glitched mcp] -- "plugins.tool" --> S
  P -- "glitched.service.call" --> S
  A -- effects --> X{declared?<br/>granted?}
  P -- effects --> X
  X -- yes --> App[App: threads, composer,<br/>clipboard, toasts]
```

Each plugin runs as up to three kinds of session in the window, each keeping its state in Glitched:

- **A page** (`contributes.pages`), and **a tab** (`glitched.tabs`) per thread, are views of their own: `init`, `view`, `update`.
- **The app session**, one per plugin, draws its places with `place`, and hears app events, timers, commands and what the user does in its places, in `update` with `page: ""`.

The engine runs the plugin's **headless half** with no window: its `service` module, or for a plugin with tools and no service, its main module.

The window never waits for a plugin. One worker per session calls the plugin, one call at a time and in order. The window keeps drawing the last view meanwhile.

### Exports

| Export | Called with | Answers | When |
| --- | --- | --- | --- |
| `init` | `{page, tab?, context?}` | `{state?, effects?}` | a page, tab or the app session opens, or starts over |
| `view` | `{page, tab?, context?, state, values}` | `{title?, subtitle?, body}` | after `init`, and once after each batch of events |
| `update` | `{page, tab?, context?, state, event, values}` | `{state?, effects?}` | each event; leave `state` out to keep it |
| `place` | `{id, at, state, items: [{key, context}]}` | a widget or `null` per item, in order | as the copies shown, their contexts or the app state change |
| `command` | `{id, args}` | `{effects?}` | a palette command or shortcut |
| `page` | `{id}` | mygo-plugin's `Page` | stock pages only |
| `tool` | `{name, args, call: {thread}}` | the tool's answer | an agent calls a tool (in the engine) |
| `serve` | `{method, args}` | the method's answer | `glitched.service.call` (in the service) |
| `timer` | `{id}` | `{effects?}` | a timer (in the service) |

Every export may be `async`. `context` is the thread record of a tab's thread. State is any JSON value, at most 1 MiB, kept by Glitched, so a runtime restarted after a timeout keeps it. Data that outlasts the app goes in `host.storage` or `glitched.db`.

### Events

```js
{ kind: "ui", type: "click" | "change" | "submit" | "close", handler, node, value?, payload?, place? }
{ kind: "effect", id?, effect, ok, result?, error?, denied? }
{ kind: "app", type: "thread.changed" | "thread.message" | "setting.changed" | "theme.changed" | "command", data }
{ kind: "timer", id }
```

- `handler` and `payload` are what the widget named (a name, or `{handler, payload}` with up to 4 KiB of JSON). No function crosses into the app.
- `values` maps each input, toggle, select and tabs of the view, by `id`, to what it held when the event happened.
- `place` is set for a widget in a place: `{id, at, key, context}` of the copy it was in.
- An effect with an `id` always answers. One without answers only when it fails.
- While a `change` waits last in line, a later `change` of the same widget replaces it. A change never replaces one before a click or a submit. At most 64 events wait.

### Keyed widgets

Inputs, toggles, selects and tabs keep their value in the app, by `id`, across views:

- A new widget starts with the `value` its view gives.
- While you have changes the plugin hasn't taken, no view changes the widget: typing is never lost to a plugin that is behind.
- Once the plugin took them, a widget with `controlled: true` shows the view's `value` whenever it differs. The plugin owns it: clearing it in the state clears it.
- A widget without `controlled` takes a `value` only when it differs from the last view's. An `onChange` that only validates never clears the field.
- To reset an uncontrolled widget, change its `value`, give it a new `id`, or leave it out of one view.

## Widgets

Every widget is `{type, id?, ...}`. Interactive widgets and dialogs need an `id` (1 to 128 of letters, digits, `_.:/-`), unique in the view. An unknown field, or a field a type doesn't take, is an error.

| Type | Fields | Fires |
| --- | --- | --- |
| `column`, `row` | `children`, `gap`, `padding` (0 to 64), `align`, `justify`, `grow`, `background` | |
| `card` | `title`, `children`, `gap`, `padding` | |
| `text` | `text`, `style` (`body` `muted` `heading` `title` `code` `error` `success`), `color`, `selectable` | |
| `markdown` | `text` (http and https links open in the browser) | |
| `badge` | `text`, `style` (`default` `accent` `success` `warning` `danger`), `color` | |
| `icon` | `name`, `color` | |
| `image` | `src` (`plugin:<file>`, PNG, JPEG or SVG up to 4 MiB; or `https://…` with the network grant), `width`, `height`, `radius` | |
| `table` | `columns` (`{id, title, width?}`, up to 20), `rows` (`{key, cells: {column: text}}`, up to 500) | |
| `divider`, `spacer`, `spinner` | | |
| `button` | `label`, `style` (`default` `primary` `danger`), `disabled` | `onClick` |
| `input` | `label`, `placeholder`, `value`, `controlled`, `multiline`, `disabled` | `onChange`, `onSubmit` |
| `toggle` | `label`, `value`, `controlled`, `disabled` | `onChange` |
| `select` | `label`, `options`, `value`, `controlled`, `disabled` | `onChange` |
| `tabs` | `options`, `value`, `controlled`, `disabled` | `onChange` |
| `menu` | `label`, `options`, `disabled` | `onChange` (the chosen option's value) |
| `dialog` | `title`, `open`, `children` | `onClose` (Escape, a click outside) |

`options` are strings, or `{value, label}`, 1 to 200. `color` and `background` are theme tokens, never raw values: `$text`, `$text.muted`, `$text.faint`, `$accent`, `$success`, `$warning`, `$danger`, `$border`, `$surface`.

A view is at most 1 MiB, 2000 widgets, 24 deep, and 32 KiB per string. A place's copy is at most 200 widgets. A view past a limit is refused whole: the last good one stays, with the error.

## Places and slots

A place puts widgets in Glitched's own views. Declare it (`glitched.places`, with `ui.place`), and export `place`, which draws every copy of it in one call:

```js
export function place({ id, at, state, items }) {
  return items.map(({ key, context }) => context.status ? { type: "badge", text: context.status } : null);
}
```

| Slot | Stable | Copies | Context |
| --- | --- | --- | --- |
| `thread.row` | yes | one per sidebar row, keyed by the thread's id | the thread |
| `thread.header` | yes | the thread shown | the thread |
| `sidebar.footer` | no | one | none |

Stable slots keep their name and context across Glitched's updates; others may move. A copy shows its last drawing while the next is drawn, so rows never wait or flicker. Glitched draws a place again only when a copy comes, goes or its record changes, or the app session's state does, in batches of up to 200 copies. What the user does in a place reaches `update` with `event.place`.

**Panel tabs** (`glitched.tabs`) show as buttons in a thread's header and open in the panel beside the chat, one session per thread, with the thread as `context`. The `tab.open` effect opens one.

## App events

A plugin hears the events its manifest lists, if it may:

| Event | Data | Needs |
| --- | --- | --- |
| `thread.changed` | the thread's record (its title, status, workspace, machine or time changed), or `{id, deleted: true}` | `threads:read` |
| `thread.message` | `{thread, role, text}`, each new message of a thread you have open: from when it's first shown, and while you're away from it once it was | `messages:read` |
| `setting.changed` | `{id, value, was}` | |
| `theme.changed` | `{appearance}` | |

`command` (a command of its own ran) and `timer` reach every app session with `update`. A thread record is `{id, title, workspace, status, machine, updatedAt}`. Without `threads:read` it is `{id}` alone, in places and tabs too.

## Effects

`init`, `update`, `command` and a service's `timer` may answer up to 16 effects. They run on the UI thread, checked as they run: an effect that waits while its grant is taken back, or its plugin stops, doesn't run.

| Effect | Fields | Answers | Needs |
| --- | --- | --- | --- |
| `toast` | `title`, `description?` | | |
| `url.open` | `url` (http, https) | | |
| `page.open` | `page` (its own) | | |
| `tab.open` | `tab` (its own) | | `ui.place` |
| `setting.status` | `setting`, `status: {badge?, tone?, text?}` or `null` | | |
| `clipboard.write` | `text` | | `clipboard` |
| `notify` | `title`, `body?` | | `notify` |
| `composer.draft` | `text`, `workspace?` | | `composer` |
| `threads.list` | | thread records, newest first | `threads:read` |
| `threads.open` | `thread` | | `threads:read` |
| `threads.create` | `prompt`, `title?`, `workspace?` | `{thread}` | `threads:write` |
| `threads.send` | `thread`, `text` | `{message}` | `threads:write` |

A toast carries the plugin's name. `threads.create` starts a thread with the composer's model and access (a service's: the last thread's), and sends the prompt. A service can't toast, copy, notify, draft or open: it has no window.

## The glitched global

Every runtime has `glitched`, awaitable calls into the host, checked against the plugin's grants on each call. A refused call rejects with `err.code` `"denied"` (or `"refused"` for a program the guard refuses).

```js
const r = await glitched.run("git", ["status", "--porcelain"], { cwd: folder, timeout: 30 });
await glitched.db.exec("CREATE TABLE IF NOT EXISTS seen (url TEXT PRIMARY KEY, at INTEGER)");
const rows = await glitched.db.query("SELECT at FROM seen WHERE url = ?", [url]);
const status = await glitched.service.call("status", { cwd: folder });
```

**`glitched.run(program, args, {cwd, stdin, timeout, env})`**, with `programs`, runs a program its manifest names, as you, under Zeron's plugin-guard rules (`internal/plugins/guard`):

- No shell: args reach the program as given. `cwd` is an absolute folder (your home without one).
- `env` sets only harmless variables (`LANG`, `TZ`, `NO_COLOR`, git's identity, gh's quiet switches, `GLITCHED_PLUGIN_…`), never what changes what runs (`PATH`, `GIT_SSH_COMMAND`, `NODE_OPTIONS`, …).
- git runs its own commands only, with `-c` only for how it prints, `-C` inside `cwd`, `git config` read only, and never its hooks, fsmonitor, `ext::` transport, pager, editor or prompt. gh never runs `alias`, `extension`, `codespace`, `auth` (but `auth status`) or `config` (but reading).
- A run waits `timeout` seconds (60, at most 300), then its process group is killed (`status` -1). 16 MiB of output is kept per stream.
- The consent sheet names each program, and says that git, gh, a shell or an interpreter can do anything you can.

**`glitched.db`** is the plugin's own SQLite file (pure Go): 100 MB, up to 10,000 rows a query, parameters always bound, and no `ATTACH`, `DETACH`, `PRAGMA`, `VACUUM INTO` or extensions. It goes with the plugin.

**`glitched.service.call(method, args, {timeout})`**, with `service`, calls the plugin's service in this machine's engine (30 s, at most 300).

**fetch** (mygo-plugin's) reaches the hosts `permissions.network` names, with the `network` grant, through the guard: a name that resolves to a private address is reached only when named exactly, and metadata and link-local addresses never. `"*"` is any public address.

## Services and tools

The engine (the one the window embeds, or `glitched serve`) runs each plugin's headless half: its `service` module, if it declares one and you granted `service`, else its main module if it has tools.

- It exports `serve` (for `glitched.service.call`), `tool` (for agents) and `timer`.
- It runs with no window, in a runtime of 64 MiB. A call has 30 s; a tool, 60 s.
- It stops after 5 minutes with nothing to do, and starts again on the next call, tool or timer. Its state goes then; keep what matters in `glitched.db`.
- It follows `plugins.json` and the plugins folder as they change.
- It shares nothing with your windows but what it answers.

**Tools.** Each tool in `glitched.tools` shows to Glitched's agents as `<plugin>__<tool>` (for example `word-tools__count_words`), beside `delegate_task`, with its description and input schema. `glitched mcp` lists them from the engine and calls them for the agent's thread (`call.thread`). The engine serves the plugin methods on its unix socket only, never over the tailnet. A tool runs the plugin's code with the plugin's grants.

## Settings

Declare settings in `glitched.settings`. Settings › Plugins draws them natively, and only you set them.

| kind | value | extra fields |
| --- | --- | --- |
| `switch` | `true` / `false` | |
| `choice` | an option's `value` | `options` `[{value, label, description?}]`, `style` `select` or `segmented` |
| `text` | up to 2000 characters | `placeholder` |
| `number` | a number within `min` and `max` | `min`, `max`, `step`, `unit` |
| `shortcut` | `"ctrl+alt+k"` or `""` | |
| `folder` | an absolute folder you chose, or `""` | |

Each has an `id`, a `kind`, a `label`, and optionally a `description`, `default` and `section`. A setting never set reads its `default`; a value that no longer fits reads the default again.

- **Read it:** `host.setting(id)` gives the value as text (`"true"`, `"25"`, the option's value), in the window and the service.
- **Follow it:** list `setting.changed` in `events`.
- **Say how it stands:** the `setting.status` effect sets a badge in a fixed tone (`neutral`, `success`, `warning`, `danger`) and words in the description's place; `null` clears it.

## Commands and shortcuts

`contributes.commands` show in the palette (⌘K) as `<plugin>: <title>`, and run the `command` export, whose effects run. `glitched.shortcuts` bind them to keys. A key Glitched or an earlier plugin already has stays theirs.

## Permissions

Your plugins and others' plugins share one rule: the manifest says what a plugin asks for, and Settings is what you give. Undeclared means denied, always.

| Capability | Lets the plugin | Grant |
| --- | --- | --- |
| `clipboard` | copy text | |
| `notify` | show notifications | |
| `composer` | draft in a new thread's composer, for you to send | |
| `ui.place` | put widgets in Glitched's views, open panel tabs | |
| `tools` | offer tools to your agents | |
| `threads:read` | read threads' titles, workspaces and status; see full records in places, tabs and events | yes |
| `messages:read` | read the messages of the thread you have open | yes |
| `threads:write` | start threads and send messages, which runs agents | yes |
| `programs` | run the programs it names, as you | yes |
| `service` | run in the background, with no window | yes |
| `network` | reach the hosts `permissions.network` names | yes |

Grants start off. **Settings › Plugins** has a switch for each one a plugin declares, and a grant reloads the plugin. `host.capability(name)` answers whether it has one now.

A plugin can never reach the UI tree, call Go beyond `glitched`, read files outside its data folder, share another plugin's runtime or storage, or hold the window.

## Signing, installing and updates

Every plugin is signed: a `signature.json` beside `plugin.json` holds an ed25519 signature, by a device's key, over the sha256 of every file. Glitched loads a plugin only from a read-only snapshot of its verified files. Editing the folder never changes what runs until the plugin is verified again.

| The plugin is | Glitched |
| --- | --- |
| signed by this device's key (or one you trust) | runs it; signs it again as you change it |
| unsigned | waits: **Trust as mine** signs it with your key |
| signed by someone else, not yet allowed | waits, off, on a consent sheet that lists what it asks for: **Allow** or **Remove** |
| an update by the same author asking for more | waits on the consent sheet again |
| changed since someone else signed it | refuses it: reinstall it |

**Installing.** In Settings › Plugins, **Install a plugin** takes:

- a `.glitched-plugin` file, or an https link to one;
- a GitHub release (`…/releases/latest`, `…/releases/tag/<tag>`): its `.glitched-plugin` asset;
- a GitHub repository (`owner/repo`, `…/tree/<branch>/<folder>`): its latest release, else its default branch;
- any https git repository.

Glitched checks the signature over every file before it writes anything, then installs atomically. **Check for updates** fetches each installed plugin from where it came. An update must be signed by the same key and be a later version, and Glitched lists what it newly asks for. **Remove** takes away the plugin's folder, data, grants, settings and consent.

**Publishing.** Commit the signed folder to a repository, or attach its package to a release. A `.glitched-plugin` file is JSON: `{format: "glitched-plugin:v1", files: {path: base64}, signature}`. `internal/plugins/pack` reads and writes both.

## Limits

| What | Limit |
| --- | --- |
| A call (`init`, `update`, `serve`, a timer) | 30 s; `view` and `place` 5 s; a tool 60 s |
| A module's top level as it starts | 5 s |
| A window runtime / a service runtime | 256 MiB / 64 MiB |
| A service with nothing to do | stopped after 5 minutes |
| State | 1 MiB |
| A view / a place's copy | 1 MiB, 2000 widgets, 24 deep / 200 widgets |
| A place call | 200 copies |
| Events waiting / effects per answer | 64 / 16 |
| A timer | every 30 s at least |
| A program run | 60 s (at most 300), 16 MiB per stream |
| The database / a query | 100 MB / 10,000 rows |
| A signed plugin | 8 MiB a file, 32 MiB, 2000 files |

## Failures and recovery

| What | What you see | Recovery |
| --- | --- | --- |
| `plugin.json` unreadable, an unknown capability, slot or event, a syntax error | its error in Settings | fix and save, or Reload |
| Unsigned, waiting for consent, or tampered | why, with **Trust as mine**, **Allow** or **Remove** | |
| A save that doesn't load | the error in Settings; the last good code runs on | fix and save |
| `init` fails | the error, and **Start over** | Start over |
| `view` or `place` fails or is refused | the error; the last good drawing stays | the next event or change |
| A call times out | a stuck runtime is restarted | state survives |
| A service call fails | `err.code`: `denied`, `no_service`, `offline`, or the service's own error | the page decides |
| A plugin misbehaves | | turn it off in Settings: its runtimes stop and its pages, places, tabs, commands and tools go |

## Not supported yet

- **Slots:** inside messages, the composer, files, or another plugin's slots and menus (Zeron's `slot:` and `menu_entries`). Replacing Glitched's elements or screens.
- **Your other devices:** reaching your other devices (Zeron's `devices`, a service called on another machine); a thread on another machine from `threads.create`.
- **Services:** a service's `emit` to windows, and folder watching.
- **Settings:** wallpaper and permission settings, a chosen thread's model or access level.
- **Translations:** `locales/` (mygo-plugin's `host.t` reads them, but Glitched shows English).
- **Installing:** a plugin registry or store, a key directory beyond `plugin-trusted-keys.json`, and the MCP tools to create, install or inspect plugins (Zeron's `create_plugin`, `plugin_status`, `describe_elements`).

## Upstream and the fork

Glitched pins `github.com/bigx333/mygo-plugin` by a `replace` of `github.com/yxxbc/mygo-plugin`. It is upstream's `main` at `4cef697`, plus:

| Change | Why |
| --- | --- |
| `Options.Functions`, `host.call`, `host.functions` | upstream lets a host add no functions; `glitched.run`, `glitched.db` and `glitched.service` are host functions |
| `Options.Prelude` | defines the `glitched` global in every runtime |
| `Options.LoadTimeout`, `Close` ending a start | upstream runs a module's top level with no limit, holding the plugin's lock |
| `Options.Main` | the engine loads a plugin's service module as a runtime of its own |
| A call fails as its runtime closes | upstream left it to its timeout, so a reload stalled callers |

The fork's README lists each change, and they are small enough to propose upstream. Glitched's own layers sit above it: the widget contract (`internal/plugins/widget`), the renderer (`render`), sessions, places, events, effects, grants, settings and services (`plugins`), the program and network guard (`guard`), and signing and installs (`pack`).

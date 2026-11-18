# Example plugins

[docs/plugins.md](../../docs/plugins.md) is the contract and the guide.

| Sample | Shows |
| --- | --- |
| `hello` | a stock mygo-plugin page |
| `counter` | a native view in TypeScript |
| `snippets` | a form, storage, effects, grants and commands |
| `row-status` | a badge on each thread row |
| `focus-timer` | a header widget, a panel tab, settings, a timer, a shortcut |
| `word-tools` | tools for agents, with a database |
| `repo-watch` | a headless service on a timer that runs git |
| `quiet-header` | a component replaced: the thread header's buttons, icons only |

`glitched.d.ts` types the contract for a TypeScript plugin's editor.

```sh
mkdir -p ~/.glitched/plugins && cp -R examples/plugins/snippets ~/.glitched/plugins/
# Then Settings › Plugins › Trust as mine.
```

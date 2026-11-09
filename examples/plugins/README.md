# Plugins

Glitched loads the plugins in `~/.glitched/plugins` (`$GLITCHED_DIR/plugins`), one folder each, when it starts. Each one keeps its storage in `~/.glitched/plugin-data/<id>`. The host is `internal/plugins`, on [mygo-plugin](https://github.com/yxxbc/mygo-plugin).

A plugin is a `plugin.json` and a JavaScript or TypeScript module. Glitched shows each page in `contributes.pages` in its sidebar, and draws what the module's `page({ id })` export returns: the title, the subtitle, the Markdown body, and the sections and their items. An item's actions run the module's `command({ id, args })` export (`kind: "command"`, the command named in `meta.command`) or open a web page (`kind: "url"`). A page is read again after one of its commands runs.

Glitched provides the `glitched` contract, version 1. A plugin that names another contract, or a newer version, does not load.

`hello` is a sample. To try it:

```sh
cp -R examples/plugins/hello ~/.glitched/plugins/
```

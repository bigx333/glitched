// Package bundled holds the plugins built into Glitched, which draw parts
// of its UI by default. Each is a folder signed by Glitched's release key;
// run cmd/sign-bundled after changing one.
package bundled

import (
	"embed"
	"io/fs"
)

//go:embed all:header-actions all:diff-panel all:settings-pages all:greetings all:row-actions all:project-strip all:composer all:timeline all:palette all:sidebar all:agents all:message-toolbar all:workspace all:model-picker all:child-threads all:plan all:queue all:pull-requests all:skills all:schedules
var files embed.FS

// FS is the built-in plugins, a folder each at its root.
var FS fs.FS = files

// ReleaseKey is the public half of the key that signs the built-in
// plugins, base64. cmd/sign-bundled -new replaces it.
const ReleaseKey = "dj7v6ruVjh7tTklNCssj6SWwWpzkD0Xc9J6Jy0sFzgw="

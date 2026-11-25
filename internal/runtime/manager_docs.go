package runtime

import (
	"cmp"
	"io"
	"os"
	"path/filepath"
	"strings"

	"glitched/internal/api"
)

const (
	managerDocFileLimit  = 16 << 10
	managerDocTotalLimit = 32 << 10
)

var managerDocFiles = []string{"AGENTS.md", "WORKERS.md", "NOTES.md"}

// loadManagerDocs reads AGENTS.md, WORKERS.md, and NOTES.md from the thread's
// root workspace and formats non-empty files as a <workspace_context> preamble
// for a session's initial prompt. Missing or unreadable files are skipped.
func loadManagerDocs(workspace, project string, _ api.ThreadRole, _ bool) string {
	root := cmp.Or(project, workspace)
	if root == "" {
		return ""
	}
	var b strings.Builder
	remaining := managerDocTotalLimit
	included := 0
	for _, name := range managerDocFiles {
		if remaining <= 0 {
			break
		}
		path := filepath.Join(root, name)
		fi, err := os.Stat(path)
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		limit := min(managerDocFileLimit, remaining)
		data, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
		_ = f.Close()
		if err != nil {
			continue
		}
		truncated := false
		if len(data) > limit {
			data = data[:limit]
			truncated = true
		}
		text := strings.TrimSpace(string(data))
		if text == "" {
			continue
		}
		if included == 0 {
			b.WriteString("<workspace_context>\n")
		} else {
			b.WriteByte('\n')
		}
		b.WriteString(`<file name="`)
		b.WriteString(name)
		b.WriteString("\">\n")
		b.WriteString(text)
		if truncated {
			b.WriteString("\n...[truncated]")
		}
		b.WriteString("\n</file>\n")
		remaining -= len(data)
		included++
	}
	if included == 0 {
		return ""
	}
	b.WriteString("</workspace_context>\n\n")
	return b.String()
}

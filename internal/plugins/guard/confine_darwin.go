//go:build darwin

package guard

import (
	"path/filepath"
	"strings"
)

// confine runs program under sandbox-exec with a profile that denies it,
// and all it starts, reading or writing inside denied: the kernel's rule,
// which no argument, shell or tree walk gets past.
func confine(program string, args, denied []string) (string, []string) {
	if len(denied) == 0 {
		return program, args
	}
	var b strings.Builder
	b.WriteString("(version 1)(allow default)")
	for _, d := range denied {
		d = filepath.Clean(d)
		for _, p := range []string{d, resolved(d)} {
			b.WriteString("(deny file-read* file-write* (subpath " + sbString(p) + "))")
		}
	}
	return "/usr/bin/sandbox-exec", append([]string{"-p", b.String(), program}, args...)
}

func sbString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

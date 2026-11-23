// Package skillstest helps tests of the skills Glitched gives agents: it
// writes skills, lists what an agent's skill folders hold, as the fake
// CLIs report it, and fingerprints folders that must stay untouched.
package skillstest

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Write writes the skill name in root, with description.
func Write(t testing.TB, root, name, description string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	md := "---\nname: " + name + "\ndescription: " + description + "\n---\n\nDo the " + name + " thing.\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(md), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// Seen is the skills in each of roots, prefix+name for each folder of one
// that holds a SKILL.md, and that file's description line, sorted: what a
// CLI given those roots would list.
func Seen(prefix string, roots ...string) []string {
	var out []string
	for _, root := range roots {
		entries, _ := os.ReadDir(root)
		for _, e := range entries {
			md, err := os.ReadFile(filepath.Join(root, e.Name(), "SKILL.md"))
			if err != nil {
				continue
			}
			desc := ""
			for line := range strings.Lines(string(md)) {
				if d, ok := strings.CutPrefix(line, "description: "); ok {
					desc = strings.TrimSpace(d)
				}
			}
			out = append(out, prefix+e.Name()+": "+desc)
		}
	}
	slices.Sort(out)
	return out
}

// Fingerprint is every path in dirs with its mode and content's hash, so
// that two fingerprints differ when anything in them changed.
func Fingerprint(t testing.TB, dirs ...string) string {
	t.Helper()
	var b strings.Builder
	for _, dir := range dirs {
		err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			fi, err := os.Lstat(p)
			if err != nil {
				return err
			}
			sum := ""
			switch {
			case fi.Mode().IsRegular():
				data, err := os.ReadFile(p)
				if err != nil {
					return err
				}
				sum = fmt.Sprintf("%x", sha256.Sum256(data))
			case fi.Mode()&fs.ModeSymlink != 0:
				sum, _ = os.Readlink(p)
			}
			fmt.Fprintf(&b, "%s %v %s\n", p, fi.Mode(), sum)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return b.String()
}

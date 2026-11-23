package secrets

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// EnvFile is where in workspace the dotenv file rel is: a file inside it
// that git ignores, so the values in it are never committed.
func EnvFile(workspace, rel string) (string, error) {
	if rel == "" || filepath.IsAbs(rel) || !filepath.IsLocal(rel) {
		return "", fmt.Errorf("env_file %q must be a path inside the thread's folder", rel)
	}
	path := filepath.Join(workspace, rel)
	if fi, err := os.Lstat(path); err == nil && !fi.Mode().IsRegular() {
		return "", fmt.Errorf("env_file %q is not a plain file", rel)
	}
	cmd := exec.Command("git", "-C", workspace, "check-ignore", "-q", "--", rel)
	switch err := cmd.Run(); {
	case err == nil:
		return path, nil
	case cmd.ProcessState != nil && cmd.ProcessState.ExitCode() == 1:
		return "", fmt.Errorf("env_file %q is not ignored by git, so its values could be committed; add it to .gitignore first", rel)
	default:
		return "", fmt.Errorf("env_file %q: git can't say whether it is ignored: %v", rel, err)
	}
}

var assignment = regexp.MustCompile(`^\s*(?:export\s+)?([A-Za-z_][A-Za-z0-9_]*)\s*=`)

// WriteDotenv sets values in the dotenv file at path: an assignment of
// each name already there is replaced where it stands, and the others
// are added at the end. Every other line stays as it was.
func WriteDotenv(path string, values map[string]string) error {
	b, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	left := maps.Clone(values)
	var out strings.Builder
	sc := bufio.NewScanner(strings.NewReader(string(b)))
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if m := assignment.FindStringSubmatch(line); m != nil {
			if v, ok := values[m[1]]; ok {
				if _, first := left[m[1]]; !first {
					continue
				}
				line = m[1] + "=" + dotenvQuote(v)
				delete(left, m[1])
			}
		}
		out.WriteString(line + "\n")
	}
	if err := sc.Err(); err != nil {
		return err
	}
	for _, name := range slices.Sorted(maps.Keys(left)) {
		out.WriteString(name + "=" + dotenvQuote(left[name]) + "\n")
	}
	return replace(path, out.String())
}

// dotenvQuote is v as dotenv parsers read a value back: single quotes
// keep it as it is, and one with a single quote in it goes in double
// quotes with its backslashes, double quotes and dollars escaped.
func dotenvQuote(v string) string {
	if !strings.Contains(v, "'") {
		return "'" + v + "'"
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "$", `\$`).Replace(v) + `"`
}

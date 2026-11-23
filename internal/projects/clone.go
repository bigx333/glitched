// Package projects lists the folders on an engine's machine and clones
// repositories onto it, for a client adding a project there.
package projects

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"glitched/internal/api"
	"glitched/internal/plugins/guard"
)

// Source is what a clone's field names: the URL git clones, and typed as
// owner/name, the repository gh clones.
type Source struct {
	URL string
	// Repo is owner/name as typed, "" for a URL.
	Repo string
	// Name is the folder the clone makes.
	Name string
}

var (
	// ownerName is owner/name, neither starting with a dash, which gh
	// would take as an option.
	ownerName = regexp.MustCompile(`^([A-Za-z0-9_.][A-Za-z0-9_.-]*)/([A-Za-z0-9_.][A-Za-z0-9_.-]*)$`)
	scpLike   = regexp.MustCompile(`^[A-Za-z0-9_.-]+@[A-Za-z0-9_.-]+:(.+)$`)
	// gitProgress is one of git's progress lines, which it redraws in
	// place, and which say nothing of why a clone failed.
	gitProgress = regexp.MustCompile(`^(remote: )?[A-Za-z][A-Za-z ]*:\s+\d+%`)
	authFailure = regexp.MustCompile(`Authentication failed|could not read Username|Permission denied \(publickey\)|Repository not found`)
	credentials = regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.-]*://)[^/@\s]+@`)
)

const (
	// CloneTimeout is as long as a clone may take.
	CloneTimeout = 15 * time.Minute
	// CloneHint ends the error of a clone that was refused access.
	CloneHint = "Sign in with gh auth login, or paste a clone URL your git can reach."
	noGit     = "git isn't installed on this machine (or isn't on your PATH)"
)

// ParseSource reads s as owner/name, a clone URL (https, http, ssh, git
// or file, on any host, or user@host:path), or a GitHub page, which clones
// its repository.
func ParseSource(s string) (Source, bool) {
	s = strings.TrimSpace(s)
	if m := ownerName.FindStringSubmatch(s); m != nil {
		if dots(m[1]) || dots(m[2]) {
			return Source{}, false
		}
		repo := m[1] + "/" + strings.TrimSuffix(m[2], ".git")
		return named(Source{URL: "https://github.com/" + repo + ".git", Repo: repo}, repo)
	}
	if m := scpLike.FindStringSubmatch(s); m != nil && !strings.Contains(s, "://") {
		return named(Source{URL: s}, m[1])
	}
	u, err := url.Parse(s)
	if err != nil {
		return Source{}, false
	}
	switch u.Scheme {
	case "https", "http":
		if strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.") == "github.com" {
			parts := strings.Split(strings.Trim(u.Path, "/"), "/")
			if len(parts) < 2 {
				return Source{}, false
			}
			owner, name := parts[0], strings.TrimSuffix(parts[1], ".git")
			if !ownerName.MatchString(owner+"/"+name) || dots(owner) || dots(name) {
				return Source{}, false
			}
			u = &url.URL{Scheme: "https", User: u.User, Host: "github.com", Path: "/" + owner + "/" + name + ".git"}
			return named(Source{URL: u.String()}, name)
		}
		fallthrough
	case "ssh", "git":
		if u.Host == "" {
			return Source{}, false
		}
	case "file":
	default:
		return Source{}, false
	}
	return named(Source{URL: u.Scheme + s[len(u.Scheme):]}, u.Path)
}

// named is src with the folder name p's last part gives it.
func named(src Source, p string) (Source, bool) {
	src.Name = strings.TrimSuffix(path.Base(strings.TrimRight(p, "/")), ".git")
	return src, src.Name != "" && src.Name != "/" && !dots(src.Name)
}

func dots(s string) bool { return s == "." || s == ".." }

// CloneInto clones what req names into its Dest on this machine, with the
// programs path finds, within CloneTimeout, and says each line they print.
// Dest is refused when it is in a folder hide names.
func CloneInto(ctx context.Context, req api.CloneRequest, hide []string, path string, progress func(string)) (api.Cloned, error) {
	src, ok := ParseSource(req.Source)
	if !ok {
		return api.Cloned{}, errors.New("projects: that isn't a clone URL or owner/name")
	}
	dest, err := Expand(req.Dest)
	if err != nil {
		return api.Cloned{}, err
	}
	if err := refuseHidden(dest, hide); err != nil {
		return api.Cloned{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, CloneTimeout)
	defer cancel()
	if err := Clone(ctx, src, dest, path, progress); err != nil {
		return api.Cloned{}, err
	}
	return api.Cloned{Path: dest}, nil
}

// Clone clones src into dest, a folder that isn't there yet or is empty,
// with the programs path finds, and says each line they print, with no
// credentials in them. It clones into a folder of its own beside dest,
// which goes to dest only once the clone is whole, and is all a clone
// that fails or is stopped takes away. Its error is the last lines git
// said, with what to do if it was refused access.
func Clone(ctx context.Context, src Source, dest, path string, progress func(string)) error {
	gh, hasGH := guard.Find("gh", path)
	git, hasGit := guard.Find("git", path)
	if !hasGit && (src.Repo == "" || !hasGH) {
		return errors.New(noGit)
	}
	parent := filepath.Dir(dest)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	if entries, err := os.ReadDir(dest); len(entries) > 0 || err != nil && !errors.Is(err, fs.ErrNotExist) {
		return errors.New(dest + " already exists and isn't empty")
	}
	env := append(os.Environ(), "PATH="+path, "GIT_TERMINAL_PROMPT=0", "GH_PROMPT_DISABLED=1", "GCM_INTERACTIVE=never")
	command := func(program string, args ...string) *exec.Cmd {
		cmd := exec.CommandContext(ctx, program, args...)
		cmd.Env = env
		guard.OwnGroup(cmd)
		cmd.WaitDelay = time.Second
		return cmd
	}
	useGH := src.Repo != "" && hasGH && command(gh, "auth", "status", "--hostname", "github.com").Run() == nil
	if !useGH && !hasGit {
		return errors.New(noGit)
	}
	stage, err := os.MkdirTemp(parent, "."+filepath.Base(dest)+".glitched-clone-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	cmd := command(git, "clone", "--progress", "--", src.URL, stage)
	if useGH {
		cmd = command(gh, "repo", "clone", src.Repo, stage, "--", "--progress")
	}
	stderr, err := cmd.StderrPipe()
	if err == nil {
		err = cmd.Start()
	}
	if err != nil {
		return err
	}
	var tail []string
	refused := false
	lines := bufio.NewScanner(stderr)
	lines.Split(splitLines)
	for lines.Scan() {
		line := credentials.ReplaceAllString(strings.ReplaceAll(strings.TrimSpace(lines.Text()), stage, dest), "$1")
		if line == "" {
			continue
		}
		progress(line)
		if !gitProgress.MatchString(line) {
			tail = append(tail[max(0, len(tail)-3):], line)
			refused = refused || authFailure.MatchString(line)
		}
	}
	err = cmd.Wait()
	if err == nil {
		// Renaming onto dest replaces it only while it's an empty folder:
		// one the user filled meanwhile, or another clone's, stays.
		if err := os.Rename(stage, dest); err != nil {
			if errors.Is(err, fs.ErrExist) {
				return errors.New(dest + " already exists and isn't empty")
			}
			return err
		}
		return nil
	}
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return errors.New("The clone took longer than 15 minutes, and was stopped.")
	case ctx.Err() != nil:
		return ctx.Err()
	case len(tail) == 0:
		tail = []string{"The repository couldn't be cloned."}
	}
	if refused {
		tail = append(tail, CloneHint)
	}
	return errors.New(strings.Join(tail, "\n"))
}

// splitLines splits git's output at each \r as well as each \n: a
// progress line redraws itself after a \r.
func splitLines(data []byte, atEOF bool) (int, []byte, error) {
	if i := bytes.IndexAny(data, "\r\n"); i >= 0 {
		return i + 1, data[:i], nil
	}
	if atEOF && len(data) > 0 || len(data) >= maxCloneLine {
		n := min(len(data), maxCloneLine)
		return n, data[:n], nil
	}
	return 0, nil, nil
}

// maxCloneLine is as much of one line as is kept: a longer one comes in
// pieces, so that no line stops the reading, and git with it.
const maxCloneLine = 4096

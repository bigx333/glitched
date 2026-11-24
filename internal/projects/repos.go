package projects

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"glitched/internal/api"
	"glitched/internal/plugins/guard"
)

const (
	// ReposTimeout is as long as a listing of repositories may take.
	ReposTimeout = 30 * time.Second
	// maxRepos is as many repositories as one listing holds.
	maxRepos = 1000
	// reposPage is as many as one page of the listing holds.
	reposPage = 100
	// reposQuery asks GitHub for the repositories the user gh is signed
	// in as reaches, most recently pushed first.
	reposQuery = "user/repos?per_page=100&sort=pushed&affiliation=owner,collaborator,organization_member"
	// reposShape keeps of each repository only what a Repo holds, one
	// object a line.
	reposShape = ".[] | {name: .full_name, description, private, fork}"
	noGH       = "gh isn't installed on this machine (or isn't on your PATH)"
	noAuth     = "gh isn't signed in to github.com on this machine: run gh auth login there"
)

// Repos gives page each reposPage of the GitHub repositories the gh path
// finds is signed in to reach, most recently pushed first, as they come.
// The last page, Last set, holds what's left, empty when a round number
// came; it's Truncated when gh may have had more than maxRepos. A listing
// that fails gives no Last page, and its error is the last lines gh
// said.
func Repos(ctx context.Context, path string, page func(api.RepoPage)) error {
	gh, ok := guard.Find("gh", path)
	if !ok {
		return errors.New(noGH)
	}
	ctx, cancel := context.WithTimeout(ctx, ReposTimeout)
	defer cancel()
	env := toolEnv(path)
	if err := tool(ctx, env, gh, "auth", "status", "--hostname", "github.com").Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New(noAuth)
	}
	cmd := tool(ctx, env, gh, "api", "--paginate", reposQuery, "--jq", reposShape)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	buf := []api.Repo{}
	dec := json.NewDecoder(stdout)
	var decErr error
	for n := 0; n < maxRepos; {
		var r api.Repo
		if err := dec.Decode(&r); err != nil {
			if !errors.Is(err, io.EOF) {
				decErr = err
			}
			break
		}
		buf = append(buf, r)
		if n++; len(buf) == reposPage {
			page(api.RepoPage{Repos: buf, Last: n == maxRepos, Truncated: n == maxRepos})
			buf = []api.Repo{}
			if n == maxRepos {
				// gh may have more: what it still has to say goes
				// nowhere.
				cancel()
				cmd.Wait()
				return nil
			}
		}
	}
	err = cmd.Wait()
	switch {
	case err == nil && decErr == nil:
		page(api.RepoPage{Repos: buf, Last: true})
		return nil
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return errors.New("listing your repositories took longer than 30 seconds, and was stopped")
	case ctx.Err() != nil:
		return ctx.Err()
	case err == nil:
		return decErr
	}
	if tail := lastLines(stderr.String(), 3); tail != "" {
		return errors.New(tail)
	}
	return errors.New("gh couldn't list your repositories")
}

// lastLines is s's last n non-empty lines, joined.
func lastLines(s string, n int) string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	if len(out) > n {
		out = out[len(out)-n:]
	}
	return strings.Join(out, "\n")
}

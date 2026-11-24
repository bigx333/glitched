package projects

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"glitched/internal/api"
)

// signedIn is a fake gh's script that answers gh auth status, as a
// signed-in gh does, and does body for gh api.
func signedIn(body string) string {
	return "[ \"$1\" = auth ] && exit 0\n" + body
}

// reposOf collects the pages Repos gives.
func reposOf(t *testing.T, bin string) ([]api.RepoPage, error) {
	t.Helper()
	var pages []api.RepoPage
	err := Repos(context.Background(), bin, func(p api.RepoPage) { pages = append(pages, p) })
	return pages, err
}

// Repos asks gh, signed in, for the repositories it reaches, which it
// emits an object a line, a null description as none. They come a page
// of a hundred at a time, the last holding what's left, Last set.
func TestReposListsWhatGhSaysInPages(t *testing.T) {
	bin := fakes(t, map[string]string{"gh": signedIn(strings.Join([]string{
		`echo '{"name":"acme/lighthouse","description":"A beacon","private":true,"fork":false}'`,
		`echo '{"name":"acme/harbor","description":null,"private":false,"fork":true}'`,
	}, "\n"))})
	pages, err := reposOf(t, bin)
	if err != nil {
		t.Fatal(err)
	}
	want := []api.Repo{
		{Name: "acme/lighthouse", Description: "A beacon", Private: true},
		{Name: "acme/harbor", Fork: true},
	}
	if len(pages) != 1 || !pages[0].Last || !slices.Equal(pages[0].Repos, want) {
		t.Fatalf("listed %+v, want %+v in one Last page", pages, want)
	}
	args := argsOf(t, bin, "gh")
	i := slices.Index(args, "api")
	if i < 0 {
		t.Fatalf("gh never ran api: %q", args)
	}
	if want := []string{"api", "--paginate", reposQuery, "--jq", reposShape}; !slices.Equal(args[i:], want) {
		t.Fatalf("gh api ran with %q, want %q", args[i:], want)
	}
}

// More than a page of repositories comes a page at a time, the last
// holding the rest.
func TestReposComesInPages(t *testing.T) {
	bin := fakes(t, map[string]string{"gh": signedIn(
		"i=0; while [ $i -lt 150 ]; do echo '{\"name\":\"o/r'$i'\"}'; i=$((i+1)); done")})
	pages, err := reposOf(t, bin)
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 2 || len(pages[0].Repos) != reposPage || pages[0].Last || len(pages[1].Repos) != 50 || !pages[1].Last {
		t.Fatalf("the pages are %d of %d…, want 100 then a Last 50", len(pages), len(pages[0].Repos))
	}
	if pages[0].Repos[0].Name != "o/r0" || pages[1].Repos[49].Name != "o/r149" {
		t.Fatalf("the pages came as %q … %q", pages[0].Repos[0].Name, pages[1].Repos[49].Name)
	}
}

// More repositories than a listing holds stops the listing, the last
// page Truncated.
func TestReposStopsAtMaxRepos(t *testing.T) {
	bin := fakes(t, map[string]string{"gh": signedIn(fmt.Sprintf(
		"i=0; while [ $i -le %d ]; do echo '{\"name\":\"o/r\"}'; i=$((i+1)); done", maxRepos))})
	pages, err := reposOf(t, bin)
	if err != nil {
		t.Fatal(err)
	}
	last := pages[len(pages)-1]
	var n int
	for _, p := range pages {
		n += len(p.Repos)
	}
	if n != maxRepos || !last.Last || !last.Truncated {
		t.Fatalf("listed %d repositories, last %+v; want %d, Last and Truncated", n, last, maxRepos)
	}
}

// No repositories is one empty Last page.
func TestReposEmptyIsOneLastPage(t *testing.T) {
	bin := fakes(t, map[string]string{"gh": signedIn("exit 0")})
	pages, err := reposOf(t, bin)
	if err != nil || len(pages) != 1 || !pages[0].Last || len(pages[0].Repos) != 0 {
		t.Fatalf("listed %+v, %v; want one empty Last page", pages, err)
	}
}

// A gh that isn't signed in says how to sign in.
func TestReposSignedOut(t *testing.T) {
	bin := fakes(t, map[string]string{"gh": "exit 1"})
	if _, err := reposOf(t, bin); err == nil || err.Error() != noAuth {
		t.Fatalf("a signed-out gh: %v, want %q", err, noAuth)
	}
}

// With no gh the listing says so, and runs nothing.
func TestReposNoGH(t *testing.T) {
	if _, err := reposOf(t, t.TempDir()); err == nil || err.Error() != noGH {
		t.Fatalf("no gh: %v, want %q", err, noGH)
	}
}

// gh api failing says its last lines, and gives no Last page.
func TestReposFailureIsGhTail(t *testing.T) {
	bin := fakes(t, map[string]string{"gh": signedIn(
		"echo 'one' >&2\necho '' >&2\necho 'two' >&2\nexit 4")})
	pages, err := reposOf(t, bin)
	if err == nil || err.Error() != "one\ntwo" {
		t.Fatalf("gh api failing: %v, want its last lines", err)
	}
	if len(pages) != 0 {
		t.Fatalf("a failed listing gave pages %+v", pages)
	}
}

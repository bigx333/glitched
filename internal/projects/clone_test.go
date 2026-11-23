package projects

import "testing"

func TestParseSource(t *testing.T) {
	for _, tc := range []struct {
		in, url, repo, name string
	}{
		{"bigx333/glitched", "https://github.com/bigx333/glitched.git", "bigx333/glitched", "glitched"},
		{"  bigx333/glitched.git ", "https://github.com/bigx333/glitched.git", "bigx333/glitched", "glitched"},
		{"o-1/my_repo.js", "https://github.com/o-1/my_repo.js.git", "o-1/my_repo.js", "my_repo.js"},
		{"https://github.com/bigx333/glitched", "https://github.com/bigx333/glitched.git", "", "glitched"},
		{"https://github.com/bigx333/glitched.git", "https://github.com/bigx333/glitched.git", "", "glitched"},
		{"https://github.com/bigx333/glitched/tree/capy/branch", "https://github.com/bigx333/glitched.git", "", "glitched"},
		{"https://www.github.com/bigx333/glitched/blob/main/README.md", "https://github.com/bigx333/glitched.git", "", "glitched"},
		{"https://gitlab.com/group/sub/project.git", "https://gitlab.com/group/sub/project.git", "", "project"},
		{"https://git.example.com/scm/repo/", "https://git.example.com/scm/repo/", "", "repo"},
		{"ssh://git@example.com:2222/o/r.git", "ssh://git@example.com:2222/o/r.git", "", "r"},
		{"git@github.com:bigx333/glitched.git", "git@github.com:bigx333/glitched.git", "", "glitched"},
		{"git@gitlab.com:group/sub/thing", "git@gitlab.com:group/sub/thing", "", "thing"},
		{"git://example.com/r.git", "git://example.com/r.git", "", "r"},
		{"file:///srv/git/lighthouse.git", "file:///srv/git/lighthouse.git", "", "lighthouse"},
	} {
		got, ok := ParseSource(tc.in)
		if want := (Source{tc.url, tc.repo, tc.name}); !ok || got != want {
			t.Errorf("ParseSource(%q) = %+v, %v; want %+v", tc.in, got, ok, want)
		}
	}
	for _, in := range []string{
		"", "glitched", "bigx333/", "/glitched", "./glitched", "bigx333/..", "../x", "o/n/x",
		"o/n!", "o n/r", "https://github.com/bigx333", "https://github.com/", "https:///o/r.git",
		"ftp://example.com/r.git", "ssh:///r.git", "file:///", "https://example.com/", "git@github.com:", "mailto:a@b",
	} {
		if got, ok := ParseSource(in); ok {
			t.Errorf("ParseSource(%q) = %+v, want it refused", in, got)
		}
	}
}

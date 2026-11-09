package pack

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func newInstaller(t *testing.T) *Installer {
	t.Helper()
	state := t.TempDir()
	keys, err := LoadKeys(state)
	if err != nil {
		t.Fatal(err)
	}
	return &Installer{Dir: filepath.Join(state, "plugins"), Keys: keys}
}

// packageFile writes the plugin id at version, signed by key, as a package
// file at path.
func writePackage(t *testing.T, path string, key *Key, id, version string, extra map[string]any) {
	t.Helper()
	data, err := Pack(signedPlugin(t, key, id, version, extra))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// entries lists dir's names.
func entries(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	es, _ := os.ReadDir(dir)
	for _, e := range es {
		out = append(out, e.Name())
	}
	return out
}

func TestInstallFromAPackageFile(t *testing.T) {
	ctx := context.Background()
	inst := newInstaller(t)
	author := newKey(t)
	pkg := filepath.Join(t.TempDir(), "hello"+Extension)
	writePackage(t, pkg, author, "hello", "1.0.0", nil)

	r, err := inst.Install(ctx, Source{Location: pkg})
	if err != nil {
		t.Fatal(err)
	}
	if r.ID != "hello" || r.Decision != NeedsConsent || r.KeyID != author.ID() || r.Previous != "" || r.Folder != filepath.Join(inst.Dir, "hello") {
		t.Fatalf("result = %+v", r)
	}
	if id, err := Verify(r.Folder); err != nil || id != author.ID() {
		t.Fatalf("installed folder: %q, %v", id, err)
	}
	o, err := ReadOrigin(r.Folder)
	if err != nil || o.Kind != FromFile || o.Path != pkg || o.Key != author.Public() || o.Version != "1.0.0" {
		t.Fatalf("origin = %+v, %v", o, err)
	}
	if got := entries(t, inst.Dir); !reflect.DeepEqual(got, []string{"hello"}) {
		t.Fatalf("plugins dir holds %v", got)
	}
	if _, err := inst.Install(ctx, Source{Location: pkg}); !errors.Is(err, ErrUpToDate) {
		t.Fatalf("installing it again: %v", err)
	}

	own := filepath.Join(t.TempDir(), "mine"+Extension)
	writePackage(t, own, inst.Keys.Own, "mine", "1.0.0", nil)
	if r, err := inst.Install(ctx, Source{Location: own}); err != nil || r.Decision != Trusted {
		t.Fatalf("own plugin: %+v, %v", r, err)
	}

	// Another folder holding the same id refuses it.
	writePlugin(t, filepath.Join(inst.Dir, "dev-copy"), map[string][]byte{ManifestFile: manifest("third", "1", nil), "index.js": nil})
	third := filepath.Join(t.TempDir(), "third"+Extension)
	writePackage(t, third, author, "third", "1.0.0", nil)
	if _, err := inst.Install(ctx, Source{Location: third}); !errors.Is(err, ErrExists) {
		t.Fatalf("an id another folder holds: %v", err)
	}
}

func TestATrustedKeysPluginTurnsOnAtOnce(t *testing.T) {
	inst := newInstaller(t)
	author := newKey(t)
	inst.Keys.Trusted = append(inst.Keys.Trusted, author.Public())
	pkg := filepath.Join(t.TempDir(), "hello"+Extension)
	writePackage(t, pkg, author, "hello", "1.0.0", nil)
	if r, err := inst.Install(context.Background(), Source{Location: pkg}); err != nil || r.Decision != Trusted {
		t.Fatalf("%+v, %v", r, err)
	}
}

func TestUpdateReportsWhatItNewlyAsksFor(t *testing.T) {
	ctx := context.Background()
	inst := newInstaller(t)
	author := newKey(t)
	pkg := filepath.Join(t.TempDir(), "hello"+Extension)
	writePackage(t, pkg, author, "hello", "1.0.0", map[string]any{
		"capabilities": []string{"clipboard"},
		"permissions":  map[string]any{"network": []string{"api.example.com"}},
		"glitched":     map[string]any{"tools": []any{map[string]any{"name": "search", "description": "Searches"}}},
	})
	if _, err := inst.Install(ctx, Source{Location: pkg}); err != nil {
		t.Fatal(err)
	}
	if found, err := inst.CheckUpdates(ctx); err != nil || len(found) != 0 {
		t.Fatalf("before an update: %+v, %v", found, err)
	}

	writePackage(t, pkg, author, "hello", "1.1.0", map[string]any{
		"capabilities": []string{"clipboard", "notify"},
		"permissions":  map[string]any{"network": []string{"cdn.example.com"}, "process": []string{"git"}},
		"glitched": map[string]any{
			"programs": []string{"rg"},
			"tools":    []any{map[string]any{"name": "search", "description": "Searches better"}, "index"},
			"service":  "service.js",
		},
	})
	want := Diff{
		"capabilities": {Added: []string{"notify"}},
		"network":      {Added: []string{"cdn.example.com"}, Removed: []string{"api.example.com"}},
		"process":      {Added: []string{"git"}},
		"commands":     {Added: []string{"rg"}},
		"tools":        {Added: []string{"index"}},
		"service":      {Added: []string{`"service.js"`}},
	}
	found, err := inst.CheckUpdates(ctx)
	if err != nil || len(found) != 1 || found[0].Err != nil {
		t.Fatalf("CheckUpdates = %+v, %v", found, err)
	}
	if f := found[0]; f.ID != "hello" || f.Version != "1.1.0" || f.Previous != "1.0.0" || f.Decision != NeedsReconsent || !reflect.DeepEqual(f.Diff, want) {
		t.Fatalf("found = %+v", f)
	}
	if v := readFile(t, filepath.Join(inst.Dir, "hello", "lib/util.js")); !strings.Contains(v, "1.0.0") {
		t.Fatal("CheckUpdates installed it")
	}

	r, err := inst.Update(ctx, "hello")
	if err != nil {
		t.Fatal(err)
	}
	if r.Decision != NeedsReconsent || r.Version != "1.1.0" || !reflect.DeepEqual(r.Diff, want) {
		t.Fatalf("Update = %+v", r)
	}
	if v := readFile(t, filepath.Join(inst.Dir, "hello", "lib/util.js")); !strings.Contains(v, "1.1.0") {
		t.Fatal("Update didn't install 1.1.0")
	}
	if o, _ := ReadOrigin(r.Folder); o.Version != "1.1.0" || o.Digest == "" {
		t.Fatalf("origin = %+v", o)
	}
	if _, err := inst.Update(ctx, "hello"); !errors.Is(err, ErrUpToDate) {
		t.Fatalf("updating again: %v", err)
	}

	// Asking for less keeps the consent given; an older version is refused.
	writePackage(t, pkg, author, "hello", "1.2.0", map[string]any{"capabilities": []string{"clipboard"}})
	if r, err := inst.Update(ctx, "hello"); err != nil || r.Decision != SameConsent || r.Diff.Grows() {
		t.Fatalf("an update asking for less: %+v, %v", r, err)
	}
	writePackage(t, pkg, author, "hello", "1.1.9", nil)
	if _, err := inst.Update(ctx, "hello"); !errors.Is(err, ErrNotNewer) {
		t.Fatalf("a downgrade: %v", err)
	}
}

func TestAnUpdateSignedByAnotherKeyIsRefused(t *testing.T) {
	ctx := context.Background()
	inst := newInstaller(t)
	pkg := filepath.Join(t.TempDir(), "hello"+Extension)
	writePackage(t, pkg, newKey(t), "hello", "1.0.0", nil)
	if _, err := inst.Install(ctx, Source{Location: pkg}); err != nil {
		t.Fatal(err)
	}
	writePackage(t, pkg, newKey(t), "hello", "2.0.0", nil)
	if _, err := inst.Update(ctx, "hello"); !errors.Is(err, ErrKeyChanged) {
		t.Fatalf("Update = %v", err)
	}
	if _, err := inst.Install(ctx, Source{Location: pkg}); !errors.Is(err, ErrKeyChanged) {
		t.Fatalf("Install over it = %v", err)
	}
	if found, _ := inst.CheckUpdates(ctx); len(found) != 1 || !errors.Is(found[0].Err, ErrKeyChanged) {
		t.Fatalf("CheckUpdates = %+v", found)
	}
	if v := readFile(t, filepath.Join(inst.Dir, "hello", "lib/util.js")); !strings.Contains(v, "1.0.0") {
		t.Fatal("the installed version changed")
	}
}

func TestAFailedInstallLeavesNothingBehind(t *testing.T) {
	ctx := context.Background()
	inst := newInstaller(t)
	author := newKey(t)
	pkg := filepath.Join(t.TempDir(), "hello"+Extension)
	writePackage(t, pkg, author, "hello", "1.0.0", nil)
	fail := errors.New("the disk filled up")
	beforeSwap = func(string) error { return fail }
	t.Cleanup(func() { beforeSwap = nil })

	if _, err := inst.Install(ctx, Source{Location: pkg}); !errors.Is(err, fail) {
		t.Fatalf("Install = %v", err)
	}
	if got := entries(t, inst.Dir); len(got) != 0 {
		t.Fatalf("a failed install left %v", got)
	}

	beforeSwap = nil
	if _, err := inst.Install(ctx, Source{Location: pkg}); err != nil {
		t.Fatal(err)
	}
	before := readFile(t, filepath.Join(inst.Dir, "hello", SignatureFile))
	writePackage(t, pkg, author, "hello", "2.0.0", nil)
	beforeSwap = func(string) error { return fail }
	if _, err := inst.Update(ctx, "hello"); !errors.Is(err, fail) {
		t.Fatalf("Update = %v", err)
	}
	if got := entries(t, inst.Dir); !reflect.DeepEqual(got, []string{"hello"}) {
		t.Fatalf("a failed update left %v", got)
	}
	if readFile(t, filepath.Join(inst.Dir, "hello", SignatureFile)) != before {
		t.Fatal("a failed update changed the installed plugin")
	}

	// A package that doesn't verify writes nothing at all.
	beforeSwap = nil
	os.WriteFile(pkg, []byte(`{"format":"glitched-plugin:v1","files":{}}`), 0o644)
	other := newInstaller(t)
	if _, err := other.Install(ctx, Source{Location: pkg}); err == nil {
		t.Fatal("installed a broken package")
	}
	if _, err := os.Stat(other.Dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a refused package made the plugins dir")
	}
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t", "GIT_CONFIG_GLOBAL="+os.DevNull)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestInstallAndUpdateFromAGitRepository(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	ctx := context.Background()
	author := newKey(t)
	repo := t.TempDir()
	git(t, repo, "init", "-q", "-b", "main")
	commit := func(version string, extra map[string]any) string {
		dir := signedPlugin(t, author, "hello", version, extra)
		for _, rel := range []string{ManifestFile, SignatureFile, "index.js", "lib/util.js"} {
			writePlugin(t, filepath.Join(repo, "plugins/hello"), map[string][]byte{rel: must(os.ReadFile(filepath.Join(dir, rel)))})
		}
		git(t, repo, "add", "-A")
		git(t, repo, "commit", "-q", "-m", version)
		return git(t, repo, "rev-parse", "HEAD")
	}
	first := commit("1.0.0", nil)

	inst := newInstaller(t)
	r, err := inst.Install(ctx, Source{Location: "file://" + repo, Ref: "main", Subdir: "plugins/hello"})
	if err != nil {
		t.Fatal(err)
	}
	if r.Origin.Kind != FromGit || r.Origin.Commit != first || r.Origin.Subdir != "plugins/hello" || r.Decision != NeedsConsent {
		t.Fatalf("result = %+v", r)
	}
	if _, err := Verify(r.Folder); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(r.Folder, ".git")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the clone's .git was installed")
	}

	second := commit("1.1.0", map[string]any{"capabilities": []string{"clipboard"}})
	r, err = inst.Update(ctx, "hello")
	if err != nil {
		t.Fatal(err)
	}
	if r.Origin.Commit != second || r.Decision != NeedsReconsent || !reflect.DeepEqual(r.Diff, Diff{"capabilities": {Added: []string{"clipboard"}}}) {
		t.Fatalf("update = %+v", r)
	}

	// A repository without a plugin at the folder named.
	if _, err := newInstaller(t).Install(ctx, Source{Location: "file://" + repo, Subdir: "nope"}); err == nil {
		t.Fatal("installed from a folder that isn't there")
	}
	// An unsigned plugin in a repository.
	os.Remove(filepath.Join(repo, "plugins/hello", SignatureFile))
	git(t, repo, "commit", "-q", "-am", "unsign")
	if _, err := newInstaller(t).Install(ctx, Source{Location: "file://" + repo, Subdir: "plugins/hello"}); !errors.Is(err, ErrUnsigned) {
		t.Fatalf("an unsigned repository: %v", err)
	}
}

func TestGitRunsWithoutHooksOrOtherTransports(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	inst := newInstaller(t)
	repo := t.TempDir()
	git(t, repo, "init", "-q")
	// The https transport only, for a file:// repository: refused.
	if _, err := inst.git(context.Background(), "https", "", "clone", "--depth", "1", "--", "file://"+repo, filepath.Join(t.TempDir(), "c")); err == nil {
		t.Fatal("cloned over a transport it didn't allow")
	}
	if _, err := inst.git(context.Background(), "https", "", "ls-remote", "--", "ext::sh -c id"); err == nil {
		t.Fatal("ran an ext:: transport")
	}
	out, err := inst.git(context.Background(), "file", repo, "config", "core.hooksPath")
	if err != nil || out != os.DevNull {
		t.Fatalf("hooksPath = %q, %v", out, err)
	}
}

func TestInstallFromALinkAndAGitHubRelease(t *testing.T) {
	ctx := context.Background()
	author := newKey(t)
	pkgs := map[string][]byte{}
	for _, v := range []string{"1.0.0", "2.0.0"} {
		pkgs[v] = must(Pack(signedPlugin(t, author, "hello", v, nil)))
	}
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		release := func(v string) {
			json.NewEncoder(w).Encode(map[string]any{"assets": []any{
				map[string]any{"name": "notes.txt", "browser_download_url": srv.URL + "/notes.txt"},
				map[string]any{"name": "hello" + Extension, "browser_download_url": srv.URL + "/dl/" + v + "/hello" + Extension},
			}})
		}
		switch p := r.URL.Path; {
		case p == "/api/repos/owner/repo/releases/latest":
			release("2.0.0")
		case p == "/api/repos/owner/repo/releases/tags/v1":
			release("1.0.0")
		case strings.HasPrefix(p, "/dl/"):
			w.Write(pkgs[strings.Split(p, "/")[2]])
		case p == "/redirect"+Extension:
			http.Redirect(w, r, "http://example.com/x"+Extension, http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	with := func() *Installer {
		inst := newInstaller(t)
		inst.HTTP, inst.GitHubAPI, inst.GitHubWeb = srv.Client(), srv.URL+"/api", srv.URL
		return inst
	}

	link := srv.URL + "/dl/1.0.0/hello" + Extension
	r, err := with().Install(ctx, Source{Location: link})
	if err != nil || r.Version != "1.0.0" || r.Origin.Kind != FromURL || r.Origin.URL != link {
		t.Fatalf("from a link: %+v, %v", r, err)
	}

	inst := with()
	r, err = inst.Install(ctx, Source{Location: "owner/repo/releases/tag/v1"})
	if err != nil || r.Version != "1.0.0" || r.Origin.Kind != FromRelease || r.Origin.Repo != "owner/repo" || r.Origin.Tag != "v1" {
		t.Fatalf("from a release tag: %+v, %v", r, err)
	}

	inst = with()
	r, err = inst.Install(ctx, Source{Location: "owner/repo"})
	if err != nil || r.Version != "2.0.0" || r.Origin.Kind != FromRelease || r.Origin.Tag != "" {
		t.Fatalf("from a repository's latest release: %+v, %v", r, err)
	}
	if found, err := inst.CheckUpdates(ctx); err != nil || len(found) != 0 {
		t.Fatalf("CheckUpdates = %+v, %v", found, err)
	}

	if _, err := with().Install(ctx, Source{Location: srv.URL + "/redirect" + Extension}); err == nil || !strings.Contains(err.Error(), "off https") {
		t.Fatalf("followed a redirect to http: %v", err)
	}
	if _, err := with().Install(ctx, Source{Location: srv.URL + "/missing" + Extension}); err == nil {
		t.Fatal("installed a 404")
	}
	if _, err := with().Install(ctx, Source{Location: "http://example.com/x" + Extension}); err == nil {
		t.Fatal("fetched over http")
	}
}

func TestSourcesResolve(t *testing.T) {
	inst := &Installer{}
	for _, tc := range []struct {
		src  Source
		want Origin
	}{
		{Source{Location: "owner/repo"}, Origin{Kind: FromRelease, Repo: "owner/repo", orGit: true}},
		{Source{Location: "github.com/owner/repo"}, Origin{Kind: FromRelease, Repo: "owner/repo", orGit: true}},
		{Source{Location: "https://github.com/owner/repo/releases/latest"}, Origin{Kind: FromRelease, Repo: "owner/repo"}},
		{Source{Location: "https://github.com/owner/repo/releases/tag/v1.2"}, Origin{Kind: FromRelease, Repo: "owner/repo", Tag: "v1.2"}},
		{Source{Location: "owner/repo", Ref: "dev"}, Origin{Kind: FromGit, URL: "https://github.com/owner/repo.git", Ref: "dev"}},
		{Source{Location: "https://github.com/owner/repo.git"}, Origin{Kind: FromRelease, Repo: "owner/repo", orGit: true}},
		{Source{Location: "https://github.com/owner/repo/tree/main/plugins/x"}, Origin{Kind: FromGit, URL: "https://github.com/owner/repo.git", Ref: "main", Subdir: "plugins/x"}},
		{Source{Location: "https://example.com/p/x" + Extension}, Origin{Kind: FromURL, URL: "https://example.com/p/x" + Extension}},
		{Source{Location: "https://git.example.com/x.git", Subdir: "p"}, Origin{Kind: FromGit, URL: "https://git.example.com/x.git", Subdir: "p"}},
		{Source{Location: "file:///srv/repo"}, Origin{Kind: FromGit, URL: "file:///srv/repo"}},
	} {
		o, err := inst.resolve(tc.src)
		tc.want.Source = tc.src.Location
		if tc.want.Ref == "" {
			tc.want.Ref = tc.src.Ref
		}
		if err != nil || o != tc.want {
			t.Errorf("%+v: %+v, %v; want %+v", tc.src, o, err, tc.want)
		}
	}
	for _, src := range []Source{
		{Location: "http://example.com/x" + Extension},
		{Location: "ssh://git@github.com/o/r.git"},
		{Location: "git@github.com:o/r.git"},
		{Location: "ext::sh -c id"},
		{Location: "ext::sh"},
		{Location: "--upload-pack=touch"},
		{Location: "https://user:pw@example.com/x.git"},
		{Location: "owner/repo", Ref: "--upload-pack=x"},
		{Location: "owner/repo", Ref: "a..b"},
		{Location: "owner/repo", Subdir: "../x"},
		{Location: "https://github.com/owner/repo/tree/main/../../x"},
		{Location: "https://github.com/owner/repo/issues"},
		{Location: ""},
	} {
		if o, err := inst.resolve(src); err == nil {
			t.Errorf("%+v resolved, to %+v", src, o)
		}
	}
}

func TestNewer(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"1.0.1", "1.0.0", true}, {"2", "1.9", true}, {"1.10.0", "1.9.0", true}, {"v1.1", "1.0", true},
		{"1.0.0", "1.0.0-beta", true}, {"1.0.0-rc.1", "1.0.0-beta", true},
		{"1.0.0", "1.0.0", false}, {"1.0", "1.0.0", false}, {"1.0.0-beta", "1.0.0", false}, {"0.9", "1.0", false},
		{"banana", "1.0", false}, {"1.0", "", false},
	} {
		if got := newer(tc.a, tc.b); got != tc.want {
			t.Errorf("newer(%q, %q) = %v", tc.a, tc.b, got)
		}
	}
}

func TestPrereleasesOrderBySemVer(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"1.0.0-rc.10", "1.0.0-rc.9", true},
		{"1.0.0-rc.2", "1.0.0-rc.10", false},
		{"1.0.0-rc.1", "1.0.0-beta.9", true},
		{"1.0.0-alpha.1", "1.0.0-alpha", true},
		{"1.0.0-1", "1.0.0-alpha", false},
		{"1.0.0", "1.0.0-rc.99", true},
	} {
		if got := newer(c.a, c.b); got != c.want {
			t.Errorf("newer(%s, %s) = %v", c.a, c.b, got)
		}
	}
}

func TestHugeNumericPrereleasesOrder(t *testing.T) {
	if !newer("1.0.0-100000000000000000000", "1.0.0-99999999999999999999") || newer("1.0.0-rc.01", "1.0.0-rc.1") || !newer("1.0.0-rc.010", "1.0.0-rc.9") {
		t.Fatal("misordered")
	}
}

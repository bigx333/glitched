package pack

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	plugin "github.com/yxxbc/mygo-plugin"
)

// Source is where to install a plugin from, as the user gives it.
//
// Location is a package file's path; an https link to a package (ending
// .glitched-plugin); a GitHub release (…/releases/latest,
// …/releases/tag/<tag>), from its .glitched-plugin asset; a GitHub
// repository (owner/repo, its page, …/tree/<branch>/<folder>), whose
// latest release's asset is used if it has one and its default branch
// otherwise; or any https git repository, or a local one as file://.
//
// Ref and Subdir pick a git branch or tag, and the plugin's folder in the
// repository; either makes a GitHub repository a git one.
type Source struct {
	Location string
	Ref      string
	Subdir   string
}

// Kind is what kind of place a plugin came from.
type Kind string

const (
	FromFile    Kind = "file"
	FromURL     Kind = "url"
	FromRelease Kind = "release"
	FromGit     Kind = "git"
)

// Origin is where an installed plugin came from, kept in its folder's
// .origin.json, so updates come from the same place and need the same key.
type Origin struct {
	Kind Kind `json:"kind"`
	// Source is the location as the user gave it.
	Source string `json:"source"`
	// Path is a package file's absolute path (FromFile).
	Path string `json:"path,omitempty"`
	// URL is the package's link (FromURL) or the repository's (FromGit).
	URL string `json:"url,omitempty"`
	// Repo and Tag name a GitHub release (FromRelease); no Tag is the latest.
	Repo string `json:"repo,omitempty"`
	Tag  string `json:"tag,omitempty"`
	// Ref, Subdir and Commit are the branch or tag, the plugin's folder in
	// the repository and the commit installed (FromGit).
	Ref    string `json:"ref,omitempty"`
	Subdir string `json:"subdir,omitempty"`
	Commit string `json:"commit,omitempty"`

	// Key is the public key that signed what's installed, which an update
	// must be signed by too.
	Key         string    `json:"key"`
	Version     string    `json:"version,omitempty"`
	Digest      string    `json:"digest"`
	InstalledAt time.Time `json:"installedAt"`

	// orGit: a bare GitHub repository, tried as a release, then as git.
	orGit bool
}

// ReadOrigin reads where the plugin in folder came from.
func ReadOrigin(folder string) (*Origin, error) {
	data, err := os.ReadFile(filepath.Join(folder, OriginFile))
	if err != nil {
		return nil, err
	}
	var o Origin
	if err := json.Unmarshal(data, &o); err != nil {
		return nil, fmt.Errorf("%s: %w", OriginFile, err)
	}
	return &o, nil
}

var errNoRelease = errors.New("no release with a " + Extension + " asset")

const fetchTimeout = 2 * time.Minute

// resolve says what src names, without reaching it.
func (i *Installer) resolve(src Source) (Origin, error) {
	loc := strings.TrimSpace(src.Location)
	o := Origin{Source: loc, Ref: strings.TrimSpace(src.Ref), Subdir: strings.Trim(strings.TrimSpace(src.Subdir), "/")}
	if !refOK(o.Ref) {
		return o, fmt.Errorf("%q can't be a branch or tag", o.Ref)
	}
	if o.Subdir != "" && !plainPath(o.Subdir) {
		return o, fmt.Errorf("%q isn't a folder inside the repository", o.Subdir)
	}
	gitOnly := o.Ref != "" || o.Subdir != ""
	if fi, err := os.Stat(loc); err == nil && fi.Mode().IsRegular() && !gitOnly {
		o.Kind, o.Path = FromFile, loc
		if abs, err := filepath.Abs(loc); err == nil {
			o.Path = abs
		}
		return o, nil
	}
	web := i.githubWeb()
	switch {
	case loc == "" || strings.IndexFunc(loc, unicode.IsSpace) >= 0:
		return o, fmt.Errorf("%q isn't a package file, a link or a repository", loc)
	case strings.Contains(loc, "://"):
	case strings.HasPrefix(loc, "github.com/"):
		loc = "https://" + loc
	case !strings.Contains(strings.Split(loc, "/")[0], ".") && strings.Count(loc, "/") >= 1:
		loc = web + "/" + loc
	default:
		return o, fmt.Errorf("%q isn't a package file, a link or a repository", loc)
	}
	u, err := url.Parse(loc)
	if err != nil {
		return o, err
	}
	switch {
	case u.Scheme == "file":
		o.Kind, o.URL = FromGit, loc
		return o, nil
	case u.Scheme != "https" || u.Host == "" || u.User != nil:
		return o, fmt.Errorf("%q: plugins come from https links, package files and file:// repositories", loc)
	case strings.HasSuffix(u.Path, Extension) && !gitOnly:
		o.Kind, o.URL = FromURL, loc
		return o, nil
	}
	if wu, err := url.Parse(web); err != nil || u.Host != wu.Host {
		o.Kind, o.URL = FromGit, loc
		return o, nil
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 2 {
		return o, fmt.Errorf("%q isn't a repository", loc)
	}
	o.Repo = parts[0] + "/" + strings.TrimSuffix(parts[1], ".git")
	if !plugin.ValidRepo(o.Repo) {
		return o, fmt.Errorf("%q isn't a repository", loc)
	}
	git := func() (Origin, error) {
		o.Kind, o.URL, o.Repo = FromGit, web+"/"+o.Repo+".git", ""
		return o, nil
	}
	switch rest := parts[2:]; {
	case len(rest) == 0 && gitOnly:
		return git()
	case len(rest) == 0:
		o.Kind, o.orGit = FromRelease, true
		return o, nil
	case !gitOnly && len(rest) <= 2 && rest[0] == "releases" && (len(rest) == 1 || rest[1] == "latest"):
		o.Kind = FromRelease
		return o, nil
	case !gitOnly && len(rest) == 3 && rest[0] == "releases" && rest[1] == "tag" && refOK(rest[2]):
		o.Kind, o.Tag = FromRelease, rest[2]
		return o, nil
	case len(rest) >= 2 && rest[0] == "tree" && refOK(rest[1]):
		if o.Ref == "" {
			o.Ref = rest[1]
		}
		if sub := strings.Join(rest[2:], "/"); o.Subdir == "" && sub != "" {
			if !plainPath(sub) {
				return o, fmt.Errorf("%q isn't a folder inside the repository", sub)
			}
			o.Subdir = sub
		}
		return git()
	}
	return o, fmt.Errorf("%q isn't a package, a release or a repository", loc)
}

// refOK reports whether r can be passed to git as a branch or tag: never
// as an option, a path out, or something a shell or URL would read.
func refOK(r string) bool {
	return r == "" || len(r) <= 200 && !strings.HasPrefix(r, "-") && !strings.Contains(r, "..") &&
		!strings.ContainsFunc(r, func(c rune) bool {
			return unicode.IsSpace(c) || unicode.IsControl(c) || strings.ContainsRune(`:\~^?*[`, c)
		})
}

// fetch reads the plugin o names and verifies it, writing nothing outside
// a temporary clone. It returns o with what it found (a git commit, or the
// kind a bare GitHub repository turned out to be).
func (i *Installer) fetch(ctx context.Context, o Origin) (*Bundle, Origin, error) {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	var data []byte
	var err error
	switch o.Kind {
	case FromFile:
		data, err = readPackageFile(o.Path)
	case FromURL:
		data, err = i.download(ctx, o.URL)
	case FromRelease:
		var asset string
		asset, err = i.releaseAsset(ctx, o.Repo, o.Tag)
		if errors.Is(err, errNoRelease) && o.orGit {
			o.Kind, o.URL, o.Repo, o.orGit = FromGit, i.githubWeb()+"/"+o.Repo+".git", "", false
			return i.fetch(ctx, o)
		}
		if err == nil {
			data, err = i.download(ctx, asset)
		}
	case FromGit:
		var b *Bundle
		b, o.Commit, err = i.fromGit(ctx, o)
		if err == nil {
			_, err = b.Manifest()
		}
		return b, o, err
	default:
		err = fmt.Errorf("unknown origin %q", o.Kind)
	}
	if err != nil {
		return nil, o, err
	}
	o.orGit = false
	b, err := Unpack(data)
	return b, o, err
}

func readPackageFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return readLimited(f)
}

func readLimited(r io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, MaxPackageBytes+1))
	if err == nil && len(data) > MaxPackageBytes {
		err = fmt.Errorf("%w: the package is over %d MiB", ErrTooLarge, MaxPackageBytes>>20)
	}
	return data, err
}

func (i *Installer) githubWeb() string {
	if i.GitHubWeb != "" {
		return strings.TrimSuffix(i.GitHubWeb, "/")
	}
	return "https://github.com"
}

func (i *Installer) githubAPI() string {
	if i.GitHubAPI != "" {
		return strings.TrimSuffix(i.GitHubAPI, "/")
	}
	return "https://api.github.com"
}

// client is the HTTP client, which never follows a redirect off https.
func (i *Installer) client() *http.Client {
	c := http.Client{Timeout: fetchTimeout}
	if i.HTTP != nil {
		c = *i.HTTP
	}
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != "https" {
			return fmt.Errorf("redirected off https, to %s", req.URL)
		}
		if len(via) >= 10 {
			return errors.New("too many redirects")
		}
		return nil
	}
	return &c
}

func (i *Installer) get(ctx context.Context, link, accept string) (*http.Response, error) {
	if u, err := url.Parse(link); err != nil || u.Scheme != "https" {
		return nil, fmt.Errorf("%q isn't an https link", link)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, link, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("User-Agent", "Glitched")
	return i.client().Do(req)
}

// download reads the package at link, up to the largest a package may be.
func (i *Installer) download(ctx context.Context, link string) ([]byte, error) {
	resp, err := i.get(ctx, link, "application/octet-stream")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("downloading %s: %s", link, resp.Status)
	}
	if resp.ContentLength > MaxPackageBytes {
		return nil, fmt.Errorf("%w: the package is over %d MiB", ErrTooLarge, MaxPackageBytes>>20)
	}
	return readLimited(resp.Body)
}

// releaseAsset is the link to the .glitched-plugin asset of repo's release
// tag, or its latest release when tag is empty: the first such asset, as
// GitHub lists them.
func (i *Installer) releaseAsset(ctx context.Context, repo, tag string) (string, error) {
	api := i.githubAPI() + "/repos/" + repo + "/releases/latest"
	if tag != "" {
		api = i.githubAPI() + "/repos/" + repo + "/releases/tags/" + url.PathEscape(tag)
	}
	resp, err := i.get(ctx, api, "application/vnd.github+json")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return "", fmt.Errorf("%s: %w", repo, errNoRelease)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("asking GitHub for %s's release: %s", repo, resp.Status)
	}
	var release struct {
		Assets []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&release); err != nil {
		return "", fmt.Errorf("reading %s's release: %w", repo, err)
	}
	for _, a := range release.Assets {
		if strings.HasSuffix(a.Name, Extension) {
			return a.URL, nil
		}
	}
	return "", fmt.Errorf("%s: %w", repo, errNoRelease)
}

// fromGit clones o's repository at its ref, shallowly, into a temporary
// folder, and reads the plugin at its subdir as a verified bundle. git runs
// with only o's own transport allowed (no ext::, ssh or other protocol, for
// the repository or anything it names), no hooks or templates, no
// submodules, LFS or prompts, and a deadline.
func (i *Installer) fromGit(ctx context.Context, o Origin) (*Bundle, string, error) {
	u, err := url.Parse(o.URL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "file") {
		return nil, "", fmt.Errorf("%q isn't an https or file:// repository", o.URL)
	}
	tmp, err := os.MkdirTemp("", "glitched-plugin-")
	if err != nil {
		return nil, "", err
	}
	defer os.RemoveAll(tmp)
	repo := filepath.Join(tmp, "repo")
	args := []string{"clone", "--depth", "1", "--single-branch", "--no-tags", "--template="}
	if o.Ref != "" {
		args = append(args, "--branch", o.Ref)
	}
	if _, err := i.git(ctx, u.Scheme, "", append(args, "--", o.URL, repo)...); err != nil {
		return nil, "", err
	}
	commit, err := i.git(ctx, u.Scheme, repo, "rev-parse", "HEAD")
	if err != nil {
		return nil, "", err
	}
	folder := repo
	if o.Subdir != "" {
		folder = filepath.Join(repo, filepath.FromSlash(o.Subdir))
	}
	b, err := readFolder(folder)
	if errors.Is(err, os.ErrNotExist) {
		return nil, "", fmt.Errorf("%s has no plugin at %q", o.URL, o.Subdir)
	}
	return b, commit, err
}

func (i *Installer) git(ctx context.Context, scheme, dir string, args ...string) (string, error) {
	bin := i.Git
	if bin == "" {
		bin = "git"
	}
	full := []string{
		"-c", "protocol.allow=never",
		"-c", "protocol." + scheme + ".allow=always",
		"-c", "core.hooksPath=" + os.DevNull,
		"-c", "core.fsmonitor=false",
		"-c", "submodule.recurse=false",
	}
	if dir != "" {
		full = append(full, "-C", dir)
	}
	cmd := exec.CommandContext(ctx, bin, append(full, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=never", "GIT_LFS_SKIP_SMUDGE=1")
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 500 {
			msg = msg[:500]
		}
		return "", fmt.Errorf("git %s: %w: %s", args[0], err, msg)
	}
	return strings.TrimSpace(out.String()), nil
}

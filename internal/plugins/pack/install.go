package pack

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	plugin "github.com/yxxbc/mygo-plugin"
)

// Installer installs plugins into Dir and updates them from where they
// came from. Its zero value but for Dir and Keys is ready to use. It
// installs one plugin at a time; two processes installing into one Dir at
// once are not guarded against.
type Installer struct {
	// Dir is <state>/plugins.
	Dir  string
	Keys *Keys
	// HTTP fetches packages and asks GitHub for releases; nil is a client
	// with a two-minute timeout. Only https is fetched.
	HTTP *http.Client
	// GitHubAPI and GitHubWeb are GitHub's API and site; empty is GitHub's.
	GitHubAPI, GitHubWeb string
	// Git is the git program; empty is "git" on the PATH.
	Git string

	mu sync.Mutex
}

// Decision is what the host does with a plugin just installed or updated.
type Decision string

const (
	// Trusted: signed by this device's key or one the user trusts; it
	// turns on at once.
	Trusted Decision = "trusted"
	// NeedsConsent: newly installed, signed by another key; the host keeps
	// it off until the user allows it.
	NeedsConsent Decision = "needs-consent"
	// NeedsReconsent: an update, by the same other key, that asks for
	// something the version it replaced didn't; the host keeps it off
	// until the user allows it again.
	NeedsReconsent Decision = "needs-reconsent"
	// SameConsent: an update, by the same other key, that asks for nothing
	// new; what the user decided for the version it replaced holds.
	SameConsent Decision = "same-consent"
)

var (
	// ErrKeyChanged refuses a plugin signed by another key than the one
	// installed under its id.
	ErrKeyChanged = errors.New("it's signed by another key than the one installed")
	// ErrUpToDate says what was fetched is what's installed.
	ErrUpToDate = errors.New("it's up to date")
	// ErrNotNewer refuses a different build that isn't a later version, so
	// an old version's holes stay closed.
	ErrNotNewer = errors.New("it isn't a later version than the one installed")
	// ErrExists refuses to install over a plugin Install can't vouch for.
	ErrExists = errors.New("a plugin with that id is installed already")
)

// Result is a plugin installed or updated, or an update found.
type Result struct {
	ID      string
	Folder  string
	Version string
	KeyID   string
	// Previous is the version it replaces, empty for a new install.
	Previous string
	Decision Decision
	// Diff is what it asks for that the version it replaces didn't, and
	// what it no longer asks for.
	Diff   Diff
	Origin Origin
}

// Install fetches the plugin src names, verifies it before writing
// anything, and installs it into Dir/<id> atomically. Over an installed
// plugin of the same id, it must be signed by the same key and be a later
// version: an update from a new place.
func (i *Installer) Install(ctx context.Context, src Source) (*Result, error) {
	o, err := i.resolve(src)
	if err != nil {
		return nil, err
	}
	b, o, err := i.fetch(ctx, o)
	if err != nil {
		return nil, err
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	r, err := i.plan(b, o)
	if err != nil {
		return nil, err
	}
	return r, i.commit(b, r)
}

// Update installs the plugin id's latest version from where it came from,
// if it's later and signed by the same key.
func (i *Installer) Update(ctx context.Context, id string) (*Result, error) {
	b, o, err := i.fetchUpdate(ctx, id)
	if err != nil {
		return nil, err
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	r, err := i.plan(b, o)
	if err != nil {
		return nil, err
	}
	return r, i.commit(b, r)
}

func (i *Installer) fetchUpdate(ctx context.Context, id string) (*Bundle, Origin, error) {
	if id == "" || strings.HasPrefix(id, ".") || strings.ContainsAny(id, `/\`) {
		return nil, Origin{}, fmt.Errorf("%q isn't a plugin id", id)
	}
	o, err := ReadOrigin(filepath.Join(i.Dir, id))
	if err != nil {
		return nil, Origin{}, fmt.Errorf("%s wasn't installed from anywhere it can be updated from: %w", id, err)
	}
	return i.fetch(ctx, *o)
}

// Found is an update CheckUpdates found, or why the check failed.
type Found struct {
	Result
	Err error
}

// CheckUpdates fetches each installed plugin from where it came from, and
// lists the ones with a later version, and the ones whose check failed
// (another key signed it, it can't be reached). It installs nothing.
func (i *Installer) CheckUpdates(ctx context.Context) ([]Found, error) {
	entries, err := os.ReadDir(i.Dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Found
	for _, e := range entries {
		id := e.Name()
		if !e.IsDir() || strings.HasPrefix(id, ".") {
			continue
		}
		if _, err := os.Stat(filepath.Join(i.Dir, id, OriginFile)); err != nil {
			continue // put there by hand, not installed: nowhere to update from
		}
		b, o, err := i.fetchUpdate(ctx, id)
		var r *Result
		if err == nil {
			i.mu.Lock()
			r, err = i.plan(b, o)
			i.mu.Unlock()
		}
		switch {
		case errors.Is(err, ErrUpToDate), errors.Is(err, ErrNotNewer):
		case err != nil:
			out = append(out, Found{Result{ID: id, Folder: filepath.Join(i.Dir, id)}, err})
		default:
			out = append(out, Found{Result: *r})
		}
	}
	return out, nil
}

// installed is what's in a plugin's folder now, as far as an update needs.
type installed struct {
	key, version, digest string
	manifest             []byte
}

// current reads the plugin in folder: its key, version and digest from
// where it was installed from, which hold even if its files were changed
// since, or else from its signature, if its files still match it.
func current(folder string) (*installed, error) {
	if _, err := os.Lstat(folder); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	manifest, _ := os.ReadFile(filepath.Join(folder, ManifestFile))
	in := &installed{manifest: manifest}
	if m, err := plugin.ParseManifest(manifest); err == nil {
		in.version = m.Version
	}
	if o, err := ReadOrigin(folder); err == nil && o.Key != "" {
		in.key, in.digest, in.version = o.Key, o.Digest, o.Version
		return in, nil
	}
	b, err := readFolder(folder)
	if err != nil {
		return nil, fmt.Errorf("%w, and it isn't signed (%v); remove it first", ErrExists, err)
	}
	in.key, in.digest = b.Key(), b.Digest()
	return in, nil
}

// plan decides what installing b from o does, without writing anything.
func (i *Installer) plan(b *Bundle, o Origin) (*Result, error) {
	m, err := b.Manifest()
	if err != nil {
		return nil, err
	}
	if err := i.idTaken(m.ID); err != nil {
		return nil, err
	}
	folder := filepath.Join(i.Dir, m.ID)
	prev, err := current(folder)
	if err != nil {
		return nil, err
	}
	o.Key, o.Version, o.Digest, o.InstalledAt = b.Key(), m.Version, b.Digest(), time.Now().UTC()
	r := &Result{ID: m.ID, Folder: folder, Version: m.Version, KeyID: KeyID(b.Key()), Origin: o}
	switch {
	case i.Keys.Trusts(b.Key()) && prev == nil:
		r.Decision = Trusted
	case prev == nil:
		r.Decision = NeedsConsent
	case prev.key != b.Key():
		return nil, fmt.Errorf("%s: %w (%s, not %s)", m.ID, ErrKeyChanged, KeyID(b.Key()), KeyID(prev.key))
	case prev.digest == b.Digest():
		return nil, fmt.Errorf("%s %s: %w", m.ID, m.Version, ErrUpToDate)
	case !newer(m.Version, prev.version):
		return nil, fmt.Errorf("%s %s: %w (%s)", m.ID, m.Version, ErrNotNewer, prev.version)
	default:
		r.Previous = prev.version
		r.Diff = diff(prev.manifest, b.Files[ManifestFile])
		switch {
		case i.Keys.Trusts(b.Key()):
			r.Decision = Trusted
		case r.Diff.Grows():
			r.Decision = NeedsReconsent
		default:
			r.Decision = SameConsent
		}
	}
	return r, nil
}

// idTaken refuses an id another folder in Dir already holds: the host
// loads only the first, and both would share its data.
func (i *Installer) idTaken(id string) error {
	entries, err := os.ReadDir(i.Dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, e := range entries {
		if e.Name() == id || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if m, err := plugin.ReadManifest(filepath.Join(i.Dir, e.Name())); err == nil && m.ID == id {
			return fmt.Errorf("%w, in the folder %s", ErrExists, e.Name())
		}
	}
	return nil
}

const incomingDir = ".incoming"

// beforeSwap, when set by a test, runs just before a staged plugin takes
// its folder; an error from it is a failure there.
var beforeSwap func(staged string) error

// commit writes b, with r's origin, into a staging folder under
// Dir/.incoming, checks it again as written, and swaps it into r.Folder.
// A failure anywhere leaves r.Folder as it was and removes the staging.
func (i *Installer) commit(b *Bundle, r *Result) error {
	incoming := filepath.Join(i.Dir, incomingDir)
	if err := os.MkdirAll(incoming, 0o755); err != nil {
		return err
	}
	defer os.Remove(incoming) // when it's empty
	sweepStale(incoming, "")
	staged, err := os.MkdirTemp(incoming, r.ID+"-")
	if err != nil {
		return err
	}
	defer removeTree(staged)
	if err := i.stage(staged, b, r.Origin); err != nil {
		return err
	}
	if beforeSwap != nil {
		if err := beforeSwap(staged); err != nil {
			return err
		}
	}
	if r.Previous == "" {
		return os.Rename(staged, r.Folder)
	}
	old := staged + "-old"
	if err := os.Rename(r.Folder, old); err != nil {
		return err
	}
	if err := os.Rename(staged, r.Folder); err != nil {
		return errors.Join(err, os.Rename(old, r.Folder))
	}
	removeTree(old)
	return nil
}

func (i *Installer) stage(staged string, b *Bundle, o Origin) error {
	if err := os.Chmod(staged, 0o755); err != nil {
		return err
	}
	if err := b.write(staged, 0o644); err != nil {
		return err
	}
	data, err := json.MarshalIndent(o, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(staged, OriginFile), append(data, '\n'), 0o644); err != nil {
		return err
	}
	// What's about to be installed is what was verified: on a filesystem
	// that folds case, two files can have become one.
	again, err := readFolder(staged)
	if err != nil {
		return fmt.Errorf("it changed while it was installed: %w", err)
	}
	if again.Key() != b.Key() || again.Digest() != b.Digest() {
		return errors.New("it changed while it was installed")
	}
	return nil
}

// sweepStale removes what a crashed install or snapshot left in dir: an
// entry named with prefix over an hour old, which no running one still
// writes.
func sweepStale(dir, prefix string) {
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), prefix) {
			continue
		}
		if fi, err := e.Info(); err == nil && time.Since(fi.ModTime()) > time.Hour {
			removeTree(filepath.Join(dir, e.Name()))
		}
	}
}

// Change is what an update adds to one kind of thing a plugin asks for,
// and what it drops.
type Change struct {
	Added   []string `json:"added,omitempty"`
	Removed []string `json:"removed,omitempty"`
}

// Diff is an update's changes to what a plugin asks for, by kind:
// "capabilities", "network" (permissions.network hosts), "process"
// (permissions.process programs), "commands" (the programs it runs),
// "tools" and "service". Kinds that don't change are left out.
type Diff map[string]Change

// Grows reports whether the update asks for anything new.
func (d Diff) Grows() bool {
	for _, c := range d {
		if len(c.Added) > 0 {
			return true
		}
	}
	return false
}

var kinds = []string{"capabilities", "network", "process", "commands", "tools", "service"}

// asks reads what plugin.json asks for, by Diff's kinds. An entry of
// commands or tools is its name, a string or an object's "name"; any other
// shape, and the service, is its JSON, so any change to it shows.
func asks(manifest []byte) map[string][]string {
	var m struct {
		Capabilities []string `json:"capabilities"`
		Permissions  struct {
			Network []string        `json:"network"`
			Process plugin.Programs `json:"process"`
		} `json:"permissions"`
		// Glitched's own block: the programs it runs, its tools, its
		// service.
		Glitched struct {
			Programs []json.RawMessage `json:"programs"`
			Tools    []json.RawMessage `json:"tools"`
			Service  json.RawMessage   `json:"service"`
		} `json:"glitched"`
	}
	json.Unmarshal(manifest, &m) // what doesn't read asks for nothing: an update past it asks for all it lists
	out := map[string][]string{
		"capabilities": m.Capabilities,
		"network":      m.Permissions.Network,
		"process":      m.Permissions.Process,
		"commands":     names(m.Glitched.Programs),
		"tools":        names(m.Glitched.Tools),
	}
	if s := canonical(m.Glitched.Service); s != "" && s != "null" {
		out["service"] = []string{s}
	}
	return out
}

func names(raws []json.RawMessage) []string {
	var out []string
	for _, raw := range raws {
		var named struct {
			Name string `json:"name"`
		}
		var s string
		switch {
		case json.Unmarshal(raw, &s) == nil:
			out = append(out, s)
		case json.Unmarshal(raw, &named) == nil && named.Name != "":
			out = append(out, named.Name)
		default:
			out = append(out, canonical(raw))
		}
	}
	return out
}

// canonical is raw as encoding/json writes it back: keys sorted, no
// spaces, so reordering a manifest's keys isn't a change.
func canonical(raw json.RawMessage) string {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return string(raw)
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func diff(before, after []byte) Diff {
	a, b := asks(before), asks(after)
	d := Diff{}
	for _, kind := range kinds {
		var c Change
		for _, s := range b[kind] {
			if !slices.Contains(a[kind], s) && !slices.Contains(c.Added, s) {
				c.Added = append(c.Added, s)
			}
		}
		for _, s := range a[kind] {
			if !slices.Contains(b[kind], s) && !slices.Contains(c.Removed, s) {
				c.Removed = append(c.Removed, s)
			}
		}
		if len(c.Added)+len(c.Removed) > 0 {
			d[kind] = c
		}
	}
	return d
}

// newer reports whether version a is later than b: dot-separated numbers,
// a missing part 0, an optional leading v, a -prerelease before its
// release, +build ignored. What it can't order is not newer: an update
// must say it's later.
func newer(a, b string) bool {
	pa, oka := parseVersion(a)
	pb, okb := parseVersion(b)
	if !oka || !okb {
		return false
	}
	for k := range max(len(pa.nums), len(pb.nums)) {
		x, y := at(pa.nums, k), at(pb.nums, k)
		if x != y {
			return x > y
		}
	}
	switch {
	case pa.pre == pb.pre:
		return false
	case pa.pre == "":
		return true
	case pb.pre == "":
		return false
	}
	return comparePre(pa.pre, pb.pre) > 0
}

// comparePre orders two prereleases by SemVer's rule: identifier by
// identifier, numbers as numbers and below words, words as text, and a
// prerelease that runs out first below the longer.
func comparePre(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for k := range min(len(as), len(bs)) {
		x, xerr := strconv.Atoi(as[k])
		y, yerr := strconv.Atoi(bs[k])
		switch {
		case xerr == nil && yerr == nil:
			if x != y {
				return cmp.Compare(x, y)
			}
		case xerr == nil:
			return -1
		case yerr == nil:
			return 1
		default:
			if c := strings.Compare(as[k], bs[k]); c != 0 {
				return c
			}
		}
	}
	return cmp.Compare(len(as), len(bs))
}

type version struct {
	nums []int
	pre  string
}

func parseVersion(s string) (version, bool) {
	s, _, _ = strings.Cut(strings.TrimPrefix(strings.TrimSpace(s), "v"), "+")
	core, pre, _ := strings.Cut(s, "-")
	var v version
	for part := range strings.SplitSeq(core, ".") {
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 {
			return v, false
		}
		v.nums = append(v.nums, n)
	}
	v.pre = pre
	return v, true
}

func at(nums []int, k int) int {
	if k < len(nums) {
		return nums[k]
	}
	return 0
}

package skills

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"glitched/internal/api"
	"glitched/internal/jsonfile"
)

// syncedDir, in the state folder, holds the skills another machine's GUI
// synced to this engine: current.json names the machine and its set,
// sets/<set> holds the set, and incoming/<tree> the trees it was given
// for a set it hasn't taken yet.
const syncedDir = "skills-synced"

// Origin is the machine whose skills an engine holds, as current.json
// keeps it: its engine Source, named Name, and its set, Set, of Trees by
// skill name, which it last said were its own at Pulled.
type Origin struct {
	Source api.ID            `json:"source"`
	Name   string            `json:"name"`
	Set    string            `json:"set"`
	Trees  map[string]string `json:"trees"`
	Pulled time.Time         `json:"pulled"`
}

func originPath(stateDir string) string { return filepath.Join(stateDir, syncedDir, "current.json") }

// readOrigin is the Origin of the skills synced to the engine in stateDir,
// and false for none.
func readOrigin(stateDir string) (Origin, bool) {
	var o Origin
	if err := jsonfile.Load(originPath(stateDir), &o); err != nil || o.Source.IsZero() || !isHash(o.Set, setHashLen) {
		return Origin{}, false
	}
	return o, true
}

func (o Origin) synced() api.SkillsSynced {
	return api.SkillsSynced{Source: o.Source, Name: o.Name, Set: o.Set, Pulled: o.Pulled}
}

const (
	setHashLen  = 32
	treeHashLen = 64
	// maxSetBytes and maxSetFiles bound a synced set, so its manifest
	// stays well within one message.
	maxSetBytes = 256 << 20
	maxSetFiles = 20000
	// staleAfter is how long a put tree waits for a sync to take it.
	staleAfter = time.Hour
)

// waitingLimit is how many bytes the trees put for syncs to come may hold
// in all: a set's worth.
var waitingLimit = maxSetBytes

func isHash(s string, n int) bool {
	if len(s) != n {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil && strings.ToLower(s) == s
}

// setHash is the hash of a set of skills by name and tree, as Set.Hash.
func setHash(trees []api.SkillTree) string {
	s := Set{}
	for _, t := range trees {
		s.Skills = append(s.Skills, Skill{Name: t.Name, Hash: t.Tree})
	}
	slices.SortFunc(s.Skills, func(a, b Skill) int { return strings.Compare(a.Name, b.Name) })
	return s.Hash()
}

// Export is this machine's own skills, the user's in <stateDir>/skills,
// as its GUI syncs them to the engines it pairs with: never those synced
// to it, so no machine passes on another's.
type Export struct {
	Manifest api.SkillManifest
	skills   map[string]Skill
}

// Exported is the Export of the skills in <stateDir>/skills that pass
// Find's checks.
func Exported(stateDir string) Export {
	f := newFinder(stateDir)
	e := Export{Manifest: api.SkillManifest{Skills: []api.SkillTree{}}, skills: map[string]Skill{}}
	for _, sk := range f.root(Home, filepath.Join(stateDir, "skills"), "") {
		t := api.SkillTree{Name: sk.Name, Tree: sk.Hash, Files: []api.SkillFile{}}
		hash, err := walk(sk.Dir, sk.folder, func(rel string, exec bool, data []byte) error {
			sum := sha256.Sum256(data)
			t.Files = append(t.Files, api.SkillFile{Path: rel, Mode: modeOf(exec), Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])})
			return nil
		})
		if err != nil || hash != sk.Hash {
			continue
		}
		e.Manifest.Skills = append(e.Manifest.Skills, t)
		e.skills[sk.Name] = sk
	}
	e.Manifest.Set = setHash(e.Manifest.Skills)
	return e
}

// Files is the files of the exported skill name, unless it changed since
// it was exported.
func (e Export) Files(name string) ([]api.SkillContent, error) {
	sk, ok := e.skills[name]
	if !ok {
		return nil, fmt.Errorf("skills: %s isn't exported", name)
	}
	var out []api.SkillContent
	hash, err := walk(sk.Dir, sk.folder, func(rel string, exec bool, data []byte) error {
		out = append(out, api.SkillContent{Path: rel, Mode: modeOf(exec), Data: data})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("skills: %s: %s", name, err)
	}
	if hash != sk.Hash {
		return nil, fmt.Errorf("%w: %s", errChanged, name)
	}
	return out, nil
}

// A Peer is an engine a machine syncs its skills to.
type Peer interface {
	SyncSkills(ctx context.Context, q api.SkillSync) (api.SkillsSynced, error)
	PutSkill(ctx context.Context, p api.SkillPut) error
}

// Push syncs the skills of the machine with state in stateDir, whose engine
// is source, named name, to peer: it offers the set, gives the trees peer
// says it lacks, and offers the set again.
func Push(ctx context.Context, stateDir string, source api.ID, name string, peer Peer) (api.SkillsSynced, error) {
	e := Exported(stateDir)
	q := api.SkillSync{Source: source, Name: name, Manifest: e.Manifest}
	got, err := peer.SyncSkills(ctx, q)
	if err != nil || len(got.Missing) == 0 {
		return got, err
	}
	for _, t := range e.Manifest.Skills {
		if !slices.Contains(got.Missing, t.Tree) {
			continue
		}
		files, err := e.Files(t.Name)
		if err != nil {
			return got, err
		}
		if err := peer.PutSkill(ctx, api.SkillPut{Name: t.Name, Tree: t.Tree, Files: files}); err != nil {
			return got, err
		}
	}
	if got, err = peer.SyncSkills(ctx, q); err == nil && len(got.Missing) > 0 {
		err = fmt.Errorf("skills: the engine still lacks %d of the skills", len(got.Missing))
	}
	return got, err
}

// A Store keeps the skills another machine's GUI syncs to this engine, in
// <stateDir>/skills-synced. Everything it is given is checked: names,
// paths, modes, sizes and every hash, and it writes plain files alone.
type Store struct {
	state string
	mu    sync.Mutex
}

func NewStore(stateDir string) *Store { return &Store{state: stateDir} }

func (s *Store) dir(elem ...string) string {
	return filepath.Join(append([]string{s.state, syncedDir}, elem...)...)
}

// Put keeps the files of the skill p.Name for a set to come, once they
// prove to be the tree p.Tree and this machine's filesystem holds them as
// they are. A tree it holds already stays as it is. Trees no sync takes
// within staleAfter go, and all it keeps waiting stay within maxSetBytes.
func (s *Store) Put(p api.SkillPut) error {
	files := make([]entry, len(p.Files))
	var md []byte
	size := 0
	for i, c := range p.Files {
		exec, err := parseMode(c.Mode)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(c.Data)
		files[i] = entry{rel: c.Path, exec: exec, size: len(c.Data), sum: hex.EncodeToString(sum[:])}
		size += len(c.Data)
		if c.Path == "SKILL.md" {
			md = c.Data
		}
	}
	if err := checkTree(p.Name, p.Tree, files); err != nil {
		return err
	}
	fm, err := frontmatter(md)
	if err != nil {
		return fmt.Errorf("skills: %s: %s", p.Name, err)
	}
	if fm.Name != p.Name {
		return fmt.Errorf("skills: %s: its SKILL.md names it %q", p.Name, fm.Name)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.collect(time.Now())
	to := s.dir("incoming", p.Tree)
	if _, err := os.Lstat(to); err == nil {
		now := time.Now()
		return os.Chtimes(to, now, now)
	}
	if s.waiting()+size > waitingLimit {
		return fmt.Errorf("skills: the engine already holds %d MiB of skills no sync has taken yet", waitingLimit>>20)
	}
	if err := os.MkdirAll(s.dir("incoming"), 0o700); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(s.dir("incoming"), ".tmp-")
	if err != nil {
		return err
	}
	if err := writeTree(tmp, p.Files); err != nil {
		os.RemoveAll(tmp)
		return err
	}
	if err := copyFiles(tmp, "", files); err != nil {
		os.RemoveAll(tmp)
		return fmt.Errorf("skills: %s: this machine's filesystem doesn't hold it as sent, as where two paths differ only in case: %w", p.Name, err)
	}
	return os.Rename(tmp, to)
}

// writeTree writes files, whose paths checkTree passed, in dir, read-only.
func writeTree(dir string, files []api.SkillContent) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	for _, c := range files {
		p := filepath.FromSlash(c.Path)
		if d := filepath.Dir(p); d != "." {
			if err := root.MkdirAll(d, 0o755); err != nil {
				return err
			}
		}
		if err := root.WriteFile(p, c.Data, fileMode(c.Mode == "755")); err != nil {
			return err
		}
	}
	return nil
}

func fileMode(exec bool) os.FileMode {
	if exec {
		return 0o555
	}
	return 0o444
}

// copyFiles copies the skill in dir to a new folder to, read-only, or
// only checks it where to is "", unless its files aren't files by path,
// size and sha256. Their modes are files', not the folder's, as a
// filesystem such as Windows' keeps no mode to run a file.
func copyFiles(dir, to string, files []entry) error {
	folder, err := os.Stat(dir)
	if err != nil {
		return err
	}
	want := map[string]entry{}
	for _, f := range files {
		want[f.rel] = f
	}
	seen := 0
	_, err = walk(dir, folder, func(rel string, _ bool, data []byte) error {
		f, ok := want[rel]
		sum := sha256.Sum256(data)
		if !ok || f.size != len(data) || f.sum != hex.EncodeToString(sum[:]) {
			return fmt.Errorf("%s isn't the file it was sent as", rel)
		}
		seen++
		if to == "" {
			return nil
		}
		p := filepath.Join(to, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		return os.WriteFile(p, data, fileMode(f.exec))
	})
	if err == nil && seen != len(files) {
		err = fmt.Errorf("it holds %d of the %d files it was sent with", seen, len(files))
	}
	return err
}

// collect deletes what no sync will take: a temporary folder a crash left,
// as none is in use while s.mu is held, and a tree put before now less
// staleAfter. s.mu must be held.
func (s *Store) collect(now time.Time) {
	for _, d := range []string{"sets", "incoming"} {
		entries, _ := os.ReadDir(s.dir(d))
		for _, e := range entries {
			info, err := e.Info()
			if strings.HasPrefix(e.Name(), ".tmp-") || d == "incoming" && (err != nil || now.Sub(info.ModTime()) > staleAfter) {
				os.RemoveAll(s.dir(d, e.Name()))
			}
		}
	}
}

// waiting is how many bytes the trees put for a sync to come hold. s.mu
// must be held.
func (s *Store) waiting() int {
	n := 0
	filepath.WalkDir(s.dir("incoming"), func(_ string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				n += int(info.Size())
			}
		}
		return nil
	})
	return n
}

// Sync takes q's skills as the ones synced from q.Source's machine, the
// last set it took kept for a run that read it. While trees of q are
// missing, ones neither that set nor a Put holds, it names them and
// changes nothing. The set it holds already only marks it pulled at now.
func (s *Store) Sync(q api.SkillSync, now time.Time) (api.SkillsSynced, error) {
	if q.Source.IsZero() {
		return api.SkillsSynced{}, errors.New("skills: a sync names no machine")
	}
	trees, err := checkManifest(q.Manifest)
	if err != nil {
		return api.SkillsSynced{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.collect(time.Now())
	cur, held := readOrigin(s.state)
	if _, err := os.Lstat(s.dir("sets", cur.Set)); err != nil {
		held = false
	}
	if held && cur.Set == q.Manifest.Set {
		cur.Source, cur.Name, cur.Pulled = q.Source, q.Name, now
		return cur.synced(), jsonfile.Write(originPath(s.state), cur)
	}
	from := map[string]string{}
	var missing []string
	for _, t := range q.Manifest.Skills {
		in := s.dir("incoming", t.Tree)
		switch _, err := os.Lstat(in); {
		case held && cur.Trees[t.Name] == t.Tree:
			from[t.Name] = s.dir("sets", cur.Set, t.Name)
		case err == nil:
			from[t.Name] = in
		default:
			missing = append(missing, t.Tree)
		}
	}
	if len(missing) > 0 {
		out := cur.synced()
		out.Missing = missing
		return out, nil
	}
	if err := os.MkdirAll(s.dir("sets"), 0o700); err != nil {
		return api.SkillsSynced{}, err
	}
	tmp, err := os.MkdirTemp(s.dir("sets"), ".tmp-")
	if err != nil {
		return api.SkillsSynced{}, err
	}
	next := Origin{Source: q.Source, Name: q.Name, Set: q.Manifest.Set, Trees: map[string]string{}, Pulled: now}
	for i, t := range q.Manifest.Skills {
		if err := copyFiles(from[t.Name], filepath.Join(tmp, t.Name), trees[i]); err != nil {
			os.RemoveAll(tmp)
			return api.SkillsSynced{}, fmt.Errorf("skills: %s: %w", t.Name, err)
		}
		next.Trees[t.Name] = t.Tree
	}
	if err := os.Rename(tmp, s.dir("sets", next.Set)); err != nil {
		os.RemoveAll(tmp)
		if _, statErr := os.Lstat(s.dir("sets", next.Set)); statErr != nil {
			return api.SkillsSynced{}, err
		}
	}
	if err := jsonfile.Write(originPath(s.state), next); err != nil {
		return api.SkillsSynced{}, err
	}
	sets, _ := os.ReadDir(s.dir("sets"))
	for _, e := range sets {
		if e.Name() != next.Set && (!held || e.Name() != cur.Set) {
			os.RemoveAll(s.dir("sets", e.Name()))
		}
	}
	for _, t := range q.Manifest.Skills {
		os.RemoveAll(s.dir("incoming", t.Tree))
	}
	return next.synced(), nil
}

// Drop deletes the skills synced from source's machine, and keeps another
// machine's.
func (s *Store) Drop(source api.ID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cur, ok := readOrigin(s.state); !ok || cur.Source != source {
		return nil
	}
	return os.RemoveAll(s.dir())
}

func parseMode(m string) (bool, error) {
	switch m {
	case "644":
		return false, nil
	case "755":
		return true, nil
	}
	return false, fmt.Errorf("skills: a file of mode %q, and a synced skill holds only files", m)
}

// checkManifest is whether m is a set of skills that holds to the limits
// and whose hashes are its own, and each skill's files.
func checkManifest(m api.SkillManifest) ([][]entry, error) {
	if len(m.Skills) > maxSkills {
		return nil, fmt.Errorf("skills: a set of %d skills, past %d", len(m.Skills), maxSkills)
	}
	names := map[string]bool{}
	trees := make([][]entry, len(m.Skills))
	count, size := 0, 0
	for i, t := range m.Skills {
		if names[t.Name] {
			return nil, fmt.Errorf("skills: %s comes twice", t.Name)
		}
		names[t.Name] = true
		files := make([]entry, len(t.Files))
		for j, f := range t.Files {
			exec, err := parseMode(f.Mode)
			if err != nil {
				return nil, err
			}
			if f.Size < 0 || f.Size > math.MaxInt32 || !isHash(f.SHA256, treeHashLen) {
				return nil, fmt.Errorf("skills: %s: %s has no size or sha256 a file can have", t.Name, f.Path)
			}
			files[j] = entry{rel: f.Path, exec: exec, size: int(f.Size), sum: f.SHA256}
			size += int(f.Size)
		}
		if err := checkTree(t.Name, t.Tree, files); err != nil {
			return nil, err
		}
		if count += len(files); count > maxSetFiles || size > maxSetBytes {
			return nil, fmt.Errorf("skills: the set holds more than %d files or %d MiB in all", maxSetFiles, maxSetBytes>>20)
		}
		trees[i] = files
	}
	if !isHash(m.Set, setHashLen) || setHash(m.Skills) != m.Set {
		return nil, errors.New("skills: the set's skills don't hash to its set")
	}
	return trees, nil
}

// checkTree is whether files can be the tree of the skill name in a
// folder of its own, and hash to tree.
func checkTree(name, tree string, files []entry) error {
	if !validName(name) {
		return fmt.Errorf("skills: %q isn't a skill name", name)
	}
	if !isHash(tree, treeHashLen) {
		return fmt.Errorf("skills: %s: its tree isn't a hash", name)
	}
	if len(files) > maxFiles {
		return fmt.Errorf("skills: %s holds more than %d files", name, maxFiles)
	}
	paths := map[string]bool{}
	total := 0
	for _, f := range files {
		if !validPath(f.rel) {
			return fmt.Errorf("skills: %s: %q isn't a path in a skill's folder", name, f.rel)
		}
		if paths[f.rel] {
			return fmt.Errorf("skills: %s: %s comes twice", name, f.rel)
		}
		paths[f.rel] = true
		if total += f.size; total > maxBytes {
			return fmt.Errorf("skills: %s holds more than %d MiB", name, maxBytes>>20)
		}
	}
	if !paths["SKILL.md"] {
		return fmt.Errorf("skills: %s has no SKILL.md", name)
	}
	if treeHash(files) != tree {
		return fmt.Errorf("skills: %s: its files don't hash to its tree", name)
	}
	return nil
}

// validPath is whether p, slash-separated, names a file inside a skill's
// folder at most maxDepth folders down.
func validPath(p string) bool {
	if p == "" || !utf8.ValidString(p) || strings.ContainsAny(p, "\\\x00") || path.IsAbs(p) || path.Clean(p) != p {
		return false
	}
	parts := strings.Split(p, "/")
	return len(parts) <= maxDepth+1 && !slices.ContainsFunc(parts, func(s string) bool { return s == "" || s == "." || s == ".." })
}

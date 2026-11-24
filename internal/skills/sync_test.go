package skills

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/skills/skillstest"
)

// storePeer is a Store as a GUI reaches the engine that keeps it, counting
// the trees it is given.
type storePeer struct {
	s    *Store
	now  time.Time
	puts *[]string
}

func (p storePeer) SyncSkills(_ context.Context, q api.SkillSync) (api.SkillsSynced, error) {
	return p.s.Sync(q, p.now)
}

func (p storePeer) PutSkill(_ context.Context, q api.SkillPut) error {
	*p.puts = append(*p.puts, q.Name)
	return p.s.Put(q)
}

func push(t *testing.T, from, to string, source api.ID, name string, now time.Time) []string {
	t.Helper()
	var puts []string
	if _, err := Push(context.Background(), from, source, name, storePeer{NewStore(to), now, &puts}); err != nil {
		t.Fatal(err)
	}
	return puts
}

func TestPushSyncsOnlyWhatChanged(t *testing.T) {
	src, _ := folders(t)
	node, _ := folders(t)
	source := api.NewID()
	skill(t, filepath.Join(src, "skills"), "deploy", "Ship it.", "run.sh", "echo hi")
	skill(t, filepath.Join(src, "skills"), "review", "Review it.")
	os.Chmod(filepath.Join(src, "skills", "deploy", "run.sh"), 0o755)
	at := time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC)
	if puts := push(t, src, node, source, "Praetor", at); !slices.Equal(puts, []string{"deploy", "review"}) {
		t.Errorf("the first sync put %v", puts)
	}
	set := Find(node, "", "")
	if got := names(set.Skills); !slices.Equal(got, []string{"synced:deploy", "synced:review"}) {
		t.Fatalf("the node finds %v", got)
	}
	if set.Synced == nil || set.Synced.Name != "Praetor" || !set.Synced.Pulled.Equal(at) {
		t.Errorf("the node's set came from %+v", set.Synced)
	}
	run, err := os.Stat(filepath.Join(set.Skills[0].Dir, "run.sh"))
	if err != nil || run.Mode().Perm()&0o100 == 0 {
		t.Errorf("deploy's run.sh lost its mode: %v, %v", run, err)
	}
	if puts := push(t, src, node, source, "Praetor", at.Add(time.Hour)); len(puts) != 0 {
		t.Errorf("a sync of the same set put %v", puts)
	}
	if set := Find(node, "", ""); !set.Synced.Pulled.Equal(at.Add(time.Hour)) {
		t.Errorf("the same set's sync left it pulled at %v", set.Synced.Pulled)
	}
	write(t, filepath.Join(src, "skills", "review", "SKILL.md"), "---\nname: review\ndescription: Review it better.\n---\n")
	if puts := push(t, src, node, source, "Praetor", at.Add(2*time.Hour)); !slices.Equal(puts, []string{"review"}) {
		t.Errorf("a sync after review changed put %v", puts)
	}
	set = Find(node, "", "")
	if i := slices.IndexFunc(set.Skills, func(s Skill) bool { return s.Name == "review" }); i < 0 || set.Skills[i].Description != "Review it better." {
		t.Errorf("the node has review as %+v", set.Skills)
	}
	sets, _ := os.ReadDir(filepath.Join(node, syncedDir, "sets"))
	if len(sets) != 2 {
		t.Errorf("the node keeps %d sets, want the current one and the one before", len(sets))
	}
}

func TestFindMergesProjectSyncedAndHome(t *testing.T) {
	src, _ := folders(t)
	node, ws := folders(t)
	skill(t, filepath.Join(src, "skills"), "review", "The source's review.")
	skill(t, filepath.Join(src, "skills"), "deploy", "The source's deploy.")
	skill(t, filepath.Join(node, "skills"), "review", "The node's review.")
	skill(t, filepath.Join(node, "skills"), "notes", "The node's notes.")
	skill(t, filepath.Join(ws, ".glitched", "skills"), "deploy", "The project's deploy.")
	push(t, src, node, api.NewID(), "Praetor", time.Now())
	set := Find(node, ws, "")
	got := map[string]Skill{}
	for _, sk := range set.Skills {
		got[sk.Name] = sk
	}
	for name, want := range map[string]struct {
		src, replaces Source
		desc          string
	}{
		"review": {Synced, Home, "The source's review."},
		"deploy": {Project, Synced, "The project's deploy."},
		"notes":  {Home, "", "The node's notes."},
	} {
		if sk := got[name]; sk.Source != want.src || sk.Replaces != want.replaces || sk.Description != want.desc {
			t.Errorf("%s is %s %q replacing %q, want %s %q replacing %q", name, sk.Source, sk.Description, sk.Replaces, want.src, want.desc, want.replaces)
		}
	}
	if got := names(set.Shadowed); !slices.Equal(sorted(got), []string{"home:review", "synced:deploy"}) {
		t.Errorf("shadowed %v", got)
	}
}

func sorted(s []string) []string { slices.Sort(s); return s }

// A machine passes on only its own skills, never those synced to it.
func TestExportedIsThisMachinesOwn(t *testing.T) {
	src, _ := folders(t)
	node, _ := folders(t)
	skill(t, filepath.Join(src, "skills"), "theirs", "The source's.")
	skill(t, filepath.Join(node, "skills"), "mine", "The node's.")
	push(t, src, node, api.NewID(), "Praetor", time.Now())
	var got []string
	for _, s := range Exported(node).Manifest.Skills {
		got = append(got, s.Name)
	}
	if !slices.Equal(got, []string{"mine"}) {
		t.Errorf("the node exports %v", got)
	}
}

func TestDropKeepsAnotherMachinesSet(t *testing.T) {
	src, _ := folders(t)
	node, _ := folders(t)
	skill(t, filepath.Join(src, "skills"), "theirs", "The source's.")
	source := api.NewID()
	push(t, src, node, source, "Praetor", time.Now())
	s := NewStore(node)
	if err := s.Drop(api.NewID()); err != nil || Find(node, "", "").Synced == nil {
		t.Fatalf("another machine's drop took the set: %v", err)
	}
	if err := s.Drop(source); err != nil {
		t.Fatal(err)
	}
	if set := Find(node, "", ""); set.Synced != nil || len(set.Skills) != 0 {
		t.Errorf("after its machine's drop the node finds %v from %+v", names(set.Skills), set.Synced)
	}
	if _, err := os.Lstat(filepath.Join(node, syncedDir)); !os.IsNotExist(err) {
		t.Errorf("the synced folder stays: %v", err)
	}
}

// put is the SkillPut of the skill name with files, path then content,
// beside a SKILL.md that names it, with the tree they hash to.
func put(name string, files ...string) api.SkillPut {
	p := api.SkillPut{Name: name, Files: []api.SkillContent{{Path: "SKILL.md", Mode: "644", Data: []byte("---\nname: " + name + "\ndescription: d\n---\n")}}}
	for i := 0; i+1 < len(files); i += 2 {
		p.Files = append(p.Files, api.SkillContent{Path: files[i], Mode: "644", Data: []byte(files[i+1])})
	}
	p.Tree = treeOf(p.Files)
	return p
}

func treeOf(files []api.SkillContent) string {
	var es []entry
	for _, f := range files {
		sum := sha256.Sum256(f.Data)
		es = append(es, entry{rel: f.Path, exec: f.Mode == "755", size: len(f.Data), sum: hex.EncodeToString(sum[:])})
	}
	return treeHash(es)
}

func manifest(puts ...api.SkillPut) api.SkillManifest {
	m := api.SkillManifest{Skills: []api.SkillTree{}}
	for _, p := range puts {
		t := api.SkillTree{Name: p.Name, Tree: p.Tree}
		for _, f := range p.Files {
			sum := sha256.Sum256(f.Data)
			t.Files = append(t.Files, api.SkillFile{Path: f.Path, Mode: f.Mode, Size: int64(len(f.Data)), SHA256: hex.EncodeToString(sum[:])})
		}
		m.Skills = append(m.Skills, t)
	}
	m.Set = setHash(m.Skills)
	return m
}

// Every offer that isn't a skill's own folder of plain files, with its own
// hashes, is refused, and leaves what the engine holds as it was.
func TestStoreRefusesHostileOffers(t *testing.T) {
	node, _ := folders(t)
	s := NewStore(node)
	good := put("good", "a.md", "ok")
	source := api.NewID()
	if err := s.Put(good); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Sync(api.SkillSync{Source: source, Name: "Praetor", Manifest: manifest(good)}, time.Now()); err != nil {
		t.Fatal(err)
	}
	before := skillstest.Fingerprint(t, filepath.Join(node, syncedDir, "sets"), originPath(node))

	badHash := put("bad-hash", "a.md", "ok")
	badHash.Files[1].Data = []byte("swapped")
	hidden := put("link", "a.md", "ok")
	hidden.Files[1].Mode = "120000"
	hidden.Tree = treeOf(hidden.Files)
	named := put("named")
	named.Files[0].Data = []byte("---\nname: other\ndescription: d\n---\n")
	named.Tree = treeOf(named.Files)
	puts := map[string]api.SkillPut{
		"a file that doesn't hash to its tree": badHash,
		"a path with ..":                       put("dots", "../escape.md", "x"),
		"an absolute path":                     put("abs", "/etc/x", "x"),
		"an empty path segment":                put("empty", "a//b", "x"),
		"a backslash":                          put("slash", "a\\..\\b", "x"),
		"a link's mode":                        hidden,
		"a file of 9 MiB":                      put("big", "big.bin", strings.Repeat("x", 9<<20)),
		"a name that fails the rule":           put("Bad--x"),
		"a SKILL.md naming another":            named,
		"a tree that isn't a hash":             {Name: "good", Tree: "../../x", Files: good.Files},
	}
	for why, p := range puts {
		if err := s.Put(p); err == nil {
			t.Errorf("a put of %s was taken", why)
		}
	}
	setOf := func(m api.SkillManifest) api.SkillManifest { m.Set = setHash(m.Skills); return m }
	wrongSet := manifest(good)
	wrongSet.Set = strings.Repeat("0", setHashLen)
	wrongTree := manifest(good)
	wrongTree.Skills[0].Files[0].Size++
	dots := manifest(good)
	dots.Skills[0].Files = append(dots.Skills[0].Files, api.SkillFile{Path: "../x", Mode: "644", SHA256: strings.Repeat("0", 64)})
	link := manifest(good)
	link.Skills[0].Files[0].Mode = "120000"
	many := manifest(good)
	for i := range 1001 {
		many.Skills[0].Files = append(many.Skills[0].Files, api.SkillFile{Path: "f" + string(rune('a'+i%26)) + strings.Repeat("x", i/26), Mode: "644", SHA256: strings.Repeat("0", 64)})
	}
	bad := manifest(good)
	bad.Skills[0].Name = "Bad--x"
	twice := manifest(good, good)
	manifests := map[string]api.SkillManifest{
		"a set that isn't its skills' hash": wrongSet,
		"a tree that isn't its files' hash": setOf(wrongTree),
		"a path with ..":                    setOf(dots),
		"a link's mode":                     setOf(link),
		"more than 1000 files":              setOf(many),
		"a name that fails the rule":        setOf(bad),
		"a skill twice":                     setOf(twice),
	}
	for why, m := range manifests {
		if _, err := s.Sync(api.SkillSync{Source: source, Name: "Praetor", Manifest: m}, time.Now()); err == nil {
			t.Errorf("a set with %s was taken", why)
		}
	}
	if _, err := s.Sync(api.SkillSync{Name: "Praetor", Manifest: manifest(good)}, time.Now()); err == nil {
		t.Error("a set from no machine was taken")
	}
	if after := skillstest.Fingerprint(t, filepath.Join(node, syncedDir, "sets"), originPath(node)); after != before {
		t.Errorf("a refused offer changed the synced skills:\n%s\nwas\n%s", after, before)
	}
	if _, err := os.Lstat(filepath.Join(node, "escape.md")); err == nil {
		t.Error("a put wrote outside its folder")
	}
	if got := names(Find(node, "", "").Skills); !slices.Equal(got, []string{"synced:good"}) {
		t.Errorf("the node finds %v", got)
	}
}

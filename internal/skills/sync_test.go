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

func (p storePeer) SyncSkills(ctx context.Context, q api.SkillSync) (api.SkillsSynced, error) {
	return p.s.Sync(ctx, q, p.now)
}

func (p storePeer) PutSkill(ctx context.Context, q api.SkillPut) error {
	*p.puts = append(*p.puts, q.Name)
	return p.s.Put(ctx, q)
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
	if len(sets) != 1 {
		t.Errorf("the node keeps %d sets, want only the current one, as no Find reads the one before", len(sets))
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
	e, err := Exported(node)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, s := range e.Manifest.Skills {
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
	if err := s.Drop(t.Context(), api.NewID()); err != nil || Find(node, "", "").Synced == nil {
		t.Fatalf("another machine's drop took the set: %v", err)
	}
	if err := s.Drop(t.Context(), source); err != nil {
		t.Fatal(err)
	}
	if set := Find(node, "", ""); set.Synced != nil || len(set.Skills) != 0 {
		t.Errorf("after its machine's drop the node finds %v from %+v", names(set.Skills), set.Synced)
	}
	if sets, _ := os.ReadDir(filepath.Join(node, syncedDir, "sets")); len(sets) != 0 {
		t.Errorf("the synced sets stay: %v", sets)
	}
	if _, err := os.Lstat(originPath(node)); !os.IsNotExist(err) {
		t.Errorf("current.json stays: %v", err)
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
	if err := s.Put(t.Context(), good); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Sync(t.Context(), api.SkillSync{Source: source, Name: "Praetor", Manifest: manifest(good)}, time.Now()); err != nil {
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
		if err := s.Put(t.Context(), p); err == nil {
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
		if _, err := s.Sync(t.Context(), api.SkillSync{Source: source, Name: "Praetor", Manifest: m}, time.Now()); err == nil {
			t.Errorf("a set with %s was taken", why)
		}
	}
	if _, err := s.Sync(t.Context(), api.SkillSync{Name: "Praetor", Manifest: manifest(good)}, time.Now()); err == nil {
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

func offer(source api.ID, puts ...api.SkillPut) api.SkillSync {
	return api.SkillSync{Source: source, Name: "Praetor", Manifest: manifest(puts...)}
}

// Two apps syncing at once each keep the trees they put till their own
// sync takes them.
func TestTwoSyncsKeepEachOthersPuts(t *testing.T) {
	node, _ := folders(t)
	s := NewStore(node)
	a, b := put("one", "a.md", "a"), put("two", "b.md", "b")
	for _, p := range []api.SkillPut{a, b} {
		if err := s.Put(t.Context(), p); err != nil {
			t.Fatal(err)
		}
	}
	for _, q := range []api.SkillSync{offer(api.NewID(), a), offer(api.NewID(), b)} {
		got, err := s.Sync(t.Context(), q, time.Now())
		if err != nil || len(got.Missing) != 0 {
			t.Fatalf("a sync whose tree was put answered %+v, %v", got, err)
		}
	}
}

// What a crash left, and trees no sync took within staleAfter, go at the
// next sync, the same set's too.
func TestSyncCollectsWhatNoSyncTakes(t *testing.T) {
	node, _ := folders(t)
	s := NewStore(node)
	p := put("one", "a.md", "a")
	if err := s.Put(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	q := offer(api.NewID(), p)
	if _, err := s.Sync(t.Context(), q, time.Now()); err != nil {
		t.Fatal(err)
	}
	stale := put("old", "b.md", "b")
	if err := s.Put(t.Context(), stale); err != nil {
		t.Fatal(err)
	}
	long := time.Now().Add(-2 * staleAfter)
	os.Chtimes(s.dir("incoming", stale.Tree), long, long)
	for _, d := range []string{s.dir("sets", ".tmp-1"), s.dir("incoming", ".tmp-2")} {
		write(t, filepath.Join(d, "x"), "x")
	}
	if _, err := s.Sync(t.Context(), q, time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{s.dir("sets", ".tmp-1"), s.dir("incoming", ".tmp-2"), s.dir("incoming", stale.Tree)} {
		if _, err := os.Lstat(d); !os.IsNotExist(err) {
			t.Errorf("%s stays: %v", d, err)
		}
	}
}

func TestPutKeepsWaitingTreesWithinALimit(t *testing.T) {
	node, _ := folders(t)
	s := NewStore(node)
	was := waitingLimit
	waitingLimit = 1 << 20
	t.Cleanup(func() { waitingLimit = was })
	if err := s.Put(t.Context(), put("one", "a.bin", strings.Repeat("a", 700<<10))); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(t.Context(), put("two", "b.bin", strings.Repeat("b", 700<<10))); err == nil || !strings.Contains(err.Error(), "no sync has taken yet") {
		t.Errorf("a put past the limit was answered %v", err)
	}
}

// A tree this machine's filesystem can't hold as sent, as two names that
// differ only in case on one that ignores case, is refused at its put; one
// that holds it takes it.
func TestPutRefusesATreeTheFilesystemFolds(t *testing.T) {
	node, _ := folders(t)
	write(t, filepath.Join(node, "Probe"), "x")
	_, err := os.Stat(filepath.Join(node, "probe"))
	folds := err == nil
	s := NewStore(node)
	p := put("cased", "README.md", "upper", "readme.md", "lower")
	if err := s.Put(t.Context(), p); (err != nil) != folds {
		t.Fatalf("a put of README.md and readme.md where the filesystem folds case is %v answered %v", folds, err)
	}
	if !folds {
		if got, err := s.Sync(t.Context(), offer(api.NewID(), p), time.Now()); err != nil || len(got.Missing) != 0 {
			t.Errorf("its sync answered %+v, %v", got, err)
		}
	}
}

// A file's mode comes from what was sent, not from the filesystem the tree
// waited on, which on Windows keeps none.
func TestSyncTakesModesFromTheManifest(t *testing.T) {
	node, _ := folders(t)
	s := NewStore(node)
	p := put("runs", "run.sh", "echo hi")
	p.Files[1].Mode = "755"
	p.Tree = treeOf(p.Files)
	if err := s.Put(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	staged := s.dir("incoming", p.Tree, "run.sh")
	if err := os.Chmod(staged, 0o444); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Sync(t.Context(), offer(api.NewID(), p), time.Now()); err != nil || len(got.Missing) != 0 {
		t.Fatalf("the sync answered %+v, %v", got, err)
	}
	set := Find(node, "", "")
	fi, err := os.Stat(filepath.Join(set.Skills[0].Dir, "run.sh"))
	if err != nil || fi.Mode().Perm()&0o100 == 0 {
		t.Errorf("run.sh is %v, %v; want it to run", fi, err)
	}
}

// A run that found a synced set reads it whole as it builds, though syncs
// or a drop replace it in between.
func TestARunKeepsTheSetItFoundTillItIsBuilt(t *testing.T) {
	for _, reason := range []string{"two-commits", "drop"} {
		t.Run(reason, func(t *testing.T) {
			node, _ := folders(t)
			store := NewStore(node)
			source := api.NewID()
			commit := func(p api.SkillPut) {
				t.Helper()
				if err := store.Put(t.Context(), p); err != nil {
					t.Fatal(err)
				}
				if r, err := store.Sync(t.Context(), offer(source, p), time.Now()); err != nil || len(r.Missing) != 0 {
					t.Fatalf("commit: %+v %v", r, err)
				}
			}
			commit(put("one"))
			snapshot, done := store.Find("", "")
			defer done()
			if len(snapshot.Skills) != 1 {
				t.Fatalf("initial Find: %+v", snapshot)
			}
			otherOverlay, err := Build(t.TempDir(), snapshot)
			if err != nil {
				t.Fatal(err)
			}
			if reason == "drop" {
				if err := store.Drop(t.Context(), source); err != nil {
					t.Fatal(err)
				}
			} else {
				commit(put("two"))
				commit(put("three"))
			}
			if _, err := os.ReadFile(SkillFile(otherOverlay, "one")); err != nil {
				t.Fatalf("already built overlay lost: %v", err)
			}
			if _, err := Build(node, snapshot); err != nil {
				t.Fatalf("the set returned by Find was reclaimed before Build: %v", err)
			}
			done()
			if sets, _ := os.ReadDir(store.dir("sets")); reason == "drop" && len(sets) != 0 || reason != "drop" && len(sets) != 1 {
				t.Errorf("once its read was done, the sets left are %v", sets)
			}
		})
	}
}

// Two machines whose offers share a tree: one's sync taking it leaves it
// for the other's, which needs no second put.
func TestAnOfferKeepsATreeAnotherMachinesSyncTook(t *testing.T) {
	node, _ := folders(t)
	store := NewStore(node)
	shared, a, b := put("shared"), put("first"), put("second")
	qa, qb, qc := offer(api.NewID(), shared, a), offer(api.NewID(), shared, b), offer(api.NewID(), put("third"))
	sync := func(q api.SkillSync) api.SkillsSynced {
		t.Helper()
		r, err := store.Sync(t.Context(), q, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	qc.Source = qb.Source
	if len(sync(qa).Missing) != 2 || len(sync(qb).Missing) != 2 {
		t.Fatal("offers should both start missing")
	}
	for _, p := range []api.SkillPut{shared, a, b} {
		if err := store.Put(t.Context(), p); err != nil {
			t.Fatal(err)
		}
	}
	if r := sync(qb); len(r.Missing) != 0 {
		t.Fatalf("second app: %+v", r)
	}
	if err := store.Put(t.Context(), put("third")); err != nil {
		t.Fatal(err)
	}
	if r := sync(qc); len(r.Missing) != 0 {
		t.Fatalf("second app after update: %+v", r)
	}
	if r := sync(qa); slices.Contains(r.Missing, shared.Tree) {
		t.Fatalf("first app successfully staged the common tree, but final sync asked for it again and left source %s current", r.Source)
	}
}

// A set a publish left in place without current.json naming it, as a crash
// between the two leaves, goes at the next sync, one of the set held
// already included, after a restart.
func TestASyncCollectsASetAPublishLeftBehind(t *testing.T) {
	node, _ := folders(t)
	store := NewStore(node)
	a, b := put("one"), put("two")
	source := api.NewID()
	qa, qb := offer(source, a), offer(source, b)
	if err := store.Put(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Sync(t.Context(), qa, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := store.Put(t.Context(), b); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(originPath(node), originPath(node)+".save"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(originPath(node), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Sync(t.Context(), qb, time.Now()); err == nil {
		t.Fatal("expected publish failure")
	}
	if err := os.Remove(originPath(node)); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(originPath(node)+".save", originPath(node)); err != nil {
		t.Fatal(err)
	}
	orphan := store.dir("sets", qb.Manifest.Set)
	if _, err := os.Stat(orphan); err != nil {
		t.Fatalf("expected directory to have been committed before pointer: %v", err)
	}
	write(t, originPath(node)+".123", "{}")
	if got, err := NewStore(node).Sync(t.Context(), qa, time.Now()); err != nil || len(got.Missing) != 0 {
		t.Fatalf("the unchanged sync answered %+v, %v", got, err)
	}
	if _, err := os.Stat(orphan); err == nil {
		t.Error("old final-name generation left by failed pointer publish escaped cleanup after restart and an unchanged sync")
	}
	if _, err := os.Stat(originPath(node) + ".123"); err == nil {
		t.Error("a current.json a write left half done stays")
	}
}

// Only skills in the folder this machine exports go: one linked in from
// elsewhere stays this machine's.
func TestExportSendsNoSkillLinkedInFromOutside(t *testing.T) {
	state, work := folders(t)
	skill(t, work, "outside", "Synthetic skill outside state.")
	skill(t, filepath.Join(state, "skills"), "inside", "Synthetic skill inside state.")
	link(t, filepath.Join(work, "outside"), filepath.Join(state, "skills", "outside"))
	e, err := Exported(state)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, entry := range e.Manifest.Skills {
		got = append(got, entry.Name)
	}
	if !slices.Equal(got, []string{"inside"}) {
		t.Errorf("the export holds %v", got)
	}
	if local := names(Find(state, "", "").Skills); !slices.Equal(local, []string{"home:inside", "home:outside"}) {
		t.Errorf("this machine's own agents get %v", local)
	}
}

// A skills folder that can't be read fails the push, and the node keeps
// the set it held; one that isn't there clears it.
func TestAnUnreadableSkillsFolderKeepsTheNodesSet(t *testing.T) {
	src, _ := folders(t)
	node, _ := folders(t)
	source := api.NewID()
	home := filepath.Join(src, "skills")
	skill(t, home, "one", "Present before I/O error.")
	push(t, src, node, source, "Source", time.Now())
	if err := os.Rename(home, home+".save"); err != nil {
		t.Fatal(err)
	}
	write(t, home, "regular file simulating a failed directory read")
	var puts []string
	if _, err := Push(t.Context(), src, source, "Source", storePeer{NewStore(node), time.Now(), &puts}); err == nil {
		t.Error("a push of a skills folder that couldn't be read succeeded")
	}
	if got := names(Find(node, "", "").Skills); len(got) != 1 {
		t.Fatalf("the push cleared the node's skills when the source root couldn't be read: %v", got)
	}
	if err := os.Remove(home); err != nil {
		t.Fatal(err)
	}
	push(t, src, node, source, "Source", time.Now())
	if got := names(Find(node, "", "").Skills); len(got) != 0 {
		t.Errorf("a push with no skills folder left the node %v", got)
	}
}

// A drop supersedes a sync of the same machine that arrived before it,
// though that sync reaches the store after: it takes nothing. A sync whose
// call ended takes nothing either; one that arrived after the drop takes.
func TestADropSupersedesASyncThatArrivedBeforeIt(t *testing.T) {
	node, _ := folders(t)
	store := NewStore(node)
	source := api.NewID()
	p := put("one")
	if err := store.Put(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	at := func(n uint64) context.Context { return api.WithArrival(t.Context(), n) }
	if err := store.Drop(at(2), source); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Sync(at(1), offer(source, p), time.Now()); err == nil || Find(node, "", "").Synced != nil {
		t.Fatalf("a sync that arrived before the drop answered %v and left %+v", err, Find(node, "", "").Synced)
	}
	ended, cancel := context.WithCancel(at(3))
	cancel()
	if _, err := store.Sync(ended, offer(source, p), time.Now()); err == nil || Find(node, "", "").Synced != nil {
		t.Fatalf("a sync whose call ended answered %v and left %+v", err, Find(node, "", "").Synced)
	}
	if got, err := store.Sync(at(4), offer(source, p), time.Now()); err != nil || len(got.Missing) != 0 || Find(node, "", "").Synced == nil {
		t.Fatalf("a sync that arrived after the drop answered %+v, %v", got, err)
	}
}

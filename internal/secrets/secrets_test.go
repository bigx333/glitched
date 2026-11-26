package secrets

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"glitched/internal/api"
)

func TestPutWritesOwnerOnlyFilesAndAQuotedScript(t *testing.T) {
	v := New(t.TempDir())
	th := api.NewID()
	odd := "a'b\"c$d `e` \x01"
	long := strings.Repeat("k", api.MaxSecret)
	if err := v.Put(th, map[string]string{"ODD": odd, "LONG": long}); err != nil {
		t.Fatal(err)
	}
	dir := v.Dir(th)
	for _, name := range []string{"", "ODD", "LONG", EnvScript, ClaudeEnv} {
		fi, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		want := os.FileMode(0o600)
		if name == "" {
			want = 0o700 | os.ModeDir
		}
		if fi.Mode() != want {
			t.Errorf("%q has mode %v, want %v", name, fi.Mode(), want)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "ODD")); string(b) != odd {
		t.Errorf("ODD holds %q", b)
	}
	if got := New(v.dir).Values(th); got["ODD"] != odd || got["LONG"] != long || len(got) != 2 {
		t.Errorf("a new vault reads %d values back, ODD %q", len(got), got["ODD"])
	}
}

func TestPutReplacesAndForgetDeletes(t *testing.T) {
	v := New(t.TempDir())
	th := api.NewID()
	v.Put(th, map[string]string{"A": "first-value", "B": "kept-value"})
	v.Put(th, map[string]string{"A": "second-value"})
	env, _ := os.ReadFile(filepath.Join(v.Dir(th), EnvScript))
	if string(env) != "export A='second-value'\nexport B='kept-value'\n" {
		t.Errorf("env is %q", env)
	}
	if err := v.Forget(th); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(v.Dir(th)); !os.IsNotExist(err) {
		t.Errorf("the folder is still there: %v", err)
	}
	if len(v.Values(th)) != 0 {
		t.Error("a forgotten thread still has values")
	}
	// RedactValues retains previously held (rotated and forgotten) values
	// until Delete so active consumers never leak stale secrets.
	rv := v.RedactValues(th)
	var held []string
	for _, val := range rv {
		held = append(held, val)
	}
	for _, want := range []string{"first-value", "second-value", "kept-value"} {
		if !slices.Contains(held, want) {
			t.Errorf("RedactValues after rotate+forget = %v, want %q retained", rv, want)
		}
	}
	if err := v.Put(th, map[string]string{"ENV": "collides-with-env-script"}); err == nil {
		t.Error("Vault.Put accepted ENV, which collides with EnvScript on case-insensitive volumes")
	}
	if err := v.Delete(th); err != nil {
		t.Fatal(err)
	}
	if len(v.RedactValues(th)) != 0 {
		t.Errorf("RedactValues after Delete = %v, want empty", v.RedactValues(th))
	}
}

// Reopening a vault after restart and calling Forget before reading the
// thread still preserves its on-disk values for RedactValues.
func TestForgetAfterRestartRetainsRedactValues(t *testing.T) {
	dir := t.TempDir()
	th := api.NewID()
	if err := New(dir).Put(th, map[string]string{"KEY": "persisted-secret-value"}); err != nil {
		t.Fatal(err)
	}
	reopened := New(dir)
	if err := reopened.Forget(th); err != nil {
		t.Fatal(err)
	}
	rv := reopened.RedactValues(th)
	if rv["KEY"] != "persisted-secret-value" {
		t.Fatalf("RedactValues after restart+Forget = %v, want persisted-secret-value retained", rv)
	}
}

// On a case-sensitive volume, a legacy vault containing an ENV file distinct
// from EnvScript still loads ENV for redaction while Put rejects new ENV
// writes.
func TestLegacyENVFileRetainedOnCaseSensitiveVolume(t *testing.T) {
	dir := t.TempDir()
	th := api.NewID()
	v := New(dir)
	if _, err := v.Prepare(th); err != nil {
		t.Fatal(err)
	}
	envPath := filepath.Join(v.Dir(th), EnvScript)
	legacyPath := filepath.Join(v.Dir(th), "ENV")
	if err := os.WriteFile(legacyPath, []byte("legacy-env-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	envInfo, err := os.Lstat(envPath)
	if err != nil {
		t.Fatal(err)
	}
	legacyInfo, err := os.Lstat(legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(envInfo, legacyInfo) {
		t.Skip("case-insensitive filesystem; ENV and env are the same file")
	}
	reopened := New(dir)
	if got := reopened.RedactValues(th)["ENV"]; got != "legacy-env-secret" {
		t.Fatalf("RedactValues on legacy ENV file = %v, want legacy-env-secret", reopened.RedactValues(th))
	}
	if err := reopened.Put(th, map[string]string{"ENV": "new-value"}); err == nil {
		t.Fatal("Put accepted new ENV credential")
	}
}

// scanned is v's Held once the vault has read every thread's folder.
func scanned(t *testing.T, v *Vault) Held {
	t.Helper()
	v.Held()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		v.mu.Lock()
		done := v.scanned
		v.mu.Unlock()
		if done {
			return v.Held()
		}
		if time.Now().After(deadline) {
			t.Fatal("the vault never read its threads' folders")
		}
	}
}

// Held holds every value any thread has held, replaced and forgotten ones
// too, until its thread is deleted.
func TestHeldKeepsEveryThreadsValuesUntilDelete(t *testing.T) {
	v := New(t.TempDir())
	th, other := api.NewID(), api.NewID()
	for _, value := range []string{"old-value", "new-value"} {
		if err := v.Put(th, map[string]string{"KEY": value}); err != nil {
			t.Fatal(err)
		}
	}
	if err := v.Put(other, map[string]string{"KEY": "other-value"}); err != nil {
		t.Fatal(err)
	}
	if err := v.Forget(th); err != nil {
		t.Fatal(err)
	}
	if h := scanned(t, v); !h.Complete || len(h.Values) != 3 || !slices.Contains(h.Values, "old-value") || !slices.Contains(h.Values, "new-value") || !slices.Contains(h.Values, "other-value") {
		t.Errorf("after a replace and a forget, Held is %+v; want every thread's values, complete", h)
	}
	if err := v.Delete(th); err != nil {
		t.Fatal(err)
	}
	if h := v.Held(); !slices.Equal(h.Values, []string{"other-value"}) || !h.Complete {
		t.Errorf("after a delete, Held is %+v; want the other thread's value alone", h)
	}
}

// A vault that has yet to read its threads' folders holds nothing,
// incomplete, and reads them without the caller waiting: Held never waits
// on the vault.
func TestHeldNeverWaits(t *testing.T) {
	dir := t.TempDir()
	th := api.NewID()
	if err := New(dir).Put(th, map[string]string{"KEY": "cold-value"}); err != nil {
		t.Fatal(err)
	}
	v := New(dir)
	v.mu.Lock()
	if h := v.Held(); len(h.Values) != 0 || h.Complete {
		t.Errorf("before the vault read the folders, Held is %+v; want nothing, incomplete", h)
	}
	v.mu.Unlock()
	if h := scanned(t, v); !h.Complete || !slices.Equal(h.Values, []string{"cold-value"}) {
		t.Errorf("once read, Held is %+v; want the value, complete", h)
	}
}

// The vault's values are all known only where every thread's environment
// script exports the values its files hold, as an interrupted Put leaves
// it not.
func TestHeldIsCompleteOnlyWhereEveryScriptAgrees(t *testing.T) {
	for _, c := range []struct {
		name     string
		change   func(dir string) error
		complete bool
	}{
		{"as Put left it", func(string) error { return nil }, true},
		{"a value newer than the script's", func(dir string) error { return os.WriteFile(filepath.Join(dir, "KEY"), []byte("newer"), 0o600) }, false},
		{"a value gone that the script holds", func(dir string) error { return os.Remove(filepath.Join(dir, "KEY")) }, false},
		{"the script gone", func(dir string) error { return os.Remove(filepath.Join(dir, EnvScript)) }, false},
		{"the script no file", func(dir string) error {
			os.Remove(filepath.Join(dir, EnvScript))
			return os.Mkdir(filepath.Join(dir, EnvScript), 0o700)
		}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			th, healthy := api.NewID(), api.NewID()
			if err := New(dir).Put(th, map[string]string{"KEY": "value-1"}); err != nil {
				t.Fatal(err)
			}
			if err := New(dir).Put(healthy, map[string]string{"KEY": "healthy-value"}); err != nil {
				t.Fatal(err)
			}
			v := New(dir)
			if err := c.change(v.Dir(th)); err != nil {
				t.Fatal(err)
			}
			if h := scanned(t, v); h.Complete != c.complete || !slices.Contains(h.Values, "healthy-value") {
				t.Errorf("Held is %+v, want complete %v, the healthy thread's value too", h, c.complete)
			}
		})
	}
}

// A value Put writes is held before any agent can read it from its file:
// until Held has it, Held is incomplete.
func TestAPutsValuesAreHeldBeforeAnAgentCanReadThem(t *testing.T) {
	v := New(t.TempDir())
	th := api.NewID()
	if err := v.Put(th, map[string]string{"KEY": "value-0"}); err != nil {
		t.Fatal(err)
	}
	scanned(t, v)
	file := filepath.Join(v.Dir(th), "KEY")
	stop, done := make(chan struct{}), make(chan struct{})
	var unheld []string
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			b, err := os.ReadFile(file)
			if h := v.Held(); err == nil && h.Complete && !slices.Contains(h.Values, string(b)) {
				unheld = append(unheld, string(b))
			}
		}
	}()
	for i := range 300 {
		if err := v.Put(th, map[string]string{"KEY": fmt.Sprintf("value-%d", i+1)}); err != nil {
			t.Fatal(err)
		}
	}
	close(stop)
	<-done
	if len(unheld) > 0 {
		t.Errorf("an agent could read %d values a complete Held lacked, as %q", len(unheld), unheld[0])
	}
}

// Prepare, on every run's start, reads nothing for Held: only a provider
// log that is on asks for it.
func TestPrepareReadsNothingForHeld(t *testing.T) {
	v := New(t.TempDir())
	if _, err := v.Prepare(api.NewID()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.scanned || v.held.Load() != nil {
		t.Error("Prepare read the vault for Held")
	}
}

// Delete is for good: the vault keeps nothing more of the thread, and
// prepares no folder for it.
func TestDeleteIsForGood(t *testing.T) {
	v := New(t.TempDir())
	th := api.NewID()
	if err := v.Delete(th); err != nil {
		t.Fatal(err)
	}
	if err := v.Put(th, map[string]string{"KEY": "late"}); !errors.Is(err, ErrDeleted) {
		t.Errorf("a Put after Delete: %v, want ErrDeleted", err)
	}
	if _, err := v.Prepare(th); !errors.Is(err, ErrDeleted) {
		t.Errorf("a Prepare after Delete: %v, want ErrDeleted", err)
	}
	if _, err := os.Stat(v.Dir(th)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the deleted thread's folder: %v, want none", err)
	}
}

func TestPutRefusesANameThatIsNoVariable(t *testing.T) {
	v := New(t.TempDir())
	if err := v.Put(api.NewID(), map[string]string{"../x": "value-123"}); err == nil {
		t.Fatal("a path took a value")
	}
}

func TestPrepareKeepsWhatAHookAppended(t *testing.T) {
	v := New(t.TempDir())
	th := api.NewID()
	dir, _ := v.Prepare(th)
	f, _ := os.OpenFile(filepath.Join(dir, ClaudeEnv), os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString("export HOOK=1\n")
	f.Close()
	v.Put(th, map[string]string{"A": "value-123"})
	v.Prepare(th)
	b, _ := os.ReadFile(filepath.Join(dir, ClaudeEnv))
	if !strings.HasSuffix(string(b), "export HOOK=1\n") || !strings.HasPrefix(string(b), ". ") {
		t.Errorf("claude.env is %q", b)
	}
}

func TestRedact(t *testing.T) {
	values := map[string]string{"KEY": "sk-secret-12345", "PIN": "1234", "INNER": "secret-12"}
	for _, tc := range []struct{ in, want string }{
		{"use sk-secret-12345 now", "use [KEY] now"},
		{"pin 1234 stays", "pin 1234 stays"},
		{"secret-12 alone", "[INNER] alone"},
		{"twice sk-secret-12345sk-secret-12345", "twice [KEY][KEY]"},
	} {
		if got := Redact(tc.in, values); got != tc.want {
			t.Errorf("Redact(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestRedactStreamCatchesAValueSplitAcrossDeltas(t *testing.T) {
	values := map[string]string{"KEY": "sk-secret-12345"}
	var shown strings.Builder
	kept := ""
	for _, d := range []string{"the key is sk-se", "cret-1", "2345, done. sk-", "nothing"} {
		out, k := RedactStream(kept+d, values, false)
		shown.WriteString(out)
		kept = k
	}
	out, _ := RedactStream(kept, values, true)
	shown.WriteString(out)
	if got, want := shown.String(), "the key is [KEY], done. sk-nothing"; got != want {
		t.Errorf("shown %q, want %q", got, want)
	}
}

// A value inside JSON is found however the JSON escapes it, and a document
// that holds none comes back as it was.
func TestRedactJSONFindsEscapedValues(t *testing.T) {
	values := map[string]string{"KEY": "a<b>\"c\\d-12345"}
	for _, raw := range []string{
		`{"command":"echo a\u003cb\u003e\"c\\d-12345"}`,
		`{"command":"echo \u0061<b>\"c\\d-12345"}`,
		`{"args":["x",{"a<b>\"c\\d-12345":1}]}`,
	} {
		got := string(RedactJSON(json.RawMessage(raw), values))
		if strings.Contains(got, "d-12345") || !strings.Contains(got, "[KEY]") || !json.Valid([]byte(got)) {
			t.Errorf("RedactJSON(%s) = %s", raw, got)
		}
	}
	for _, raw := range []string{"", "null", `{"a":  "b"}`, "not json"} {
		if got := string(RedactJSON(json.RawMessage(raw), values)); got != raw {
			t.Errorf("RedactJSON(%q) = %q, want it unchanged", raw, got)
		}
	}
}

package secrets

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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

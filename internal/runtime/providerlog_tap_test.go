package runtime

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/provider"
	"glitched/internal/providerlog"
	"glitched/internal/secrets"
)

// tapped is a host with vault and a provider log, on, and the log's
// folder.
func tapped(t *testing.T, vault *secrets.Vault) (*Host, *providerlog.Log, string) {
	t.Helper()
	if !providerlog.Supported {
		t.Skip("this system keeps no provider log")
	}
	state := t.TempDir()
	plog, err := providerlog.Open(state)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(plog.Close)
	h := &Host{vault: vault}
	WithProviderLog(plog, true)(h)
	return h, plog, providerlog.Under(state)
}

// holding returns once vault's Held, which its first asking has it read,
// holds value.
func holding(t *testing.T, vault *secrets.Vault, value string) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); !slices.Contains(vault.Held().Values, value); time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("the vault never held %q", value)
		}
	}
}

// said is the log's file of thread, once what was written is.
func said(t *testing.T, plog *providerlog.Log, dir string, thread api.ID) []byte {
	t.Helper()
	plog.Flush()
	b, err := os.ReadFile(providerlog.File(dir, thread))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// A tap made before a run's agent parked hides every value the thread
// has held, the ones since replaced or forgotten too, as the parked agent
// may still say them.
func TestAParkedTapHidesValuesTheThreadNoLongerHolds(t *testing.T) {
	vault := secrets.New(t.TempDir())
	h, plog, dir := tapped(t, vault)
	thread := api.NewID()
	tap := h.tap(thread)
	const old, replaced = "synthetic-old-token-451", "synthetic-new-token-784"
	say := func(value string) {
		tap.Line(provider.ClaudeStreamJSON, provider.Stdout, []byte(`{"type":"`+value+`"}`))
	}
	if err := vault.Put(thread, map[string]string{"GLX_REVIEW": old}); err != nil {
		t.Fatal(err)
	}
	for !vault.Held().Complete {
		time.Sleep(time.Millisecond)
	}
	say(old)
	if err := vault.Put(thread, map[string]string{"GLX_REVIEW": replaced}); err != nil {
		t.Fatal(err)
	}
	say(old)
	if err := h.ForgetCredentials(context.Background(), thread); err != nil {
		t.Fatal(err)
	}
	say(old)
	say(replaced)
	if b := said(t, plog, dir, thread); bytes.Contains(b, []byte(old)) || bytes.Contains(b, []byte(replaced)) || bytes.Count(b, []byte(`"type":"<str:23>"`)) != 4 {
		t.Errorf("the log holds, after the value was replaced and forgotten:\n%s", b)
	}
}

// A thread's tap hides the values every other thread holds too, as one
// thread's credential may reach a workspace another's agent reads.
func TestATapHidesEveryThreadsValues(t *testing.T) {
	vault := secrets.New(t.TempDir())
	h, plog, dir := tapped(t, vault)
	owner, other := api.NewID(), api.NewID()
	const value = "synthetic-shared-token-52"
	if err := vault.Put(owner, map[string]string{"GLX_SHARED": value}); err != nil {
		t.Fatal(err)
	}
	for !vault.Held().Complete {
		time.Sleep(time.Millisecond)
	}
	h.tap(other).Line(provider.ClaudeStreamJSON, provider.Stdout, []byte(`{"type":"`+value+`"}`))
	if b := said(t, plog, dir, other); bytes.Contains(b, []byte(value)) || !bytes.Contains(b, []byte(`"type":"<str:25>"`)) {
		t.Errorf("another thread's log holds:\n%s", b)
	}
}

// A vault that can't tell every value its threads hold, which a thread's
// environment script may still hold for its agent, leaves every thread's
// lines their pipes and lengths alone: where it can't read a value, and
// where the script and the files disagree, as an interrupted Put leaves
// them.
func TestAColdVaultItCantTellLeavesLinesTheirLengthAlone(t *testing.T) {
	const value, healthy = "cold-vault-value-93", "healthy-vault-value-17"
	for _, c := range []struct {
		name   string
		change func(t *testing.T, file string)
		shape  bool
	}{
		{"as Put left it", func(*testing.T, string) {}, true},
		{"a value it can't read", func(t *testing.T, file string) {
			if os.Geteuid() == 0 {
				t.Skip("root reads a file of mode 000")
			}
			if err := os.Chmod(file, 0); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.Chmod(file, 0o600) })
		}, false},
		{"a value file gone", func(t *testing.T, file string) {
			if err := os.Remove(file); err != nil {
				t.Fatal(err)
			}
		}, false},
		{"a value file newer than the script", func(t *testing.T, file string) {
			if err := os.WriteFile(file, []byte("cold-vault-value-94"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			vdir := t.TempDir()
			thread, other := api.NewID(), api.NewID()
			if err := secrets.New(vdir).Put(thread, map[string]string{"GLX_COLD": value}); err != nil {
				t.Fatal(err)
			}
			if err := secrets.New(vdir).Put(other, map[string]string{"GLX_HEALTHY": healthy}); err != nil {
				t.Fatal(err)
			}
			cold := secrets.New(vdir)
			c.change(t, filepath.Join(cold.Dir(thread), "GLX_COLD"))
			if b, err := os.ReadFile(filepath.Join(cold.Dir(thread), secrets.EnvScript)); err != nil || !bytes.Contains(b, []byte(value)) {
				t.Fatalf("the environment script holds %q, %v; want the value", b, err)
			}
			h, plog, dir := tapped(t, cold)
			holding(t, cold, healthy)
			h.tap(other).Line(provider.ClaudeStreamJSON, provider.Stdout, []byte(`{"type":"`+value+`"}`))
			if b := said(t, plog, dir, other); bytes.Contains(b, []byte(value)) || bytes.Contains(b, []byte("json")) != c.shape {
				t.Errorf("the log holds, want its shape %v:\n%s", c.shape, b)
			}
		})
	}
}

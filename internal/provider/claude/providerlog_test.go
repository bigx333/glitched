package claude_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/provider"
	"glitched/internal/providerlog"
	"glitched/internal/runtime"
	"glitched/internal/secrets"
)

// The provider log keeps nothing while it is off. Turned on, it keeps at
// once the shape of each line on the pipes of the thread's claude, the
// one parked since the thread's last run too, and nothing any line said:
// a stderr line keeps its length alone. Deleting the thread deletes its
// log.
func TestTheProviderLogKeepsTheShapeOfClaudesLines(t *testing.T) {
	testdata := filepath.Join(must(os.Getwd()), "testdata")
	t.Setenv(envFake, filepath.Join(testdata, "hi.jsonl")+string(os.PathListSeparator)+filepath.Join(testdata, "provider-log.jsonl"))
	state := t.TempDir()
	dir := providerlog.Under(state)
	vault := secrets.New(t.TempDir())
	if !providerlog.Supported {
		t.Skip("this system keeps no provider log")
	}
	plog, err := providerlog.Open(state)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(plog.Close)
	h := host(t, nil, runtime.WithVault(vault), runtime.WithProviderLog(plog, false))
	thread := newThread(t, h, api.AccessAsk)
	if err := vault.Put(thread, map[string]string{"GLX_PROBE": probeValue}); err != nil {
		t.Fatal(err)
	}

	if got := turn(t, h, thread, "Say hi in three words."); got.ended.Outcome != api.OutcomeCompleted {
		t.Fatalf("the first turn ended %+v, want completed", got.ended)
	}
	if _, err := os.Stat(providerlog.File(dir, thread)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("with the log off, the thread's file: %v, want none", err)
	}

	h.SetProviderLog(true)
	for !vault.Held().Complete {
		time.Sleep(time.Millisecond)
	}
	if got := turn(t, h, thread, "My key is "+probeValue+"."); got.ended.Outcome != api.OutcomeFailed {
		t.Fatalf("the second turn ended %+v, want failed, as its claude exited", got.ended)
	}
	plog.Flush()
	b := must(os.ReadFile(providerlog.File(dir, thread)))
	for _, part := range []string{probeValue[:8], probeValue[8:]} {
		if bytes.Contains(b, []byte(part)) {
			t.Errorf("the log holds %q of the value:\n%s", part, b)
		}
	}
	said := map[provider.Pipe][]string{}
	for line := range strings.Lines(string(b)) {
		var r struct {
			Pipe  provider.Pipe
			Kind  string
			Bytes int
			JSON  json.RawMessage
		}
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("the log holds %q: %v", line, err)
		}
		said[r.Pipe] = append(said[r.Pipe], r.Kind+" "+string(r.JSON)+" "+strconv.Itoa(r.Bytes))
	}
	holds := func(pipe provider.Pipe, s string) int {
		return len(slices.DeleteFunc(slices.Clone(said[pipe]), func(l string) bool { return !strings.Contains(l, s) }))
	}
	if len(said[provider.Stdin]) != 1 || holds(provider.Stdin, `"content":"<str:27>"`) != 1 {
		t.Errorf("claude's stdin is logged as %q, want the turn's message alone: the parked claude was initialized before", said[provider.Stdin])
	}
	if holds(provider.Stdout, `"text":"<str:28>"`) != 1 || holds(provider.Stdout, "stream_event/content_block_delta ") != 2 || holds(provider.Stdout, `"subtype":"init"`) != 1 {
		t.Errorf("claude's stdout is logged as %q, want its init, its message and its two pieces, by their shapes", said[provider.Stdout])
	}
	if !slices.Equal(said[provider.Stderr], []string{"  21", "  29"}) {
		t.Errorf("claude's stderr is logged as %q, want its two lines by their lengths alone", said[provider.Stderr])
	}

	submit(t, h, thread, api.ThreadDelete{})
	if _, err := os.Stat(providerlog.File(dir, thread)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the deleted thread's log: %v, want none", err)
	}
}

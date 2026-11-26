package plugins

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	plugin "github.com/yxxbc/mygo-plugin"
	"github.com/yxxbc/mygo-plugin/plugintest"
)

const hello = "../../examples/plugins/hello"

func TestHelloAnswersItsPageAndCountsGreetings(t *testing.T) {
	p := plugintest.New(t, hello, plugin.Options{Contract: Contract, ContractVersion: ContractVersion})
	page, err := p.Page("home")
	if err != nil {
		t.Fatal(err)
	}
	if page.Title != "Hello" || page.Markdown == "" || len(page.Sections) != 1 {
		t.Fatalf("page = %+v", page)
	}
	count := page.Sections[0].Items[0]
	if count.Title != "Greeted 0 times" || len(count.Actions) != 1 || count.Actions[0].Kind != "command" {
		t.Fatalf("count item = %+v", count)
	}
	if err := p.RunCommand("greet", nil); err != nil {
		t.Fatal(err)
	}
	page, err = p.Page("home")
	if err != nil {
		t.Fatal(err)
	}
	if got := page.Sections[0].Items[0].Title; got != "Greeted once" {
		t.Fatalf("after greet: %q", got)
	}
	if err := p.RunCommand("nope", nil); err == nil {
		t.Fatal("an unknown command ran")
	}
}

// install copies the sample plugin into the state directory's plugins
// folder, as name.
func install(t *testing.T, state, name string) {
	t.Helper()
	dst := filepath.Join(Dir(state), name)
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"plugin.json", "index.ts"} {
		b, err := os.ReadFile(filepath.Join(hello, f))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dst, f), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	signAsOwn(t, state, dst)
}

func writePlugin(t *testing.T, state, name, manifest string) {
	t.Helper()
	dst := filepath.Join(Dir(state), name)
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dst, "plugin.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dst, "index.js"), []byte("export function page() { return { title: 'x', sections: [] } }"), 0o644); err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(manifest, "{\"") {
		signAsOwn(t, state, dst) // not the broken one
	}
}

type logs []string

func (l *logs) logf(format string, args ...any) { *l = append(*l, fmt.Sprintf(format, args...)) }

func TestOpenLoadsWhatFitsAndLeavesTheRestOut(t *testing.T) {
	state := t.TempDir()
	install(t, state, "a-hello")
	install(t, state, "b-hello-again")
	writePlugin(t, state, "music", `{"id": "music", "name": "Music", "contract": "mygo-music", "contributes": {"pages": [{"id": "home"}]}}`)
	writePlugin(t, state, "newer", `{"id": "newer", "name": "Newer", "contract": "glitched", "contractVersion": 3, "contributes": {"pages": [{"id": "home"}]}}`)
	writePlugin(t, state, "broken", `{`)
	var l logs
	h := Open(state, Options{Logf: l.logf})
	t.Cleanup(h.Close)

	want := []Entry{{Plugin: "hello", Page: "home", Title: "Hello", Icon: "sparkles"}}
	if got := h.Entries(); !slices.Equal(got, want) {
		t.Fatalf("entries = %+v, want %+v", got, want)
	}
	if h.Name("hello") != "Hello" {
		t.Fatalf("name = %q", h.Name("hello"))
	}
	if len(l) != 4 {
		t.Fatalf("logged %d, want the copy, the other contract, the newer version and the broken one: %q", len(l), l)
	}
	for _, why := range []string{"already installed", "mygo-music", "version 2", "broken"} {
		if !slices.ContainsFunc(l, func(s string) bool { return strings.Contains(s, why) }) {
			t.Errorf("no log says %q: %q", why, l)
		}
	}
}

func TestOpenWithNothingInstalled(t *testing.T) {
	var l logs
	h := Open(t.TempDir(), Options{Logf: l.logf})
	if len(h.Entries()) != 0 || len(l) != 0 {
		t.Fatalf("entries %v, logs %q", h.Entries(), l)
	}
	h.Close()
}

func TestActRunsCommandsAndOpensOnlyWebPages(t *testing.T) {
	state := t.TempDir()
	install(t, state, "hello")
	var opened []string
	h := Open(state, Options{OpenURL: func(u string) error { opened = append(opened, u); return nil }})
	t.Cleanup(h.Close)

	page, err := h.Page("hello", "home")
	if err != nil {
		t.Fatal(err)
	}
	greet, web := page.Sections[0].Items[0].Actions[0], page.Sections[0].Items[1].Actions[0]
	if err := h.Act("hello", greet); err != nil {
		t.Fatal(err)
	}
	if page, err = h.Page("hello", "home"); err != nil || page.Sections[0].Items[0].Title != "Greeted once" {
		t.Fatalf("after greet: %+v, %v", page, err)
	}
	b, err := os.ReadFile(filepath.Join(DataDir(state), "hello", "storage.json"))
	if err != nil || !strings.Contains(string(b), `"greetings": 1`) && !strings.Contains(string(b), `"greetings":1`) {
		t.Fatalf("storage.json = %s, %v", b, err)
	}
	if err := h.Act("hello", web); err != nil || !slices.Equal(opened, []string{"https://github.com/yxxbc/mygo-plugin"}) {
		t.Fatalf("opened %q, %v", opened, err)
	}

	for _, a := range []plugin.Action{
		{ID: "file", Kind: "url", URL: "file:///etc/passwd"},
		{ID: "scheme", Kind: "url", URL: "glitched://x"},
		{ID: "nameless", Kind: "command"},
		{ID: "odd", Kind: "play"},
	} {
		if err := h.Act("hello", a); err == nil {
			t.Errorf("%s: no error", a.ID)
		}
	}
	if len(opened) != 1 {
		t.Fatalf("opened %q", opened)
	}
	if _, err := h.Page("gone", "home"); err == nil {
		t.Fatal("a page of a plugin not installed")
	}
}

func TestEveryExamplePluginUsesTypeScriptAndCompiles(t *testing.T) {
	const root = "../../examples/plugins"
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	var count int
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		count++
		dir := filepath.Join(root, e.Name())
		b, err := os.ReadFile(filepath.Join(dir, "plugin.json"))
		if err != nil {
			t.Fatalf("%s: %v", e.Name(), err)
		}
		var m struct {
			Main     string `json:"main"`
			Glitched struct {
				Service string `json:"service"`
			} `json:"glitched"`
		}
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("%s: %v", e.Name(), err)
		}
		for _, v := range CheckTypeScriptPlugin(dir) {
			t.Errorf("%s: %v", e.Name(), v)
		}
		p, err := plugin.Load(dir, plugin.Options{Main: m.Main, Prelude: Prelude, Contract: Contract, ContractVersion: ContractVersion, DataDir: t.TempDir()})
		if err != nil {
			t.Errorf("%s (%s): %v", e.Name(), m.Main, err)
			continue
		}
		p.Shutdown()
		if m.Glitched.Service != "" {
			svc, err := plugin.Load(dir, plugin.Options{Main: m.Glitched.Service, Prelude: Prelude, Contract: Contract, ContractVersion: ContractVersion, DataDir: t.TempDir()})
			if err != nil {
				t.Errorf("%s (%s): %v", e.Name(), m.Glitched.Service, err)
				continue
			}
			svc.Shutdown()
		}
	}
	if count == 0 {
		t.Fatal("found no example plugins")
	}
}

package plugins

import (
	"strings"
	"testing"
)

func TestACreateThatNamesAnAccessNeedsThreadsAccess(t *testing.T) {
	state, u, fx := t.TempDir(), newUIThread(), &effects{}
	writeModule(t, state, "starter", `"capabilities": ["threads:write", "threads:access"], `, `
		async function try_(f) { try { return "ok:" + JSON.stringify(await f()) } catch (e) { return e.code + ":" + e.message } }
		export async function init() {
			return { state: [
				await try_(() => glitched.threads.create("plain")),
				await try_(() => glitched.threads.create("wide", { access: "Full access" })),
				await try_(() => glitched.threads.create("asks", { access: "Ask for approval" })),
			] }
		}
		export function view({ state }) { return { body: { type: "text", text: state.join(" | ") } } }`)
	h := open(t, state, u, fx)
	if err := h.SetGranted("starter", "threads:write", true); err != nil {
		t.Fatal(err)
	}
	if err := h.SetGranted("starter", "threads:access", false); err != nil {
		t.Fatal(err)
	}
	s := session(t, h, "starter")
	u.until(t, "the three answers", func() bool { return strings.Count(texts(s), " | ") == 2 })
	got := strings.Split(strings.TrimSuffix(texts(s), "||"), " | ")
	want := []string{
		"ok:null",
		"denied:permission denied: threads.create needs the threads:access capability, which isn't granted in Settings › Plugins",
		"denied:permission denied: threads.create needs the threads:access capability, which isn't granted in Settings › Plugins",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("the answers\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if len(fx.ran) != 1 || !strings.Contains(string(fx.ran[0].Args), `"plain"`) {
		t.Fatalf("the app ran %+v, want the plain create alone", fx.ran)
	}
}

func TestACreateThatNamesAnAccessWithoutDeclaringItIsRefused(t *testing.T) {
	state, u, fx := t.TempDir(), newUIThread(), &effects{}
	writeModule(t, state, "starter", `"capabilities": ["threads:write"], `, `
		export async function init() {
			try { await glitched.threads.create("wide", { access: "Full access" }); return { state: "ran" } } catch (e) { return { state: e.code + ":" + e.message } }
		}
		export function view({ state }) { return { body: { type: "text", text: state } } }`)
	h := open(t, state, u, fx)
	if err := h.SetGranted("starter", "threads:write", true); err != nil {
		t.Fatal(err)
	}
	s := session(t, h, "starter")
	u.until(t, "the answer", func() bool { return texts(s) != "" })
	if want := "denied:permission denied: threads.create needs the threads:access capability, which the plugin doesn't declare"; !strings.Contains(texts(s), want) {
		t.Fatalf("got %q, want %q", texts(s), want)
	}
	if len(fx.ran) != 0 {
		t.Fatalf("the app ran %+v", fx.ran)
	}
}

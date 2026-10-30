package sysweb

import "testing"

// A page whose process crashed or was ended loads again; one only
// unresponsive keeps its state, as it may come back.
func TestReloads(t *testing.T) {
	for reason, want := range map[string]bool{"crashed": true, "browser-exited": true, "killed": true, "unresponsive": false} {
		if got := reloads(reason); got != want {
			t.Errorf("reloads(%q) = %v, want %v", reason, got, want)
		}
	}
}

// A crashed page reloads once for each navigation: crashing again before
// another, it fails. Its reload committing is no new navigation; the app's
// and the page's going elsewhere are.
func TestCrashesReloadOncePerNavigation(t *testing.T) {
	type step struct {
		do           string // "crash", "hang", "asked", or a URL committed
		reload, fail bool
	}
	const a, b = "https://example.com/a", "https://example.com/b"
	for _, s := range [][]step{
		{{do: "crash", reload: true}, {do: a}, {do: "crash", fail: true}, {do: "crash", fail: true}},
		{{do: "crash", reload: true}, {do: "asked"}, {do: "crash", reload: true}, {do: "crash", fail: true}},
		{{do: "crash", reload: true}, {do: b}, {do: "crash", reload: true}, {do: b}, {do: "crash", fail: true}},
		{{do: "hang"}, {do: "hang"}, {do: "crash", reload: true}},
	} {
		c := &crashes{}
		at := a
		for i, st := range s {
			switch st.do {
			case "asked":
				c.asked()
				continue
			case "crash", "hang":
			default:
				at = st.do
				c.committed(at)
				continue
			}
			reason := "crashed"
			if st.do == "hang" {
				reason = "unresponsive"
			}
			reload, fail := c.gone(reason, at)
			if reload != st.reload || fail != st.fail {
				t.Errorf("%v, step %d: reload %v, fail %v", s, i, reload, fail)
			}
		}
	}
}

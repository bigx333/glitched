// Package webtarget finds the elements of a page in the browser pane for
// agents: a snapshot lists what the page shows as lines of roles and
// names, as an accessibility tree reads, and gives each element an agent
// can act on a ref, as s12e3, which finds it again while that snapshot is
// the page's latest. A snapshot replaces the page's earlier refs, and a
// page navigating or reloading drops them all, so that a ref never finds
// another element than the one it named: resolving a stale one says why,
// and to take a new snapshot.
//
// The page's side is a script it evaluates (snapshot.js), as the web view
// gives no accessibility tree of its own; the snapshot's format and the
// refs' epochs follow a prior internal automation engine's design
// (docs/browser.md).
package webtarget

import (
	"context"
	cryptorand "crypto/rand"
	_ "embed"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"regexp"
	"strconv"
	"sync/atomic"
	"unicode/utf8"
)

//go:embed snapshot.js
var script string

// Page evaluates JavaScript in a page and returns its result, decoded
// from JSON, as *mygo.Page does.
type Page interface {
	EvalContext(ctx context.Context, code string) (any, error)
}

// Options shape a snapshot.
type Options struct {
	// Full lists the page's structure, its landmarks, lists and headings,
	// around the elements to act on; by default only those, and headings.
	Full bool
	// MaxRefs and MaxChars bound the snapshot, which says where it was
	// cut; by default 2000 refs and 50 000 characters.
	MaxRefs, MaxChars int
}

// Snapshot is what a page showed, as an agent reads it.
type Snapshot struct {
	Epoch     uint64 `json:"epoch"`
	URL       string `json:"url"`
	Title     string `json:"title"`
	Refs      int    `json:"refs"`
	Truncated bool   `json:"truncated"`
	// Text is a line for each element, indented under its container:
	//	- link "Docs" [ref=s12e3]
	//	- heading "Install" [level=2]
	Text string `json:"text"`
	// Elements are the elements with refs, in order: what each ref named.
	Elements []Named `json:"elements"`
}

// Named is an element with a ref, as a snapshot named it.
type Named struct {
	Ref  string `json:"ref"`
	Role string `json:"role"`
	Name string `json:"name"`
}

// Verify reports whether what a ref finds now is what the snapshot named,
// as an agent's action checks before it acts: a page that changed an
// element since, or tampered with the refs, fails.
func (s Snapshot) Verify(ref string, e Element) error {
	n, err := s.named(ref)
	if err != nil {
		return err
	}
	if n.Role != e.Role || n.Name != e.Name {
		return fmt.Errorf("webtarget: %s was %s %q, now %s %q; take a new snapshot", ref, n.Role, n.Name, e.Role, e.Name)
	}
	return nil
}

// named is what the snapshot named ref.
func (s Snapshot) named(ref string) (Named, error) {
	for _, n := range s.Elements {
		if n.Ref == ref {
			return n, nil
		}
	}
	return Named{}, fmt.Errorf("webtarget: %s is not a ref of snapshot s%d", ref, s.Epoch)
}

// Element is what a ref finds: its role, its name, and where it shows in
// the page's viewport, in CSS pixels.
type Element struct {
	Role string `json:"role"`
	Name string `json:"name"`
	Box  struct {
		X, Y, Width, Height float64
	} `json:"box"`
}

// epochs numbers snapshots, from a random start, so that a ref of an
// earlier run of the app finds nothing in a page that outlived it.
var epochs atomic.Uint64

// key names where the page script keeps its state in a page, random to
// each run of the app. It hides nothing from a page that looks: the
// property isn't enumerable, but Object.getOwnPropertyNames(window) lists
// it. As it lasts the run, a page that learns it can plant a fake under it
// in a later document, one it navigates to or another tab's at a site it
// controls, before that document's first snapshot. The checks of Take and
// Describe bound what a fake returns, not what it says; isolating the
// script from the page is the fix (docs/browser.md, Next 3).
var key = "__glitched_" + cryptorand.Text()

// What a page's answer may hold, beside the snapshot's own budgets: its
// title and URL, which the script clips to these too (snapshot.js), an
// element's role and name, and the line saying where a snapshot was cut,
// past its budget of characters.
const (
	maxTitle  = 300
	maxURL    = 2048
	maxRole   = 80
	maxName   = 120
	maxMarker = 200
)

func init() { epochs.Store(uint64(rand.IntN(1 << 20))) }

// Take snapshots the page.
func Take(ctx context.Context, p Page, o Options) (Snapshot, error) {
	if o.MaxRefs <= 0 {
		o.MaxRefs = 2000
	}
	if o.MaxChars <= 0 {
		o.MaxChars = 50000
	}
	var s Snapshot
	epoch := epochs.Add(1)
	err := run(ctx, p, map[string]any{
		"op": "snapshot", "epoch": epoch, "interactive": !o.Full,
		"maxRefs": o.MaxRefs, "maxChars": o.MaxChars,
		"maxTitle": maxTitle, "maxURL": maxURL,
	}, &s)
	if err == nil {
		err = s.check(epoch, o)
	}
	if err != nil {
		return Snapshot{}, err
	}
	return s, nil
}

// check reports whether a page's snapshot is one the script would have
// taken as asked: of the epoch asked for, its refs that epoch's, in
// order, and within its budgets.
func (s Snapshot) check(epoch uint64, o Options) error {
	bad := func(format string, a ...any) error {
		return fmt.Errorf("webtarget: the page's snapshot is not the script's: "+format, a...)
	}
	switch {
	case s.Epoch != epoch:
		return bad("it is s%d, asked for as s%d", s.Epoch, epoch)
	case len(s.Elements) > o.MaxRefs:
		return bad("it has %d refs, past the %d asked for", len(s.Elements), o.MaxRefs)
	case s.Refs != len(s.Elements):
		return bad("it counts %d refs and lists %d", s.Refs, len(s.Elements))
	case utf8.RuneCountInString(s.Text) > o.MaxChars+maxMarker:
		return bad("its text is %d characters, past the %d asked for", utf8.RuneCountInString(s.Text), o.MaxChars)
	case utf8.RuneCountInString(s.Title) > maxTitle:
		return bad("its title is %d characters, past %d", utf8.RuneCountInString(s.Title), maxTitle)
	case utf8.RuneCountInString(s.URL) > maxURL:
		return bad("its URL is %d characters, past %d", utf8.RuneCountInString(s.URL), maxURL)
	}
	for i, n := range s.Elements {
		if want := fmt.Sprintf("s%de%d", epoch, i+1); n.Ref != want {
			return bad("its ref %d is %q, not %s", i+1, n.Ref, want)
		}
		if err := sizes(n.Role, n.Name); err != nil {
			return bad("%s %v", n.Ref, err)
		}
	}
	return nil
}

// sizes reports a role or a name longer than the script makes them.
func sizes(role, name string) error {
	if n := utf8.RuneCountInString(role); n > maxRole {
		return fmt.Errorf("has a role of %d characters, past %d", n, maxRole)
	}
	if n := utf8.RuneCountInString(name); n > maxName {
		return fmt.Errorf("has a name of %d characters, past %d", n, maxName)
	}
	return nil
}

// Describe finds a ref's element in the page: what it is and where.
func Describe(ctx context.Context, p Page, ref string) (Element, error) {
	var e Element
	if _, _, err := ParseRef(ref); err != nil {
		return e, err
	}
	if err := run(ctx, p, map[string]any{"op": "describe", "ref": ref}, &e); err != nil {
		return Element{}, err
	}
	if err := sizes(e.Role, e.Name); err != nil {
		return Element{}, fmt.Errorf("webtarget: the page's answer for %s is not the script's: it %v", ref, err)
	}
	return e, nil
}

// MaxTyped is the most an agent may type in one action, in characters.
const MaxTyped = 10000

// Click clicks ref's element, a ref of s, the page's latest snapshot.
func Click(ctx context.Context, p Page, s Snapshot, ref string) error {
	return act(ctx, p, s, ref, map[string]any{"action": "click"})
}

// Type replaces the text of ref's element, a text box or an editor named
// by s, the page's latest snapshot, with text, and with submit presses
// Enter, which submits its form unless the page takes the key.
func Type(ctx context.Context, p Page, s Snapshot, ref, text string, submit bool) error {
	if n := utf8.RuneCountInString(text); n > MaxTyped {
		return fmt.Errorf("webtarget: %d characters is more than %d to type at once", n, MaxTyped)
	}
	return act(ctx, p, s, ref, map[string]any{"action": "type", "text": text, "submit": submit})
}

// act does what args ask of ref's element, in the page's script, which
// first checks it is still what s named it, shows and is enabled.
func act(ctx context.Context, p Page, s Snapshot, ref string, args map[string]any) error {
	n, err := s.named(ref)
	if err != nil {
		return err
	}
	args["op"], args["ref"], args["role"], args["name"] = "act", ref, n.Role, n.Name
	var out struct {
		Done bool `json:"done"`
	}
	if err := run(ctx, p, args, &out); err != nil {
		return err
	}
	// That catches a page answering with nothing, not one that fakes
	// the script under its key (key), which isolation will close.
	if !out.Done {
		return fmt.Errorf("webtarget: the page's answer for %s is not the script's", ref)
	}
	return nil
}

var refPattern = regexp.MustCompile(`^s(\d+)e(\d+)$`)

// ParseRef splits a ref into its snapshot's epoch and its element's
// number.
func ParseRef(ref string) (epoch uint64, n int, err error) {
	m := refPattern.FindStringSubmatch(ref)
	if m == nil {
		return 0, 0, fmt.Errorf("webtarget: %q is not a ref: refs look like s12e3", ref)
	}
	if epoch, err = strconv.ParseUint(m[1], 10, 64); err != nil {
		return 0, 0, fmt.Errorf("webtarget: %q is not a ref: its snapshot's number is too large", ref)
	}
	if n, err = strconv.Atoi(m[2]); err != nil {
		return 0, 0, fmt.Errorf("webtarget: %q is not a ref: its element's number is too large", ref)
	}
	return epoch, n, nil
}

// run evaluates the page's script with args, decoding what it returns
// into out.
func run(ctx context.Context, p Page, args map[string]any, out any) error {
	v, err := p.EvalContext(ctx, Script(args))
	if err != nil {
		return err
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

// Script is the page's script for args, the body of an async function,
// as Page.Eval runs statements.
func Script(args map[string]any) string {
	args["key"] = key
	a, _ := json.Marshal(args)
	return "const args = " + string(a) + ";\n" + script
}

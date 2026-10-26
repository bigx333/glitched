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
// gives no accessibility tree of its own; refs and their epochs follow
// the automation engine's browser MCP (docs/browser.md).
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
	for _, n := range s.Elements {
		if n.Ref == ref {
			if n.Role != e.Role || n.Name != e.Name {
				return fmt.Errorf("webtarget: %s was %s %q, now %s %q; take a new snapshot", ref, n.Role, n.Name, e.Role, e.Name)
			}
			return nil
		}
	}
	return fmt.Errorf("webtarget: %s is not a ref of snapshot s%d", ref, s.Epoch)
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
// each run, which a page can't guess to fake it.
var key = "__glitched_" + cryptorand.Text()

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
	err := run(ctx, p, map[string]any{
		"op": "snapshot", "epoch": epochs.Add(1), "interactive": !o.Full,
		"maxRefs": o.MaxRefs, "maxChars": o.MaxChars,
	}, &s)
	return s, err
}

// Describe finds a ref's element in the page: what it is and where.
func Describe(ctx context.Context, p Page, ref string) (Element, error) {
	var e Element
	if _, _, err := ParseRef(ref); err != nil {
		return e, err
	}
	err := run(ctx, p, map[string]any{"op": "describe", "ref": ref}, &e)
	return e, err
}

var refPattern = regexp.MustCompile(`^s(\d+)e(\d+)$`)

// ParseRef splits a ref into its snapshot's epoch and its element's
// number.
func ParseRef(ref string) (epoch uint64, n int, err error) {
	m := refPattern.FindStringSubmatch(ref)
	if m == nil {
		return 0, 0, fmt.Errorf("webtarget: %q is not a ref: refs look like s12e3", ref)
	}
	epoch, _ = strconv.ParseUint(m[1], 10, 64)
	n, _ = strconv.Atoi(m[2])
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

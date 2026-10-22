package main

import (
	"sync/atomic"

	"github.com/egoist/mygo/ui"
)

// Mock data: the prototype has no backend, so every project, thread and
// message here is canned.

type status int

// The agent's status on a thread. Waiting on an approval is not one: it
// is the thread's pending approval, which its entries hold.
const (
	statusIdle status = iota
	statusWorking
	statusCompleted
)

type thread struct {
	id      string
	title   string
	age     string
	status  status
	branch  string
	entries []entry
	// The inbox: unread since the agent last finished; settled, done with;
	// snoozed, out of the way until when it says.
	unread  bool
	settled bool
	snoozed string
	// turn counts the agent's turns. A goroutine playing an older turn,
	// stopped or replaced, writes nothing more.
	turn atomic.Uint64
	// home is the project an archived thread came from.
	home *project
}

type project struct {
	id      string
	name    string
	path    string
	open    bool
	threads []*thread
	// Its mark in the sidebar: an icon on a tile of its color.
	icon  *ui.SVG
	color ui.Color
}

type entryKind int

const (
	entryUser entryKind = iota
	entryAssistant
	entryWork
	entryChanges
	entryApproval
)

// decision is what the user said to an approval: nothing yet, yes, or no.
type decision int

const (
	undecided decision = iota
	allowed
	denied
	// superseded: the user sent a message instead of answering.
	superseded
)

// approval is a request from the agent that waits on the user: the
// command it wants to run, where, and why.
type approval struct {
	command string
	dir     string
	reason  string
	waiting string
	// rule is what Always allow would allow from then on.
	rule     string
	decision decision
}

type workItem struct {
	kind   string // "command", "edit", "read", "search"
	label  string
	detail string
	add    int
	del    int
	// How a command went, once it has: its exit code, how long it took,
	// and what it last printed.
	exit   int
	took   string
	output string
}

type entry struct {
	kind      entryKind
	text      string
	streaming bool
	took      string
	work      []workItem
	files     []fileChange
	approval  *approval
	// attachments went with a message of the user's.
	attachments []attachment
}

type fileChange struct {
	path string
	add  int
	del  int
}

func mockProjects() []*project {
	active := &thread{
		id:     "t1",
		title:  "Sort sidebar threads by recent activity",
		age:    "now",
		status: statusWorking,
		branch: "t3code/sidebar-sort",
		entries: []entry{
			{kind: entryUser, text: "Where does the sidebar get its threads from?"},
			{kind: entryAssistant, text: "From `useThreads(projectId)` in `apps/web/src/hooks/useThreads.ts`, which subscribes to the thread store and returns that project's threads **in the order they were created**."},
			{kind: entryUser, text: "And where is a thread's updatedAt set?"},
			{kind: entryAssistant, text: "On the server, in `ThreadStore.touch()`, whenever a turn starts or ends, or the thread is renamed. The client receives it with every thread event, so it's always current there."},
			{kind: entryUser, text: "Can the project groups collapse today?"},
			{kind: entryAssistant, text: "No. `ProjectGroup` holds an `expanded` state that is always true: nothing sets it. The label is plain text."},
			{kind: entryUser, text: "The sidebar threads are in creation order. Sort them by most recent activity, and let me collapse a project by clicking its name."},
			{kind: entryWork, took: "Worked for 38s", work: []workItem{
				{kind: "search", label: "Searched", detail: "useThreads in apps/web/src"},
				{kind: "read", label: "Read", detail: "apps/web/src/components/Sidebar.tsx"},
				{kind: "read", label: "Read", detail: "apps/web/src/hooks/useThreads.ts"},
				{kind: "edit", label: "Edited", detail: "apps/web/src/components/Sidebar.tsx", add: 9, del: 2},
				{kind: "edit", label: "Edited", detail: "apps/web/src/components/ChevronIcon.tsx", add: 14},
				{kind: "command", label: "Ran", detail: "bun run typecheck", took: "1.2s"},
				{kind: "command", label: "Ran", detail: "bun run test -- Sidebar", took: "2.8s"},
			}},
			{kind: entryAssistant, text: "Threads are now sorted by `updatedAt`, newest first, and a project's label toggles it open or closed.\n\n" +
				"**What changed**\n\n" +
				"- `Sidebar.tsx` sorts a copy of the threads inside `useMemo`, so the store's array is no longer mutated in render.\n" +
				"- The group label is a button with a chevron that rotates when the project is collapsed.\n" +
				"- Added `ChevronIcon.tsx`, a small wrapper over Lucide's chevron.\n\n" +
				"`bun run typecheck` and the Sidebar tests pass."},
			{kind: entryChanges, files: []fileChange{
				{path: "apps/web/src/components/Sidebar.tsx", add: 9, del: 2},
				{path: "apps/web/src/components/ChevronIcon.tsx", add: 14},
			}},
			{kind: entryUser, text: "Nice. Can you also persist the collapsed state in localStorage?"},
			{kind: entryWork, took: "Working…", work: []workItem{
				{kind: "read", label: "Read", detail: "apps/web/src/lib/storage.ts"},
				{kind: "command", label: "Ran", detail: "bun run test -- Sidebar", exit: 1, took: "3.4s",
					output: "✗ Sidebar › restores collapsed state (12ms)\n  expected true, received undefined\nTest Files  1 failed (1)"},
				{kind: "edit", label: "Editing", detail: "apps/web/src/components/Sidebar.tsx", add: 6, del: 1},
			}},
		},
	}
	return []*project{
		{id: "p1", name: "t3code", path: "~/Projects/t3code", open: true, icon: icTerminal, color: ui.Hex("#8b5cf6"), threads: []*thread{
			active,
			{id: "t2", title: "Fix terminal resize flicker on Windows", age: "2h", branch: "main", entries: []entry{
				{kind: entryUser, text: "The terminal flickers when the window is resized on Windows 11. Find the cause and fix it."},
				{kind: entryWork, took: "Worked for 48s", work: []workItem{
					{kind: "read", label: "Read", detail: "apps/desktop/src/terminal/resize.ts"},
					{kind: "read", label: "Read", detail: "apps/desktop/src/terminal/pty.ts"},
					{kind: "edit", label: "Edited", detail: "apps/desktop/src/terminal/resize.ts", add: 12, del: 2},
					{kind: "command", label: "Ran", detail: "bun run typecheck", took: "1.2s"},
				}},
				{kind: entryApproval, approval: &approval{
					command: "bun run test --filter=apps/desktop -- resize",
					dir:     "apps/desktop",
					reason:  "Verifies the debounce on the resize handler before the fix is marked done.",
					waiting: "2 min",
					rule:    "bun run test",
				}},
			}},
			{id: "t3", title: "Add keyboard shortcut for new thread", age: "5h", status: statusCompleted, branch: "main", unread: true},
			{id: "t4", title: "Investigate websocket reconnect loop", age: "1d", branch: "main"},
			{id: "t5", title: "Bump effect to v4 beta", age: "3d", branch: "main", settled: true},
		}},
		{id: "p2", name: "mygo", path: "~/Projects/mygo", open: true, icon: icWind, color: ui.Hex("#0ea5e9"), threads: []*thread{
			{id: "t6", title: "Prototype T3 Code UI in native Go", age: "12m", status: statusCompleted, branch: "main", unread: true},
			{id: "t7", title: "Markdown table column alignment", age: "2d", branch: "main", settled: true},
		}},
		{id: "p3", name: "agent-phone", path: "~/Projects/agent-phone", icon: icPhone, color: ui.Hex("#22c55e"), threads: []*thread{
			{id: "t8", title: "SIP re-INVITE handling", age: "1w", branch: "main", snoozed: "Tomorrow"},
			{id: "t9", title: "RTP jitter buffer underruns", age: "3d", branch: "main", settled: true},
		}},
	}
}

func mockArchived(home *project) []*thread {
	return []*thread{
		{id: "a1", title: "Try Tauri for the desktop shell", age: "3w", branch: "main", home: home},
		{id: "a2", title: "Spike: SQLite event store", age: "1mo", branch: "main", home: home},
		{id: "a3", title: "Rename provider adapters", age: "2mo", branch: "main", home: home},
	}
}

// script is what the pretend agent does after a message: its steps, then
// its reply, streamed in.
type script struct {
	steps []workItem
	reply string
}

var scripts = []script{
	{
		steps: []workItem{
			{kind: "search", label: "Searched", detail: "collapsed in apps/web/src"},
			{kind: "read", label: "Read", detail: "apps/web/src/lib/storage.ts"},
			{kind: "edit", label: "Edited", detail: "apps/web/src/components/Sidebar.tsx", add: 6, del: 1},
			{kind: "command", label: "Ran", detail: "bun run typecheck", took: "1.1s"},
		},
		reply: "Done. The collapsed projects are kept in `localStorage` under `t3code:sidebar:collapsed`, as a list of project ids.\n\n" +
			"- `useCollapsedProjects()` reads it once and writes it back on every toggle.\n" +
			"- A project removed from the list is dropped from the stored set the next time it's written, so it can't grow forever.\n\n" +
			"> This is a **prototype**: nothing ran. The steps above and this reply are canned, streamed in to show how it would feel.",
	},
	{
		steps: []workItem{
			{kind: "read", label: "Read", detail: "apps/server/src/terminal/pty.ts"},
			{kind: "command", label: "Ran", detail: "rg -n \"resize\" apps/server/src", took: "0.1s"},
			{kind: "edit", label: "Edited", detail: "apps/server/src/terminal/pty.ts", add: 11, del: 4},
		},
		reply: "The flicker comes from resizing the PTY on every `ResizeObserver` tick. I debounced it to one resize per animation frame:\n\n" +
			"```ts\nconst resize = rafThrottle((cols: number, rows: number) => {\n  pty.resize(cols, rows);\n});\n```\n\n" +
			"| Before | After |\n|---|---|\n| ~40 resizes per drag | 1 per frame |\n",
	},
}

// approved is what the agent does once its request is allowed: it runs
// the command and reports.
var approved = script{
	steps: []workItem{
		{kind: "command", label: "Ran", detail: "bun run test --filter=apps/desktop -- resize", took: "4.2s"},
	},
	reply: "The resize tests pass, the debounced handler included. The fix holds up on the three resize cases the suite covers: a drag, a maximize, and a DPI change.",
}

// refused is the agent's reply to a denied request.
const refused = "Understood, I won't run it. The debounce change is in place in `resize.ts`; tell me how you'd like it verified instead."

// untitled is a new thread's title, until its first message names it.
const untitled = "New thread"

// startTurn has the thread go to Working, out of the snoozed and settled
// ones, as the agent's turn begins.
func (t *thread) startTurn() {
	t.status, t.age, t.settled, t.snoozed = statusWorking, "now", false, ""
}

// stopTurn ends the agent's turn where it is: the thread is the user's
// again, the work log says it was stopped, and a reply half streamed
// stands as it is.
func (t *thread) stopTurn() {
	t.turn.Add(1)
	t.status = statusIdle
	for i := range t.entries {
		e := &t.entries[i]
		if e.kind == entryWork && e.took == "Working…" {
			e.took = "Stopped"
		}
		e.streaming = false
	}
}

// finish ends the agent's turn: the thread comes back to the inbox,
// unread until it's opened.
func (t *thread) finish() {
	t.status, t.unread = statusCompleted, true
}

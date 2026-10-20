package main

import (
	"sync/atomic"

	"github.com/egoist/mygo/ui"
)

// Mock data: the prototype has no backend, so every project, thread and
// message here is canned.

type status int

const (
	statusIdle status = iota
	statusWorking
	statusCompleted
	statusApproval
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
	// stop asks the pretend agent's goroutine to stop.
	stop atomic.Bool
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
)

type workItem struct {
	kind   string // "command", "edit", "read", "search"
	label  string
	detail string
	add    int
	del    int
}

type entry struct {
	kind      entryKind
	text      string
	streaming bool
	took      string
	work      []workItem
	files     []fileChange
}

type fileChange struct {
	path string
	add  int
	del  int
}

type diffLine struct {
	kind byte // ' ', '+', '-', '@'
	text string
}

var sampleDiff = []diffLine{
	{'@', "@@ -42,9 +42,14 @@ export function Sidebar({ projects }: SidebarProps) {"},
	{' ', "  const threads = useThreads(project.id);"},
	{' ', "  const [expanded, setExpanded] = useState(true);"},
	{'-', "  const sorted = threads.sort((a, b) => b.updatedAt - a.updatedAt);"},
	{'+', "  const sorted = useMemo("},
	{'+', "    () => [...threads].sort((a, b) => b.updatedAt - a.updatedAt),"},
	{'+', "    [threads],"},
	{'+', "  );"},
	{' ', ""},
	{' ', "  return ("},
	{' ', "    <SidebarGroup>"},
	{'-', "      <SidebarGroupLabel>{project.name}</SidebarGroupLabel>"},
	{'+', "      <SidebarGroupLabel onClick={() => setExpanded(!expanded)}>"},
	{'+', "        <ChevronIcon open={expanded} />"},
	{'+', "        {project.name}"},
	{'+', "      </SidebarGroupLabel>"},
	{' ', "      {expanded && sorted.map((t) => ("},
	{' ', "        <ThreadRow key={t.id} thread={t} />"},
	{' ', "      ))}"},
}

func mockProjects() []*project {
	active := &thread{
		id:     "t1",
		title:  "Sort sidebar threads by recent activity",
		age:    "now",
		status: statusWorking,
		branch: "t3code/sidebar-sort",
		entries: []entry{
			{kind: entryUser, text: "The sidebar threads are in creation order. Sort them by most recent activity, and let me collapse a project by clicking its name."},
			{kind: entryWork, took: "Worked for 38s", work: []workItem{
				{kind: "search", label: "Searched", detail: "useThreads in apps/web/src"},
				{kind: "read", label: "Read", detail: "apps/web/src/components/Sidebar.tsx"},
				{kind: "read", label: "Read", detail: "apps/web/src/hooks/useThreads.ts"},
				{kind: "edit", label: "Edited", detail: "apps/web/src/components/Sidebar.tsx", add: 9, del: 2},
				{kind: "edit", label: "Edited", detail: "apps/web/src/components/ChevronIcon.tsx", add: 14},
				{kind: "command", label: "Ran", detail: "bun run typecheck"},
				{kind: "command", label: "Ran", detail: "bun run test -- Sidebar"},
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
				{kind: "edit", label: "Editing", detail: "apps/web/src/components/Sidebar.tsx", add: 6, del: 1},
			}},
		},
	}
	return []*project{
		{id: "p1", name: "t3code", path: "~/Projects/t3code", open: true, icon: icTerminal, color: ui.Hex("#8b5cf6"), threads: []*thread{
			active,
			{id: "t2", title: "Fix terminal resize flicker on Windows", age: "2h", status: statusApproval, branch: "main"},
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

func mockArchived() []*thread {
	return []*thread{
		{id: "a1", title: "Try Tauri for the desktop shell", age: "3w", branch: "main"},
		{id: "a2", title: "Spike: SQLite event store", age: "1mo", branch: "main"},
		{id: "a3", title: "Rename provider adapters", age: "2mo", branch: "main"},
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
			{kind: "command", label: "Ran", detail: "bun run typecheck"},
		},
		reply: "Done. The collapsed projects are kept in `localStorage` under `t3code:sidebar:collapsed`, as a list of project ids.\n\n" +
			"- `useCollapsedProjects()` reads it once and writes it back on every toggle.\n" +
			"- A project removed from the list is dropped from the stored set the next time it's written, so it can't grow forever.\n\n" +
			"> This is a **prototype**: nothing ran. The steps above and this reply are canned, streamed in to show how it would feel.",
	},
	{
		steps: []workItem{
			{kind: "read", label: "Read", detail: "apps/server/src/terminal/pty.ts"},
			{kind: "command", label: "Ran", detail: "rg -n \"resize\" apps/server/src"},
			{kind: "edit", label: "Edited", detail: "apps/server/src/terminal/pty.ts", add: 11, del: 4},
		},
		reply: "The flicker comes from resizing the PTY on every `ResizeObserver` tick. I debounced it to one resize per animation frame:\n\n" +
			"```ts\nconst resize = rafThrottle((cols: number, rows: number) => {\n  pty.resize(cols, rows);\n});\n```\n\n" +
			"| Before | After |\n|---|---|\n| ~40 resizes per drag | 1 per frame |\n",
	},
}

// finish ends the agent's turn: the thread comes back to the inbox,
// unread until it's opened.
func (t *thread) finish() {
	t.status, t.unread = statusCompleted, true
}

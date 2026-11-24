// Glitched: T3 Code, rebuilt in MyGo's native UI: the projects and threads
// sidebar, the chat with its work log, the composer, the diff panel, a
// command palette and the settings. Its threads are those of the engine on
// this machine, which it embeds when none runs, and of the servers it is
// paired with.
package main

import (
	"context"
	"fmt"
	"log"
	"maps"
	"math"
	"math/rand/v2"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/glass"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
	"github.com/egoist/mygo/ui/markdown"
	"github.com/egoist/mygo/ui/markdown/parser"

	"glitched/internal/api"
	"glitched/internal/browse/sysweb"
	"glitched/internal/client"
	"glitched/internal/daemon"
	eng "glitched/internal/engine"
	"glitched/internal/mcp"
	"glitched/internal/plugins"
	"glitched/internal/plugins/pack"
	"glitched/internal/transport"
	"glitched/wind"
)

type colors struct {
	bg, sidebar, surface, hover, active, border, text, muted, faint ui.Color
	accent, green, red, amber, blue                                 ui.Color
}

// theme is one of the app's looks: its colors, and the terminal's, a
// theme of Ghostty's where the theme has a matching one.
type theme struct {
	name   string
	dark   bool
	colors colors
	term   string
}

// The two looks the system's appearance picks between.
var (
	lightTheme = theme{"Light", false, lightColors, ""}
	darkTheme  = theme{"Dark", true, darkColors, ""}
)

// themes are the looks the settings choose from, after System.
var themes = []theme{
	lightTheme,
	darkTheme,
	{"Mocha", true, mochaColors, "Catppuccin Mocha"},
	{"Dawn", false, dawnColors, "Rose Pine Dawn"},
}

// systemTheme is the choice that follows the system's appearance.
const systemTheme = "System"

// themeNames are the settings' choices: the system's appearance, then
// each theme.
func (a *app) themeNames() []string {
	names := []string{systemTheme}
	for _, t := range slices.Concat(themes, a.pluginThemes) {
		names = append(names, t.name)
	}
	return names
}

// readPluginThemes reads the running plugins' themes: one whose name the
// app or an earlier plugin took is left out.
func (a *app) readPluginThemes() []theme {
	var out []theme
	for _, t := range a.plugins.Themes() {
		if slices.ContainsFunc(slices.Concat(themes, out), func(th theme) bool { return th.name == t.Name }) || t.Name == systemTheme {
			continue
		}
		hex := func(k string) ui.Color { return ui.Hex(t.Colors[k]) }
		out = append(out, theme{t.Name, t.Dark, colors{
			bg: hex("bg"), sidebar: hex("sidebar"), surface: hex("surface"), hover: hex("hover"), active: hex("active"),
			border: hex("border"), text: hex("text"), muted: hex("muted"), faint: hex("faint"),
			green: hex("green"), red: hex("red"), amber: hex("amber"), blue: hex("blue"),
		}, t.Terminal})
	}
	return out
}

// forgetPluginTerminals drops the terminal colors read for plugins'
// themes, which a plugin's change may have changed.
func (a *app) forgetPluginTerminals() {
	for _, t := range a.pluginThemes {
		delete(terminalThemes, t.name)
	}
}

// themeFor is the theme a settings choice names; System, or a name
// that is no theme's, is the system's appearance, Dark or Light.
func (a *app) themeFor(name string, systemDark bool) theme {
	for _, t := range slices.Concat(themes, a.pluginThemes) {
		if t.name == name {
			return t
		}
	}
	if systemDark {
		return darkTheme
	}
	return lightTheme
}

var (
	darkColors = colors{
		bg:      ui.Hex("#0c0c0e"),
		sidebar: ui.Hex("#111113"),
		surface: ui.Hex("#18181b"),
		hover:   ui.RGBA(255, 255, 255, 0.05),
		active:  ui.RGBA(255, 255, 255, 0.09),
		border:  ui.RGBA(255, 255, 255, 0.08),
		text:    ui.Hex("#ececf0"),
		muted:   ui.Hex("#8e8e98"),
		faint:   ui.Hex("#85858d"),
		green:   ui.Hex("#4ade80"),
		red:     ui.Hex("#f87171"),
		amber:   ui.Hex("#fbbf24"),
		blue:    ui.Hex("#60a5fa"),
	}
	lightColors = colors{
		bg:      ui.Hex("#ffffff"),
		sidebar: ui.Hex("#f6f6f7"),
		surface: ui.Hex("#ffffff"),
		hover:   ui.RGBA(0, 0, 0, 0.04),
		active:  ui.RGBA(0, 0, 0, 0.07),
		border:  ui.RGBA(0, 0, 0, 0.09),
		text:    ui.Hex("#18181b"),
		muted:   ui.Hex("#64646d"),
		faint:   ui.Hex("#6f6f78"),
		green:   ui.Hex("#16a34a"),
		red:     ui.Hex("#dc2626"),
		amber:   ui.Hex("#d97706"),
		blue:    ui.Hex("#2563eb"),
	}
	// mochaColors are Catppuccin Mocha's.
	mochaColors = colors{
		bg:      ui.Hex("#1e1e2e"),
		sidebar: ui.Hex("#181825"),
		surface: ui.Hex("#262637"),
		hover:   ui.RGBA(205, 214, 244, 0.06),
		active:  ui.RGBA(205, 214, 244, 0.1),
		border:  ui.RGBA(205, 214, 244, 0.1),
		text:    ui.Hex("#cdd6f4"),
		muted:   ui.Hex("#a6adc8"),
		faint:   ui.Hex("#9399b2"),
		green:   ui.Hex("#a6e3a1"),
		red:     ui.Hex("#f38ba8"),
		amber:   ui.Hex("#f9e2af"),
		blue:    ui.Hex("#89b4fa"),
	}
	// dawnColors are Rosé Pine Dawn's.
	dawnColors = colors{
		bg:      ui.Hex("#faf4ed"),
		sidebar: ui.Hex("#f2e9e1"),
		surface: ui.Hex("#fffaf3"),
		hover:   ui.RGBA(87, 82, 121, 0.06),
		active:  ui.RGBA(87, 82, 121, 0.1),
		border:  ui.RGBA(87, 82, 121, 0.12),
		text:    ui.Hex("#575279"),
		muted:   ui.Hex("#605c78"),
		faint:   ui.Hex("#66627e"),
		green:   ui.Hex("#286983"),
		red:     ui.Hex("#b4637a"),
		amber:   ui.Hex("#ea9d34"),
		blue:    ui.Hex("#56949f"),
	}
	accents = []struct {
		name  string
		color ui.Color
	}{
		{"Violet", ui.Hex("#8b7cf6")},
		{"Blue", ui.Hex("#3b82f6")},
		{"Teal", ui.Hex("#14b8a6")},
		{"Orange", ui.Hex("#f97316")},
		{"Pink", ui.Hex("#ec4899")},
	}
	// palette is the colors of the frame being built, set from the
	// settings at the start of each.
	palette = darkColors
)

// settings is what the settings pages edit. Most of it is only shown.
type settings struct {
	theme         string // a theme's name, or systemTheme
	accent        int
	glass         bool
	vibrancy      float64 // 0 to 1: how much of the sidebar's vibrancy shows through the background
	tintPanels    bool    // the floating panels take the glass tint too, not only the window's bars
	wind          bool    // the wind's field of glyphs behind the chat
	fontSize      float64
	timeFormat    string
	confirmDelete bool
	streaming     bool
	newThreadMode int // Local, Worktree
	notifications bool
	checkCLIs     bool // ask each machine's engine whether its CLIs have newer versions

	checking bool
}

type app struct {
	// post runs a func on the thread that builds the views, as the
	// manager's changes do.
	post    func(func())
	win     *mygo.Window
	router  *ui.Router
	backend *client.Manager
	// projects are the backend's threads by project, as the frame being
	// built shows them.
	projects []*project
	selected api.ID // zero: the draft
	fresh    draft
	worktree worktreeChoice  // where the next new thread runs
	repos    map[string]repo // what git said of the folders a new thread could start in
	opened   opened
	// kids are the threads by parent, as the frame being built shows them;
	// childrenOpen the threads whose children the sidebar shows.
	kids         map[api.ID][]thread
	childrenOpen map[api.ID]bool
	// ended is each thread's last turn's end, as last seen, to tell of
	// the next once.
	ended map[api.ID]time.Time
	// questions are the picks on the question cards that wait.
	questions map[api.ID]*questionForm
	// credentials are the values typed on the credentials cards that wait,
	// until they go to the engine.
	credentials map[api.ID]*credentialsForm
	// forgotten are the threads whose credentials were forgotten from here.
	forgotten map[api.ID]bool
	// notify shows a desktop notification: nil until the window is.
	notify   func(mygo.NotificationOptions)
	settings settings
	// savedSettings are the settings as last saved to settings.json, or
	// read from it; settingsOut writes it (settingsfile.go).
	savedSettings settingsFile
	// pendingTheme is a plugin's theme settings.json chose, which the app
	// started without: it shows once the plugin runs, unless the user
	// chose one meanwhile.
	pendingTheme string
	settingsOut  settingsWriter
	// theme is the frame's look, as the settings and the system choose it.
	theme theme
	// pluginThemes are the looks this app's plugins add, after the app's,
	// as its frame last read them.
	pluginThemes []theme
	// server is the Servers page's form for adding one, why the last add
	// failed, and whether one is waiting for its server to answer.
	server    client.Server
	serverErr string
	adding    bool
	// pairLabel is the This machine page's label for the next token;
	// paired, the token it minted last, shown until dismissed.
	pairLabel  string
	paired     pairing
	machineErr string
	// control is the This machine page's draft of the control server.
	control string
	// limits are the Agents card's inputs, as read from limitsOf.
	limits   [3]float64
	limitsOf eng.Limits

	showSidebar  bool
	showDiff     bool
	showTerminal bool
	sidebarWidth float32
	// sidebarShown is the sidebar's width as this frame draws it: as
	// dragged, but never leaving the chat less than its least width.
	sidebarShown float32
	// windowW is the window's width, as this frame lays it out.
	windowW float32
	// sidebarOpen is how far the sidebar has slid in this frame, from 0
	// to 1; sidebarJump skips the next slide, for ⌘B.
	sidebarOpen float32
	// sidebarTall is how tall the sidebar was as last laid out, or the
	// window, before it was.
	sidebarTall float32
	// sidebarRows are the threads in the rows of each part of the
	// sidebar's body, as last built.
	sidebarRows map[string]map[api.ID]bool
	// sidebarPages are how many pages of threads the user had a section,
	// by its id, or a thread's children, by the thread's, show; one
	// without has one. sidebarKept are the threads shown wherever they
	// sort, past the pages: those the user chose, and those that moved
	// section.
	sidebarPages map[string]int
	sidebarKept  map[api.ID]bool
	// sectionCounts are each section's threads, as the sidebar's body last
	// counted them, for the headers its plugin places.
	sectionCounts map[category]int
	sidebarJump   bool
	// sidebarAsked is whether the user asked for the sidebar since the last
	// frame, which makes room for it at its own size.
	sidebarAsked bool
	// vibrant is whether the sidebar shows the window's vibrancy.
	vibrant bool
	// panelsJump shows the diff panel and the terminal without motion,
	// as when ⌘D and ⌘J toggle them.
	panelsJump bool

	draft string
	// code is the draft's pieces once it holds code blocks, which ``` and
	// Enter open: the draft is then "" (codeblock.go).
	code codeBlock
	// panes are what the draft's text areas held as last laid out, by the
	// id of the piece, 0 for the draft without blocks, and codeSeq the
	// last id given. codeFocus is where the focus goes next among them,
	// codeLast the one that had it last (focusBack), and codeCarry a text
	// area the focus left as it went (codeblock.go).
	panes     map[int]*paneState
	codeSeq   int
	codeFocus codeFocus
	codeLast  int
	codeCarry codeCarry
	// codeScroll is where the draft holding blocks is scrolled, and
	// codeReveal how many frames more it follows the caret.
	codeScroll  ui.ScrollState
	codeReveal  int
	codeScrollW float32 // the scroll's width as last laid out
	// fenceSaw is the draft as typedFence last saw it.
	fenceSaw string
	typed    string // the draft the wind last saw: typing keeps it blowing
	wind     *wind.Texture
	// The model picker: open, fast mode, and when fast mode came on and
	// effort went Max, for their effects.
	pickerOpen   bool
	pickerFamily string
	pickerReveal pickerReveal
	fast         bool
	fastAt       time.Time
	// The effort slider's drag: held, where it was pressed and last held,
	// in levels, to tell a click from a drag, and the level the held
	// thumb last locked onto, -1 before any, which it chooses once.
	// effortPressed is set while the thumb is pressed, as the last frame
	// built it: a slider not built, as its picker closed mid-drag, sees
	// no release, so the next frame ends the hold (view). Left held, it
	// would keep the default unsaved, and the next picker opened would
	// take it for a click let go there.
	effortHeld, effortPressed   bool
	effortPressAt, effortLastAt float32
	effortLock                  int
	// effortIDs are the efforts the slider last showed: a press held as
	// they change ends, its levels being the old ones', and effortVoid
	// is set till it's let go.
	effortIDs  string
	effortVoid bool
	maxAt      time.Time
	model      modelKey
	followsCLI bool // the default's model is its provider's program's own (choice)
	// cliDefaults are the models each provider's program runs where given
	// none in a workspace, by provider and workspace, "" where it can't
	// say (draftDefaults); cliAsking those being asked, and defaultsIn the
	// workspace of the draft, "" for none or another machine's; cliEpoch
	// counts the times they were cleared, so an answer to an ask before
	// is dropped.
	cliDefaults map[[2]string]string
	cliAsking   map[[2]string]bool
	// guessed is the model and effort the picker expected each thread
	// sent from here to run, by its id, where it left the model to its
	// program: the thread shows them till the program reports its own.
	guessed    map[api.ID]choice
	defaultsIn string
	cliEpoch   int
	effort     string
	access     string // one of accessLevels
	accessOpen bool
	accessAt   time.Time // when the level last changed, for its effect
	// savedChoice is the default for a new thread as composer.json last
	// gave it, triedChoice the one last saved or tried (saveComposer),
	// and pickerLock the only family the model picker offers, a thread's
	// provider's, or "" for all.
	savedChoice choice
	triedChoice choice
	// composerFile is what composer.json held as it was last read or
	// saved, which the default is taken from again as the engines'
	// models come (askModels).
	composerFile choice
	pickerLock   string
	accessFor    api.ID // the thread whose access changed at accessAt, zero for the draft
	pickersFor   api.ID // the thread whose composer the pickers are in, zero for the draft (pickersIn)
	collapsed    map[string]bool
	// The inbox: the project it's filtered to, and its closed sections.
	projectFilter map[projectKey]bool // the projects the sidebar shows; none is all
	sectionClosed map[category]bool
	// rowFx is the joke playing on a thread's row, by thread.
	rowFx map[api.ID]rowGagFx
	// attachments go with the next message; pastes counts pasted images,
	// to name them.
	attachments []attachment
	// draftToEnd puts the composer's caret at the end of the draft as its
	// text area next builds, after a draft set from outside it
	// (attachQuote).
	draftToEnd bool
	// quoteCol is the column of the reply whose selection has a bar open,
	// as the pass that built its row bound it (followSelection).
	quoteCol ui.Handle
	// quote is the bar of text selected in a reply (quote.go).
	quote  quoteBar
	pastes int
	// The composer's autocomplete: the token it's for, whether it was
	// dismissed, its suggestions and choice, its flyout, and the projects'
	// files it suggests.
	suggestFor       string
	suggestDismissed bool
	suggestions      []suggestion
	suggestIndex     int
	suggestFly       *mygo.Window
	suggestBox       [5]int // the flyout's anchor and height, as last set
	suggestList      ui.ListState
	files            map[string][]string
	// slashLists are the providers' commands / suggests, by provider and
	// workspace.
	slashLists map[slashKey]*slashList
	// models are the models each family's engine reports (models.go);
	// pickerAt is the model button's place, and effortBox the picker's
	// effort, which the popover's height leaves room for.
	models    modelLists
	pickerAt  ui.Rect
	effortBox ui.Handle
	// The preview pane, a browser: shown, its tabs and the one shown, and
	// why its web views could not start (see browser.go).
	showPreview bool
	browser     browser
	// previewDragged is the preview's width as its edge was dragged, 0
	// until it is, and again after a double click on it.
	previewDragged float32
	// sidebarHeld and previewHeld are set while the sidebar's or the
	// preview's edge is dragged, from its first move until it is let go:
	// the chat follows it without easing. sidebarPressed and
	// previewPressed are set while the edge is pressed, as the last frame
	// built it: an edge not built, as its pane closed mid-drag, ends no
	// drag, so the next frame ends it (view), and holds nothing.
	sidebarHeld, previewHeld       bool
	sidebarPressed, previewPressed bool
	// dir is the state directory, which holds the layout (layout.go).
	dir string
	// terminalFocused is set while the terminal drawer has the keyboard,
	// as the last frame built it; it keeps the browser's keys then.
	terminalFocused bool
	// covered is whether the last pass showed one of the app's overlays,
	// which covering notes as this one builds them (overlay.go).
	covered, covering bool
	// modal is whether the last pass built showed a dialog other than the
	// palette, which holds the window's keys, ⌘K's too; modalNow notes
	// one as this pass builds it.
	modal, modalNow bool
	// covers are what the app draws over the timeline, by key, and coverN
	// how many this pass bound; over is where the frame on screen drew
	// them; and blind is whether the last pass showed one at a place the
	// app does not know, which blinding notes as this one builds them
	// (overlay.go).
	covers          map[coverKey]*ui.Handle
	coverN          int
	over            []ui.Rect
	blind, blinding bool
	// zap is a request's answers just answered, breaking up as they go;
	// answerAt where the waiting request's answers were last laid out.
	zap      approvalZap
	answerAt answerRow
	// drawn is where the last frame drew each waiting card this frame's
	// timeline built, and drew the same for the frame before; laidAt is
	// when the last frame drew, as this frame's timeline found it, and
	// builtAt when this frame's timeline was built; keyed is the request
	// ⌘⏎ and ⇧⌘D answer, as the last frame found it, and keyedSince when
	// a frame first showed it so, zero until one did.
	drawn, drew     map[api.ID]sight
	laidAt, builtAt time.Time
	keyed           api.ID
	keyedSince      time.Time
	// reads are what the window showed of each waiting card; chord is
	// when ⌘ went down, zero while it is up; and pressing is the press of
	// a request's answers the pointer holds.
	reads    map[api.ID]*reading
	chord    time.Time
	pressing pressAt
	// quick is the quick-ask panel over every app.
	quick quickAsk
	// quitting is set as a quit starts, for the windows that hide rather
	// than close to let it close them.
	quitting bool
	// nextTurn runs a func on the main loop's next turn; nil in tests.
	nextTurn func(func())
	// leavingFullScreen, while the window leaves full screen to hide on
	// macOS, drops that hide: its listener off, its fallback disarmed.
	leavingFullScreen func()
	// lists are the threads' timelines, by thread.
	lists map[api.ID]*ui.ListState
	// markers are, by thread, what its turn markers keep from frame to
	// frame.
	markers     map[api.ID]*markerState
	markerTicks markerTicks
	// rows are the threads' timelines' rows, by thread, as rowsOf
	// derives them; posts counts the changes posted to the window, after
	// which they are derived again.
	rows  map[api.ID]*rowsCache
	posts uint64
	// recentThreads are the threads, the most recently updated first.
	recentThreads recentThreads
	// sorted are the threads in the sidebar's sections, as sortThreads
	// last sorted them. filterEdits and fxEdits count the changes to the
	// project filter and the rows' jokes, which move threads too.
	sorted               sortedThreads
	filterEdits, fxEdits uint64
	// added are the folders added as projects on this machine, as
	// projects.json holds them; addedEdits counts their changes, which
	// the sidebar's projects follow.
	added      []addedProject
	addedEdits uint64
	// lastProject is the folder of the project the last thread on each
	// machine was started in, by machineKey, as last-project.json holds
	// it.
	lastProject map[string]string
	// The diff panel: each file's viewer, by thread and path, and their
	// layout.
	diffs      map[diffKey]*fileDiff
	reading    map[diffKey]diffRead // files being read for diffs
	readGen    uint64
	diffLayout editor.DiffLayout
	// diffWrap wraps the diff's long lines.
	diffWrap bool
	// diffFile is the file each thread's diff shows, by path.
	diffFile map[api.ID]string
	// subagentStep is the step whose native subagent each thread's panel
	// shows, by item, while it is open.
	subagentStep map[api.ID]subagentSel
	subagentMade subagentMade
	// shells are the terminal drawer's shells, by workspace.
	shells map[string]*shell
	// windDraw paints the wind's field behind the chat, as the frame's
	// pane has it.
	windDraw func(p *ui.Painter, r ui.Rect)
	// roomBeside is the window's width beside the sidebar, and chatWidth
	// the chat's, as the last frame laid them out: the panels size to the
	// one and the header to the other.
	roomBeside, chatWidth float32
	// headerGive is what the header gives way to fit its row.
	headerGive headerGive
	// effortFlow is the light flowing through the effort bar.
	effortFlow flowState
	// composerOpened is the composer brought back to its full size by a
	// click while compact, until the end of the thread shows again.
	composerOpened bool
	// The composer's morph in this frame, for the open thread: how far
	// from compact (0) to full (1), and how far Jump to latest has come
	// in; and, as the last frames measured them, the card's compact and
	// full heights, and the row below it.
	composerK, jumpK              float32
	cardCompactH, cardFullH, envH float32
	// cardH is the card's height as the last frame of a morph set it.
	cardH float32
	// cardFor is the look the card's heights were measured in, cardLaid
	// the one it was laid out in the frame before.
	cardFor, cardLaid cardLook
	// composerRest is the composer at rest, as it was the frame before;
	// composerSeen the threads it has been shown over.
	composerRest bool
	composerSeen map[api.ID]bool
	// The hero (hero.go): dockK is how far the composer is from the hero
	// (0) to its dock (1) in this frame, dockJump makes it jump there, for
	// ⌘N, and paneAt is the chat pane, as last laid out; leaving
	// is the thread last left for a hero, whose transcript fades out as
	// the composer lifts, and arrived the last to leave the hero, at
	// arrivedAt, whose row slides into the sidebar.
	dockK     float32
	dockJump  bool
	paneAt    ui.Rect
	leaving   api.ID
	arrived   api.ID
	arrivedAt time.Time
	// aboveH is the height of what's over the composer card, the agents
	// strip, as last measured.
	aboveH float32
	// composerH is the composer's frame, the card with what's over and
	// under it, which a draft grows over the timeline; composerAt is where
	// the frame on screen drew it (lookOver).
	composerH  ui.Handle
	composerAt ui.Rect
	// composerIn draws composer.input in this frame's composer, for the
	// plugin that places it; composerAround is the room the plugin's
	// layout takes around it, as last laid out, zero in Glitched's own.
	composerIn     func(*ui.Context)
	composerAround struct{ w, h float32 }
	// The agents strip: over whose composer the panel is open, and showing
	// all its rows past five; in this frame, how far from pill (0) to panel
	// (1), the panel's height as it heads for its rows', and the pill's
	// width as its label last measured; and the thread it was last shown
	// for, moving to another closing it.
	agentsOpen, agentsAll         map[api.ID]bool
	agentsK, agentsH, agentsPillW float32
	agentsFor                     api.ID
	// shownAll are the texts of requests' cards shown all, not cut.
	shownAll map[cardText]bool
	// draftArea is the full composer's text area, and draftLine the
	// compact composer's line.
	draftArea, draftLine ui.Handle
	// chosen is the thread a person chose, until its composer, at rest,
	// has the focus, and asked the editor its focus was asked for.
	chosen api.ID
	asked  *ui.Handle
	// revealing is the thread opened from the palette, or by a plugin,
	// till its sidebar row has scrolled into view.
	revealing api.ID
	// steers are the messages sent from the composer, until each lands
	// or fails, by command.
	steers map[api.ID]steer
	// continues are the threads Continue all continues, in order.
	continues []continuing
	// queueEditing is the queued message being edited in its row, and
	// queueArea the area it's edited in.
	queueEditing queueEdit
	queueArea    ui.Handle
	// queueRefocus is the queued message whose Edit takes the focus back
	// as its edit ends, and queueEditButton that Edit.
	queueRefocus    api.ID
	queueEditButton ui.Handle
	// queueH is the queue's height over the composer, as last laid out.
	queueH float32
	// queueOrder is the queue's messages as last drawn.
	queueOrder []api.ID
	// snoozeFor is the thread whose snooze popover is open; popoverOpen
	// says a plugin's popover drew open, as its row's actions are drawn.
	snoozeFor   api.ID
	popoverOpen bool
	// snoozeWake is when the next snoozed thread comes back.
	snoozeWake time.Time
	// renaming is the thread whose title is being edited in its row, and
	// renameDraft the title as typed; renameFocused once the input has
	// had the focus, so that losing it ends the edit.
	renaming      api.ID
	renameDraft   string
	renameFocused bool
	// deleting is the thread the alert asks about deleting.
	deleting   thread
	deleteOpen bool
	// reverting is the revert of files the alert asks about.
	reverting  revertAsk
	revertOpen bool
	// deleteChanges are the changes git doesn't ignore in the worktrees
	// the alert's delete would remove, -1 where none can tell, and
	// deleteCounted those worktrees' threads.
	deleteChanges int
	deleteCounted []api.ID
	// removing is the project the alert asks about removing, by key, so
	// that it removes the threads the project has as it's confirmed.
	removing   projectKey
	removeOpen bool
	// picking is while a folder picker is open: the New project wizard's
	// Existing folder or its Choose….
	picking bool
	// wizard is the New project wizard (wizard.go).
	wizard wizard
	// now is the frame's time, as the view builds it.
	now time.Time
	// clock and zone are what a message's time is told against: the day
	// it is, and where. Nil: the frame's time, and the system's zone.
	clock func() time.Time
	zone  *time.Location
	// stamps are messages' times as their toolbars write them.
	stamps map[stampKey][2]string

	cmdkOpen  bool
	cmdkQuery string
	cmdkIndex int
	// cmdkOpens counts the palette's openings, each a copy of its own of
	// the palette component, which none of the last one's state shows in.
	cmdkOpens int
	cmdkList  ui.ListState

	glassOffset ui.Point
	checkUntil  time.Time

	md markdown.Options
	// userMD is how the user's messages read: as replies do, their code
	// wrapped to the bubble, without math, which reads escaped brackets,
	// or autolinks, which read escaped addresses, and with links, should
	// any read, opening nothing. Strikethrough only stands in for none:
	// no extensions read as the default ones.
	userMD markdown.Options
	// userMDs are the user's messages as they show, by their entries'
	// keys.
	userMDs map[string]userMD

	// plugins are the plugins installed in the state directory, and
	// pluginPages what their pages show.
	plugins        *plugins.Host
	pluginPages    map[pluginKey]*pluginPage
	pluginControls map[pluginKey]*sessionControls
	pluginMD       markdown.Options
	// pluginLater are what plugins asked for that takes a frame: toasts
	// and the clipboard.
	pluginLater []func(c *ui.Context)
	// placeFrame is what this frame drew of the plugins' places, and
	// placeControls their inputs' values.
	placeFrame    placeFrame
	placeControls map[placeKey]*sessionControls
	// partsDrawn is who drew each component's copies, as the last frame
	// that drew it did, for Settings to say.
	partsDrawn map[string]*componentDrawn
	// frames counts the frames that drew plugins' places; scopeFrames is
	// the last that drew a thread's components, and the app's own.
	frames      int
	scopeFrames [2]int
	// pluginRecords are what the plugins were last told of the threads,
	// and pluginSections the sidebar section each was last in.
	pluginRecords  map[string]string
	pluginSections map[string]string
	// pluginMessages are the messages the plugins were told of, by thread
	// and key.
	pluginMessages map[string]*toldMessages
	// pluginFiles are the thread's changed files the plugins were told
	// of, by thread id, with each one's last edit.
	pluginFiles map[string]*toldFiles
	// showPluginTab shows pluginTabShown in the panel beside the chat.
	showPluginTab  bool
	pluginTabShown pluginTab
	pluginDark     *bool
	// pluginFocused is whether the window was in front, as plugins last
	// heard it; nil before the first frame.
	pluginFocused *bool
	// skills are what each thread's engine last said of its skills, for
	// its header.
	skills map[api.ID]*skillsRead
	// openFolder, when set, chooses a folder in place of the system's
	// dialog, as in a test.
	openFolder func() string
	stopWatch  func()
	// pluginAssets are the plugins' pictures, by plugin and file.
	pluginAssets map[string]asset
	// services reaches the plugins' services in this machine's engine.
	serviceMu sync.Mutex
	services  *transport.EndpointClient
	// pluginSource is the install field's; pluginBusy is set while an
	// install, an update or a check runs; pluginUpdates are what the last
	// check found.
	// Settings › Plugins as the user left it: pluginsView is the plugins
	// or the parts of Glitched; pluginsQuery the filter's words;
	// pluginsOpen the rows opened or closed, by folder; pluginsReveal the
	// folder of a row to scroll to once.
	pluginsView int
	// providersOpen are Settings › Providers' rows opened, by provider.
	providersOpen map[string]bool
	launches      launchState
	// clis are the remote machines' providers, and the CLI updates the
	// user ran on any machine.
	clis cliLists
	// updateRunner, when set, runs a CLI's update in the engine's place,
	// as a test has it.
	updateRunner  func(e *client.Env, name string, whenIdle bool, done func(transport.UpdateResult, error)) (cancel func())
	pluginsQuery  string
	pluginsOpen   map[string]bool
	pluginsReveal string
	pluginSource  string
	pluginBusy    bool
	pluginUpdates []pack.Found
	pluginChecked bool
	// settingDrafts are text settings as typed, by plugin and setting,
	// until saved.
	settingDrafts map[string]string

	// stopAgents stops serving agents the browser (serveAgents).
	stopAgents func() error
}

// newApp is the app on the engines its state directory dir names. The
// manager posts its changes through post, which runs them on the thread
// that builds the views. An engine it embeds serves as engine has it, or
// with the fake for every provider when engine is nil, as in a test.
func newApp(dir string, engine func(*daemon.Config), detect func(context.Context) (api.Tailnet, error), post func(func())) *app {
	a := &app{
		dir:            dir,
		router:         newRouter(),
		wind:           wind.New(rand.Uint32(), nil),
		ended:          map[api.ID]time.Time{},
		questions:      map[api.ID]*questionForm{},
		credentials:    map[api.ID]*credentialsForm{},
		forgotten:      map[api.ID]bool{},
		showSidebar:    true,
		sidebarWidth:   272,
		collapsed:      map[string]bool{},
		sectionClosed:  map[category]bool{catWorking: true, catSnoozed: true, catSettled: true},
		sidebarRows:    map[string]map[api.ID]bool{},
		sidebarPages:   map[string]int{},
		sidebarKept:    map[api.ID]bool{},
		sectionCounts:  map[category]int{},
		projectFilter:  map[projectKey]bool{},
		rowFx:          map[api.ID]rowGagFx{},
		shells:         map[string]*shell{},
		diffs:          map[diffKey]*fileDiff{},
		reading:        map[diffKey]diffRead{},
		steers:         map[api.ID]steer{},
		post:           post,
		lists:          map[api.ID]*ui.ListState{},
		markers:        map[api.ID]*markerState{},
		rows:           map[api.ID]*rowsCache{},
		files:          map[string][]string{},
		slashLists:     map[slashKey]*slashList{},
		diffLayout:     editor.DiffUnified,
		diffWrap:       true,
		diffFile:       map[api.ID]string{},
		subagentStep:   map[api.ID]subagentSel{},
		childrenOpen:   map[api.ID]bool{},
		composerSeen:   map[api.ID]bool{},
		agentsOpen:     map[api.ID]bool{},
		agentsAll:      map[api.ID]bool{},
		shownAll:       map[cardText]bool{},
		drawn:          map[api.ID]sight{},
		reads:          map[api.ID]*reading{},
		covers:         map[coverKey]*ui.Handle{},
		pluginPages:    map[pluginKey]*pluginPage{},
		pluginControls: map[pluginKey]*sessionControls{},
		placeControls:  map[placeKey]*sessionControls{},
		pluginAssets:   map[string]asset{},
		settingDrafts:  map[string]string{},
		pluginsOpen:    map[string]bool{},
		providersOpen:  map[string]bool{},
		clis:           newCLILists(),
		cliDefaults:    map[[2]string]string{},
		cliAsking:      map[[2]string]bool{},
		guessed:        map[api.ID]choice{},
		pluginRecords:  map[string]string{},
		pluginSections: map[string]string{},
		pluginMessages: map[string]*toldMessages{},
		pluginFiles:    map[string]*toldFiles{},
		settings: settings{
			theme:         systemTheme,
			glass:         true,
			vibrancy:      1, // untinted: Liquid Glass has no color of its own
			wind:          true,
			fontSize:      13,
			timeFormat:    "Relative",
			confirmDelete: true,
			streaming:     true,
			notifications: true,
			checkCLIs:     true,
		},
	}
	a.userMD.Extensions, a.userMD.WrapCode = parser.ExtStrikethrough, true
	a.userMD.OnLink = func(string) {}
	a.loadLayout()
	a.loadModels()
	a.loadComposer()
	a.loadProjects()
	a.loadLastProject()
	a.loadSettings()
	a.openPlugins(dir)
	a.backend = client.New(client.Config{Dir: dir, Post: func(fn func()) {
		post(func() {
			fn()
			a.changed()
		})
	}, Asked: a.notifyAsked, Engine: engine, Detect: detect})
	return a
}

// applyTheme sets the theme the settings ask for: the system's appearance,
// which the default choice follows, or a theme of the app's or a plugin's.
func (a *app) applyTheme(c *ui.Context) {
	a.pluginThemes = a.readPluginThemes()
	if a.pendingTheme != "" && slices.Contains(a.themeNames(), a.pendingTheme) {
		a.settings.theme, a.pendingTheme = a.pendingTheme, ""
	}
	a.theme = a.themeFor(a.settings.theme, c.Theme().Dark)
	palette = a.theme.colors
	palette.accent = accents[a.settings.accent].color

	t := *c.Theme()
	t.Dark = a.theme.dark
	t.Background = palette.bg
	t.Surface = palette.surface
	t.SurfaceHover = palette.surface.Mix(palette.text, 0.06)
	t.SurfacePressed = palette.surface.Mix(palette.text, 0.1)
	t.Border = palette.border
	t.Text = palette.text
	t.TextMuted = palette.muted
	t.Accent = palette.accent
	t.AccentHover = palette.accent.Mix(ui.Hex("#ffffff"), 0.12)
	t.AccentPressed = palette.accent.Mix(ui.Hex("#000000"), 0.12)
	t.AccentText = ui.Hex("#ffffff")
	t.Selection = palette.accent.Alpha(0.35)
	t.Focus = palette.accent.Alpha(0.6)
	t.Scrollbar = palette.text.Alpha(0.18)
	t.Inverse, t.InverseText = palette.surface.Mix(palette.text, 0.1), palette.text
	t.Radius = 8
	t.FontSize = float32(a.settings.fontSize)
	c.SetTheme(&t)
	if a.plugins != nil && (a.pluginDark == nil || *a.pluginDark != t.Dark) {
		dark, first := t.Dark, a.pluginDark == nil
		a.pluginDark = &dark
		if !first {
			appearance := map[bool]string{true: "dark", false: "light"}[dark]
			a.plugins.Emit("theme.changed", func(string) any { return map[string]string{"appearance": appearance} })
		}
	}
}

// pane is the material of a floating panel: Liquid Glass, or a plain
// surface when the glass is turned off in the settings.
//
// The glass is plain, as macOS draws its own, unless the Tint panels
// setting is on: then every pane takes the one tint the Glass tint
// setting sets, as the window's vibrancy does in the sidebar and the
// headers, and the two kinds of glass, macOS's showing the desktop and
// MyGo's what the app draws under it, read as one material.
func (a *app) pane(e ui.Element, g glass.Glass) ui.Element {
	if a.settings.glass {
		if g.Tint == (ui.Color{}) && a.settings.tintPanels {
			g.Tint = a.glassTint()
		}
		return e.Material(g)
	}
	return e.Background(palette.surface).Border(a.paneBorder(), palette.border)
}

// paneBorder is the width of the border pane gives a panel: the plain
// surface's hairline, or none for the glass.
func (a *app) paneBorder() float32 {
	if a.settings.glass {
		return 0
	}
	return 1
}

// frosted is the material of what floats over the chat: the composer, the
// popovers, the palette and the toasts. In the dark it's frosted and
// untinted, as the glass would lift black behind it to a 15% grey, with a
// hairline for its rim, a shadow that takes no room. In the light it's a
// pane.
func (a *app) frosted(c *ui.Context, e ui.Element) ui.Element {
	if c.Theme().Dark && a.settings.glass {
		return e.Material(glass.Blur{Radius: 18}).Shadow(0, 0, 0, 1, palette.border)
	}
	return a.pane(e, glass.Glass{})
}

// newRouter shows pages at once: settings sections are tabs, and their
// content should not slide.
func newRouter() *ui.Router {
	r := ui.NewRouter("/")
	r.Transition = ui.TransitionNone
	return r
}

func (a *app) view(c *ui.Context) {
	// The window's vibrancy is the system's material, in the system's
	// appearance: the sidebar shows it while the app's theme matches, and
	// the Liquid Glass setting is on.
	a.now = c.Now()
	if !a.arrived.IsZero() && a.arrivedAt.IsZero() {
		a.arrivedAt = a.now // sentFromHero
	}
	a.sidebarHeld = a.sidebarHeld && a.sidebarPressed
	if a.previewHeld && !a.previewPressed {
		a.endPreviewDrag()
	}
	if a.effortHeld && !a.effortPressed {
		a.effortHeld = false
		if a.pickersFor.IsZero() {
			a.saveComposer()
		}
	}
	a.sidebarPressed, a.previewPressed, a.effortPressed = false, false, false
	a.saveSettings()
	a.browser.letGo(a.showPreview)
	a.sortThreads()
	a.pruneDiffs()
	a.pruneThreadState()
	a.paceContinues()
	// A steer to a server removed since never lands.
	maps.DeleteFunc(a.steers, func(_ api.ID, s steer) bool { return !slices.Contains(a.backend.Envs(), s.env) })
	// Jokes over are gone, whether or not their rows show: a row in a
	// closed section isn't built to see its own end.
	for id, fx := range a.rowFx {
		if fx.done(a.now) {
			delete(a.rowFx, id)
		}
	}
	systemDark := c.Theme().Dark
	a.applyTheme(c)
	if f := c.WindowActive(); a.plugins != nil && (a.pluginFocused == nil || *a.pluginFocused != f) {
		if a.pluginFocused != nil {
			a.plugins.Emit("window.focused", func(string) any { return map[string]bool{"focused": f} })
			if f {
				a.skillsStale()
			}
		}
		a.pluginFocused = &f
	}
	a.shortcuts(c)
	// The drawer sets it again as it builds with the keyboard (chat.go);
	// a frame without it, as on another page, leaves the keys the browser's.
	a.terminalFocused = false
	a.covered, a.covering = a.covering, false
	a.blind, a.blinding = a.blinding, false
	a.coverN = 0
	// Before anything this pass builds binds its cover anew.
	a.lookOver(c)
	a.vibrant = c.Vibrancy() && a.settings.glass && c.Theme().Dark == systemDark
	if c.Vibrancy() {
		// The material shows wherever nothing paints over it.
		c.Root().Background(ui.Transparent)
	}
	root := ui.Row(c).Fill().AlignItems(ui.Stretch)
	noteGPU(root)
	w, _ := c.Size()
	a.windowW = w
	// As dragged, but capped so the chat keeps its least width; a panel
	// that still doesn't fit beside it takes its room whole (crowded).
	a.sidebarShown = a.sidebarWidth
	if w > 0 {
		a.sidebarShown = max(min(a.sidebarWidth, w-minChatWidth), minSidebarWidth)
	}
	if a.sidebarAsked {
		a.sidebarAsked = false
		a.makeRoomForSidebar()
	}
	target := float32(0)
	if a.sidebarVisible() {
		target = 1
	}
	if a.sidebarJump {
		a.sidebarOpen = root.Jump("sidebar", target)
		a.sidebarJump = false
	} else {
		a.sidebarOpen = root.AnimateTo("sidebar", target, sidebarSlide)
	}
	// Of this frame's sidebar, so that what lays itself out beside it, as
	// the header, fits the room it has in this frame.
	if w > 0 {
		a.roomBeside = w - a.sidebarShown*a.sidebarOpen
	}
	root.Children(func() {
		a.sidebarFrame(c)
		// The pages paint their own backgrounds, under their headers,
		// which show the window's vibrancy as the sidebar does.
		// The bars' tint, as the sidebar's: the headers are clear over it,
		// and it shows around the content's rounded corner.
		main := a.chrome(ui.Box(c).Grow(1).Shrink(1))
		main.Children(func() {
			ui.Column(c).Fill().Children(func() { a.pages(c) })
		})
	})
	a.commandPalette(c)
	a.deleteAlert(c)
	a.revertAlert(c)
	a.removeAlert(c)
	a.wizardDialog(c)
	for _, fn := range a.pluginLater {
		fn(c)
	}
	a.pluginLater = nil
	a.toasts(c)
	a.syncPlaces()
	a.modal, a.modalNow = a.modalNow, false
}

func (a *app) pages(c *ui.Context) {
	a.router.View(c, func(r *ui.Route) {
		switch {
		case r.Match("/settings/{section...}"):
			r.Title("Settings")
			a.settingsPage(c, r)
		case r.Match("/plugins/{plugin}/{page}"):
			a.pluginsPage(c, r)
		default:
			r.Title("Chat")
			a.chatPage(c)
		}
	}).Grow(1)
}

func (a *app) shortcuts(c *ui.Context) {
	if c.Shortcut(ui.Cmd, ui.KeyK) {
		a.openCommandPalette()
	}
	if c.Shortcut(ui.Cmd, ui.KeyN) {
		a.newThread()
		a.dockJump = true // keyboard actions don't animate
	}
	// Keyboard actions don't animate.
	if c.Shortcut(ui.Cmd, ui.KeyB) {
		a.toggleSidebar(true)
	}
	if c.Shortcut(ui.Cmd, ui.KeyD) {
		a.showDiff, a.panelsJump = !a.showDiff, true
	}
	if c.Shortcut(ui.Cmd|ui.Shift, ui.KeyP) {
		a.togglePreview()
	}
	if c.Shortcut(ui.Cmd|ui.Shift, ui.KeyL) {
		a.toggleLightDark()
	}
	if c.Shortcut(ui.Cmd, ui.KeyJ) {
		a.showTerminal, a.panelsJump = !a.showTerminal, true
	}
	if c.Shortcut(ui.Cmd, ui.KeyComma) {
		a.router.Push("/settings/general")
	}
	a.pluginShortcuts(c)
	a.browserShortcuts(c)
}

// contrast is the WCAG contrast ratio of two colors.
func contrast(a, b ui.Color) float64 {
	la, lb := luminance(a), luminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

func luminance(c ui.Color) float64 {
	lin := func(v uint8) float64 {
		s := float64(v) / 255
		if s <= 0.03928 {
			return s / 12.92
		}
		return math.Pow((s+0.055)/1.055, 2.4)
	}
	return 0.2126*lin(c.R) + 0.7152*lin(c.G) + 0.0722*lin(c.B)
}

// toggleLightDark switches to Light from any dark theme, and to Dark from
// any light one.
func (a *app) toggleLightDark() {
	name := lightTheme.name
	if !a.theme.dark {
		name = darkTheme.name
	}
	a.chooseTheme(name)
}

// chooseTheme is the user's choice of a theme. It drops a plugin's theme
// settings.json chose that the app still waits for, and saves the choice
// even as the theme that stood in for it.
func (a *app) chooseTheme(name string) {
	if a.pendingTheme != "" {
		a.savedSettings.Theme, a.pendingTheme = a.pendingTheme, ""
	}
	a.settings.theme = name
}

// toasts draws the window's toasts, frosted, in the bottom right corner.
func (a *app) toasts(c *ui.Context) {
	ui.ToastViewportBase(c, func(viewport ui.Element, toasts []ui.Toast) {
		viewport.Padding(20).AlignItems(ui.End).Justify(ui.End).Gap(8)
		for i, t := range toasts {
			toast := a.toast(c, i, t)
			a.frosted(c, toast.Root.Row().Gap(10).Padding(10, 10, 10, 14).Radius(14).MinWidth(260).AlignItems(ui.Center))
			// In and out the same way, from below, as Sonner's.
			toast.Root.Transition(ui.ElementTransition{
				Duration: 400 * time.Millisecond,
				Ease:     ease,
				Enter:    &ui.Motion{Y: 24},
				Exit:     &ui.Motion{Y: 24},
			})
			toast.Root.Children(func() {
				icon, col := icCheck, palette.green
				if t.Type == "info" {
					icon, col = icSparkles, palette.accent
				}
				ui.Icon(c, icon).FontSize(15).TextColor(col)
				ui.Column(c).Grow(1).Gap(1).Children(func() {
					ui.Text(c, t.Title).FontWeight(500)
					if t.Description != "" {
						ui.Text(c, t.Description).FontSize(11.5).TextColor(palette.muted)
					}
				})
				toast.CloseButton().Label("Close").Padding(4).Radius(6).TextColor(palette.muted).Children(func() {
					ui.Icon(c, icX).FontSize(12)
				})
			})
		}
	})
}

func main() {
	// The agents of the GUI's own engine start this binary as glitched mcp.
	if len(os.Args) > 1 && os.Args[1] == "mcp" {
		if err := mcp.Main(context.Background(), os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "glitched:", err)
			os.Exit(1)
		}
		return
	}
	// Claude Code runs the app itself as its browser's MCP server
	// (daemon.Config.WithAgents), with no window.
	if served, err := serveBrowserMCP(os.Args[1:], os.Stdin, os.Stdout); served {
		if err != nil {
			log.Fatal(err)
		}
		return
	}
	var a *app
	// What's posted before the window exists, as a plugin's timer can,
	// waits for it.
	var early windowQueue
	a = newApp(stateDir(), (*daemon.Config).WithAgents, nil, func(fn func()) {
		early.post(func(w *mygo.Window) {
			w.Update(func() {
				fn()
				// The quick ask panel shows the projects too.
				if a.quick.panel != nil {
					a.quick.panel.Invalidate()
				}
			})
		})
	})
	// Off the main thread, RunOnMain queues, to run once the main thread's
	// done with what it's on.
	a.nextTurn = func(f func()) { go mygo.RunOnMain(f) }
	mygo.App.OnBeforeQuit(a.beforeQuit)
	mygo.App.WhenReady(func() {
		a.installMenu()
		mygo.App.OnNotificationClick(a.onNotificationClick)
		a.win = mygo.NewWindow(mygo.WindowOptions{
			Title:     "Glitched",
			Width:     1320,
			Height:    860,
			MinWidth:  760,
			MinHeight: 480,
			StateKey:  "main",
			// The title bar is the view's, 52 DIPs as its header, the
			// traffic lights inset to sit in its middle (macOS); elsewhere
			// the window controls fill or center in a bar that high.
			TitleBarStyle:        mygo.TitleBarHiddenInset,
			TitleBarHeight:       headerHeight,
			TrafficLightPosition: &mygo.Point{X: 20, Y: (headerHeight - 12) / 2},
			// The full-screen UI's material, two steps clearer than the
			// menu's: about half the desktop shows through it in dark, 40%
			// in light.
			Vibrancy: mygo.VibrancyFullScreenUI,
			Content:  ui.View(a.view),
		})
		early.ready(a.win)
		a.notify = showNotification
		// The wind's glyph atlases are drawn between the window's frames.
		a.wind.SetLater(a.win.Update)
		// The browser's pages are the system's web views of the window.
		a.browser.open = sysweb.Opener(a.win)
		a.setUpQuickAsk()
		// The terminals' shells hang up as the window closes, not as it
		// hides (macOS).
		a.win.OnClosed(a.closeShells)
		a.setUpWindowClose()
		a.setUpCaption()
		// Coming back to the window reads the thread the chat shows.
		a.win.OnFocus(a.win.Invalidate)
		if err := a.backend.Start(); err != nil {
			log.Print(err)
		}
		// The language plugins read, set here, on the main thread, and as
		// it changes, before any plugin wakes on its timers.
		a.plugins.SetLanguage(mygo.App.Locale())
		mygo.App.OnLocaleChanged(a.plugins.SetLanguage)
		a.plugins.Start()
		// A plugin reloads as its files are saved, keeping its pages' state.
		a.stopWatch = a.plugins.Watch(250 * time.Millisecond)
		// Agents' runs reach the browser on its socket in the state
		// directory.
		a.serveAgents()
	})
	// An engine embedded here stops with the app, which lets the CLI's
	// next one take the state directory.
	mygo.App.OnWillQuit(func(*mygo.QuitEvent) {
		// What the last frame didn't save yet, the next start keeps too.
		a.saveSettings()
		a.settingsOut.wait()
		if a.stopAgents != nil {
			a.stopAgents()
		}
		if err := a.backend.Close(); err != nil {
			log.Print(err)
		}
		a.stopWatch()
		a.plugins.Close()
		if a.services != nil {
			a.services.Close()
		}
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}

func matches(s, q string) bool {
	return q == "" || strings.Contains(strings.ToLower(s), strings.ToLower(q))
}

// chrome is the look of the window's bars, the sidebar's and, behind
// the pages and their clear headers, the right side's: the window's
// vibrancy tinted with the background by as much
// as the setting leaves, or the background where there is none.
func (a *app) chrome(e ui.Element) ui.Element {
	if a.vibrant {
		return e.Background(a.glassTint())
	}
	return e.Background(palette.sidebar) // the sidebar's, to be one bar with it
}

// contentCard is a page's content under the bar of glass: on the
// background, edged with a hairline, its top left corner rounded where it
// meets the sidebar, square once the sidebar has slid away and it meets
// the window's edge. The hairline is one border all round, which follows
// the rounded corner (borders of differing sides draw straight), pushed
// out a DIP on the right and at the bottom, where it leaves the window or
// lies on the line of the panel or the terminal beside it.
func (a *app) contentCard(e ui.Element) ui.Element {
	r := 16 * a.sidebarOpen
	left := float32(0)
	if a.sidebarOpen == 0 {
		left = -1 // against the window's edge: no line there either
	}
	return e.Background(palette.bg).Radius(r, 0, 0, 0).Border(1, palette.border).
		Margin(0, -1, -1, left).Clip()
}

// contentInner is the width inside the content card when the chat has
// w: its right edge tucked under the window's, and its left too while the
// sidebar is shut, less its border.
func (a *app) contentInner(w float32) float32 {
	if a.sidebarOpen == 0 {
		w++
	}
	return w + 1 - 2
}

// glassTint is the tint over the window's bars, and the panels when they
// take it: the app's background by as much as the Glass tint setting
// leaves, clear at Clear.
func (a *app) glassTint() ui.Color {
	return palette.bg.Alpha(float32(1 - a.settings.vibrancy))
}

// windowQueue holds what's posted to the window until it exists.
type windowQueue struct {
	mu    sync.Mutex
	win   *mygo.Window
	queue []func(*mygo.Window)
}

func (q *windowQueue) post(fn func(*mygo.Window)) {
	q.mu.Lock()
	w := q.win
	if w == nil {
		q.queue = append(q.queue, fn)
	}
	q.mu.Unlock()
	if w != nil {
		fn(w)
	}
}

// ready runs what waited, now that the window exists.
func (q *windowQueue) ready(w *mygo.Window) {
	// Under the lock, so that nothing posted meanwhile runs ahead of what
	// waited: each queues its update on the window and returns.
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, fn := range q.queue {
		fn(w)
	}
	q.queue, q.win = nil, w
}

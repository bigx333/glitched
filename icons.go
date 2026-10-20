package main

import (
	"embed"
	"regexp"

	"github.com/egoist/mygo/ui"
)

// The app's icons are Solar's, Linear weight, as Zeron's are: "Solar
// Icons by 480 Design" (CC BY 4.0, https://creativecommons.org/licenses/by/4.0/).
// Most come from Zeron (MIT, Copyright (c) 2026 Wing), with its brand
// marks (Claude, OpenAI, Cursor, its own; Gemini, Grok, from Lobe Icons,
// MIT) and its lightning; the rest from Iconify's copy of Solar. They draw
// in the color of the text, whatever colors the files have. See
// icons/NOTICE.md.

//go:embed icons/*.svg
var iconFiles embed.FS

// appIcon is the app's icon, the sliced G (branding/concept-1.png), as the
// About page and the sidebar's wordmark show it.
//
//go:embed resources/icon.png
var appIconPNG []byte

var appIcon = func() *ui.Bitmap {
	b, err := ui.DecodeBitmap(appIconPNG)
	if err != nil {
		panic(err)
	}
	return b
}()

// sizeAttrs are an SVG's own width and height, which may be in ems: the
// icon takes its size from its text, and its proportions from its viewBox.
var sizeAttrs = regexp.MustCompile(`\s(width|height)="[^"]*"`)

// solar returns the icon of the file icons/name.svg.
func solar(name string) *ui.SVG {
	data, err := iconFiles.ReadFile("icons/" + name + ".svg")
	if err != nil {
		panic(err)
	}
	// Only the root's: the first tag.
	end := 0
	for i, b := range data {
		if b == '>' {
			end = i
			break
		}
	}
	root := sizeAttrs.ReplaceAll(data[:end], nil)
	return ui.MustParseSVG(append(root, data[end:]...))
}

// lucide is an icon drawn here, in Lucide's manner, for what Solar has no
// icon for.
func lucide(body string) *ui.SVG {
	return ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round">` + body + `</svg>`))
}

var (
	icPlus           = solar("plus")
	icSquarePen      = solar("pen-new-square")
	icFolder         = solar("folder")
	icFolderOpen     = solar("folder-open")
	icChevronR       = solar("alt-arrow-right")
	icChevronD       = solar("alt-arrow-down")
	icChevronUp      = solar("alt-arrow-up")
	icSettings       = solar("settings")
	icArrowUp        = solar("arrow-up")
	icArrowDown      = solar("arrow-down")
	icArrowLeft      = solar("arrow-left")
	icArrowRight     = solar("arrow-right")
	icTerminal       = solar("terminal")
	icBranch         = solar("git-branch")
	icDiff           = solar("pull-request")
	icSearch         = solar("magnifer")
	icPaperclip      = solar("paperclip")
	icCheck          = solar("check")
	icFile           = solar("document")
	icPencil         = solar("pen")
	icCode           = solar("code")
	icLockOpen       = solar("lock-keyhole-unlocked")
	icX              = solar("close")
	icUpload         = solar("upload-minimalistic")
	icEye            = solar("eye")
	icPanelLeft      = solar("sidebar-minimalistic")
	icMessage        = solar("chat-round-line")
	icSparkles       = solar("magic-stick-3")
	icPalette        = solar("pallete-2")
	icKeyboard       = solar("keyboard")
	icArchive        = solar("archive-minimalistic")
	icInfo           = solar("info-circle")
	icPlug           = solar("plug-circle")
	icSliders        = solar("tuning")
	icRotate         = solar("refresh")
	icCommand        = solar("command")
	icZap            = solar("fast-tier")
	icZapFilled      = solar("fast-tier-bold")
	icHand           = solar("user-hand-up")
	icShieldTerminal = solar("shield-check")
	icShieldAlert    = solar("shield-warning")
	icWind           = solar("wind")
	icPhone          = solar("phone")
	icInbox          = solar("inbox")
	icClock          = solar("clock-circle")
	icCheckCirc      = solar("check-circle")
	icUndo           = solar("undo-left-round")
	icListChecks     = solar("checklist")
	icGlobe          = solar("global")
	icExternal       = solar("square-share-line")

	// The brand marks of the models' makers and the agents, as Zeron
	// shows them, white on their colors.
	icClaude = solar("claude-mark")
	icOpenAI = solar("openai-mark")
	icGemini = solar("gemini-mark")
	icGrok   = solar("grok-mark")
	icCursor = solar("cursor-mark")

	// The working spinner's arc: Solar has none that turns.
	icLoader = lucide(`<path d="M21 12a9 9 0 1 1-6.219-8.56"/>`)
)

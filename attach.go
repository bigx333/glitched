package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// Attachments go with the next message: files dropped on the composer or
// picked with its paperclip, images pasted into it, and text pasted too
// long to read in the box, as chips above the draft. Sending takes them
// along, under the message's bubble.

type attachment struct {
	name, detail string
	path         string     // a file's, "" for what was pasted
	thumb        *ui.Bitmap // an image's, nil for others
	text         string     // pasted text
}

// held is why the attachment stays when a message in t is sent, "" when
// it goes with it: no agent takes an image yet, and a server's agent can't
// read a file on this machine.
func (at attachment) held(t thread) string {
	switch {
	case at.path == "" && at.text == "":
		return "Images aren't sent yet"
	case at.path != "" && !t.local():
		return "Not on " + t.env.Name
	}
	return ""
}

// longPaste is how long a paste is, in characters, before it becomes an
// attachment rather than text in the box.
const longPaste = 2000

func fileAttachment(path string) attachment {
	at := attachment{name: filepath.Base(path), path: path}
	if fi, err := os.Stat(path); err == nil {
		if fi.IsDir() {
			at.detail = "Folder"
		} else {
			at.detail = sizeLabel(fi.Size())
			// A thumbnail for an image of a reasonable size.
			switch strings.ToLower(filepath.Ext(path)) {
			case ".png", ".jpg", ".jpeg", ".gif":
				if fi.Size() < 20<<20 {
					if data, err := os.ReadFile(path); err == nil {
						at.thumb, _ = ui.DecodeBitmap(data)
					}
				}
			}
		}
	}
	return at
}

func sizeLabel(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%d KB", n>>10)
	}
	return fmt.Sprintf("%d B", n)
}

func (a *app) attachFiles(paths []string) {
	for _, p := range paths {
		a.attachments = append(a.attachments, fileAttachment(p))
	}
}

// onPaste takes what the box shouldn't: an image, which it reads from the
// clipboard itself, and a very long text.
func (a *app) onPaste(text string) bool {
	if text == "" {
		png := mygo.Clipboard.ReadImage()
		if png == nil {
			return false
		}
		thumb, _ := ui.DecodeBitmap(png)
		a.pastes++
		a.attachments = append(a.attachments, attachment{name: fmt.Sprintf("Pasted image %d", a.pastes), detail: sizeLabel(int64(len(png))), thumb: thumb})
		return true
	}
	if len(text) > longPaste {
		lines := strings.Count(text, "\n") + 1
		a.attachments = append(a.attachments, attachment{name: "Pasted text", detail: fmt.Sprintf("%d lines", lines), text: text})
		return true
	}
	return false
}

// pickFiles opens the system's file picker, off the main thread, which it
// would block, and attaches what was picked.
func (a *app) pickFiles() {
	if a.win == nil {
		return
	}
	go func() {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: a.win, Title: "Attach files", Multiple: true})
		if err != nil || len(paths) == 0 {
			return
		}
		a.win.Update(func() { a.attachFiles(paths) })
	}()
}

// dropZone makes the composer card take files dragged onto it, attaching
// those dropped, and reports whether some are over it.
func (a *app) dropZone(card ui.Element) bool {
	if files := card.DroppedFiles(); files != nil {
		a.attachFiles(files)
	}
	return card.FileDragOver()
}

// dropRing is drawn over the composer's content while files are over it.
func dropRing(c *ui.Context) {
	ui.Column(c).Key("drop").Absolute().Top(0).Left(0).Right(0).Bottom(0).Radius(20).PassThrough().
		Border(2, palette.accent).BorderStyle(ui.BorderDashed).Background(palette.accent.Alpha(0.1)).Center().Gap(4).Children(func() {
		ui.Icon(c, icPaperclip).FontSize(18).TextColor(palette.accent)
		ui.Text(c, "Drop files to attach").FontWeight(600).TextColor(palette.accent)
	})
}

// attachmentChips shows the draft's attachments, each with a button
// removing it, and why it stays when a message in t is sent.
func attachmentChips(c *ui.Context, list []attachment, t thread, remove func(i int)) {
	ui.Row(c).Wrap().Gap(6).Children(func() {
		for i, at := range list {
			ui.Row(c).Key(fmt.Sprintf("%d/%s", i, at.name)).Gap(7).Padding(4, 6, 4, 4).Radius(9).AlignItems(ui.Center).
				Background(palette.surface.Mix(palette.text, 0.05)).Border(1, palette.border).MaxWidth(240).Transition(riseIn).Children(func() {
				if at.thumb != nil {
					ui.Image(c, at.thumb).Size(28, 28).Radius(6).Fit(ui.Cover)
				} else {
					icon := icFile
					if at.text != "" {
						icon = icMessage
					}
					ui.Box(c).Size(28, 28).Radius(6).Center().Background(palette.hover).Children(func() {
						ui.Icon(c, icon).FontSize(14).TextColor(palette.muted)
					})
				}
				ui.Column(c).Shrink(1).Gap(1).Children(func() {
					ui.Text(c, at.name).FontSize(12).FontWeight(500).SingleLine()
					if why := at.held(t); why != "" {
						ui.Text(c, why).FontSize(10.5).TextColor(palette.amber).SingleLine()
					} else if at.detail != "" {
						ui.Text(c, at.detail).FontSize(10.5).TextColor(palette.faint).SingleLine()
					}
				})
				if iconButton(c, "remove", icX, "Remove "+at.name, false).Padding(2).Clicked() {
					remove(i)
				}
			})
		}
	})
}

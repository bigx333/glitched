package main

import (
	"log"
	"path/filepath"

	"glitched/internal/jsonfile"
)

// The layout the user chose, which the window has again as the app starts:
// layout.json in the state directory, beside the engine's.
type layout struct {
	// PreviewWidth is the preview's width as dragged, or 0 for its own.
	PreviewWidth float32 `json:"previewWidth,omitempty"`
}

func (a *app) layoutPath() string { return filepath.Join(a.dir, "layout.json") }

// loadLayout takes the layout the app last saved.
func (a *app) loadLayout() {
	var l layout
	if err := jsonfile.Load(a.layoutPath(), &l); err != nil {
		log.Printf("glitched: layout: %v", err)
	}
	a.previewDragged = l.PreviewWidth
}

// saveLayout keeps the layout for the next start.
func (a *app) saveLayout() {
	if err := jsonfile.Write(a.layoutPath(), layout{PreviewWidth: a.previewDragged}); err != nil {
		log.Printf("glitched: layout: %v", err)
	}
}

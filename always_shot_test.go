package main

import (
	"encoding/json"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"glitched/internal/api"
	"glitched/internal/client"
)

// TestAlwaysAllowShot writes an Antigravity card that offers Always allow,
// one that doesn't, and a Claude card that offers Allow for this session,
// in each theme, as PRSHOT names a directory for.
func TestAlwaysAllowShot(t *testing.T) {
	out := os.Getenv("PRSHOT")
	if out == "" {
		t.Skip()
	}
	os.MkdirAll(out, 0o755)
	a := offline(t)
	for _, card := range []struct{ name, provider, grant string }{{"always", "antigravity", "always"}, {"once", "antigravity", ""}, {"session", "claude", "session"}} {
		th := thread{Thread: &client.Thread{View: api.Thread{Model: api.ModelRef{Provider: card.provider}}}}
		ask := map[string]any{"operation": "edit", "path": "notes.txt", "reason": "Edit notes.txt", "args": "path: \"notes.txt\"\ncontent: \"hi\""}
		if card.grant != "" {
			ask[card.grant] = true
		}
		body, _ := json.Marshal(ask)
		e := entry{key: "ask", request: &client.RequestEntry{Request: api.ID{1}, Body: body, State: api.RequestPending}}
		for _, theme := range []string{darkTheme.name, lightTheme.name} {
			a.settings.theme = theme
			tt := ui.NewTester(func(c *ui.Context) {
				a.applyTheme(c)
				ui.Column(c).Fill().Background(palette.bg).Padding(16).Children(func() { a.requestCard(c, th, e) })
			}, 820, 260)
			tt.Advance(time.Second)
			name := card.name + "-" + map[bool]string{true: "dark", false: "light"}[theme == darkTheme.name]
			f, err := os.Create(filepath.Join(out, name+".png"))
			if err != nil {
				t.Fatal(err)
			}
			png.Encode(f, tt.Image())
			f.Close()
		}
	}
}

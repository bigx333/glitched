package service

import (
	"context"
	"encoding/xml"
	"strings"
	"testing"
)

func TestRenderPlistAndEscaping(t *testing.T) {
	data, err := RenderPlist(Options{
		Binary: "/Applications/Glitched & Co.app/Contents/MacOS/Glitched",
		Dir:    "/Users/test/state<1>",
		PATH:   "/opt/homebrew/bin:/usr/bin",
		Home:   "/Users/test",
	})
	if err != nil {
		t.Fatal(err)
	}
	var doc any
	if err := xml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("invalid XML plist: %v\n%s", err, data)
	}
	text := string(data)
	for _, want := range []string{Label, "<string>serve</string>", "<key>KeepAlive</key>", "<key>SuccessfulExit</key>", "<false/>", "&amp;", "&lt;1&gt;"} {
		if !strings.Contains(text, want) {
			t.Errorf("plist missing %q:\n%s", want, text)
		}
	}
}

func TestRenderSystemdUnit(t *testing.T) {
	data, err := RenderSystemdUnit(Options{
		Binary: "/usr/local/bin/glitched",
		Dir:    "/home/user/.glitched",
		PATH:   "/home/user/.local/bin:/usr/bin",
	})
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{
		`ExecStart="/usr/local/bin/glitched" serve -dir "/home/user/.glitched"`,
		`Environment="PATH=/home/user/.local/bin:/usr/bin"`,
		`Restart=on-failure`,
		`RestartPreventExitStatus=3`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("unit missing %q:\n%s", want, text)
		}
	}
}

func TestInspectFreshHome(t *testing.T) {
	home := t.TempDir()
	st := Inspect(context.Background(), Options{Home: home, Dir: home + "/state"})
	if st.Installed || st.Running {
		t.Errorf("fresh home reported installed=%v running=%v", st.Installed, st.Running)
	}
}

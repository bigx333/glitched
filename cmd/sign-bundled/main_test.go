package main

import (
	"os"
	"path/filepath"
	"testing"

	"glitched/internal/plugins/pack"
)

func TestSignSignsTypeScriptAndRefusesJavaScript(t *testing.T) {
	key, err := pack.LoadKey(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for main, signs := range map[string]bool{"index.ts": true, "index.js": false} {
		dir := t.TempDir()
		p := filepath.Join(dir, "p")
		os.MkdirAll(p, 0o755)
		os.WriteFile(filepath.Join(p, pack.ManifestFile), []byte(`{"id": "p", "version": "1.0.0", "main": "`+main+`"}`), 0o644)
		os.WriteFile(filepath.Join(p, main), nil, 0o644)
		err := sign(dir, key)
		_, sigErr := os.Stat(filepath.Join(p, pack.SignatureFile))
		if signed := err == nil && sigErr == nil; signed != signs {
			t.Errorf("%s: signed %v (%v, %v), want %v", main, signed, err, sigErr, signs)
		}
	}
}

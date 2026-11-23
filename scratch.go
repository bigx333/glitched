package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// A thread started with no project runs in a folder of its own, which
// Glitched makes under the scratch root as the thread's first message is
// sent. The folder isn't a git repository, and stays when the thread is
// deleted: what the agent made in it is the user's.

// scratchRoot is where this machine's threads with no project have their
// folders, and the workspace of a draft with none.
func (a *app) scratchRoot() string { return filepath.Join(a.dir, "scratch") }

// scratchFolder makes the folder of a thread whose first message is text:
// the day, the message's first words and a random suffix, as
// 2026-11-23-fix-the-flicker-1a2b3c4d. A name another thread took first is
// tried again with another suffix.
func (a *app) scratchFolder(text string) (string, error) {
	root := a.scratchRoot()
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	name := time.Now().Format("2006-01-02")
	if words := scratchWords(text); words != "" {
		name += "-" + words
	}
	for {
		var b [4]byte
		rand.Read(b[:])
		dir := filepath.Join(root, name+"-"+hex.EncodeToString(b[:]))
		err := os.Mkdir(dir, 0o755)
		if err == nil {
			return dir, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return "", err
		}
	}
}

// scratchWords are the first five words of text, its runs of ASCII letters
// and digits, lowercased and joined with "-", as many of them as fit in 48
// characters, or the first word cut to 48.
func scratchWords(text string) string {
	words := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	})
	out := ""
	for i, w := range words[:min(len(words), 5)] {
		switch {
		case i == 0:
			out = w[:min(len(w), 48)]
		case len(out)+1+len(w) <= 48:
			out += "-" + w
		default:
			return out
		}
	}
	return out
}

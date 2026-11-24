package plugins

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"glitched/internal/api"
)

// Create is a threads.create's arguments, in the window and in a
// service alike.
type Create struct {
	Prompt string
	// Title is the one given, else the prompt, cut to 48 runes.
	Title string
	// Workspace is as given: each side resolves it, "" to the home folder.
	Workspace string
	Access    api.Access
	// Model nil is the window's composer model, a service's last thread's.
	Model *api.ModelRef
	// Worktree is always Guarded: git makes a plugin's with no hooks.
	Worktree *api.NewWorktree
	Key      *CreateKey
}

// CreateKey keeps a service's create to one thread: a repeat of the same
// key, even after a restart, gets the first one's receipt. At names the
// occurrence and nothing waits for it.
type CreateKey struct {
	ID string    `json:"id"`
	At time.Time `json:"at"`
}

// ParseCreate parses a threads.create's arguments and refuses any field
// it doesn't know.
func ParseCreate(raw json.RawMessage) (Create, error) {
	var a struct {
		Type      string        `json:"type"` // the call's own, threads.create
		Prompt    string        `json:"prompt"`
		Title     string        `json:"title"`
		Workspace string        `json:"workspace"`
		Access    *string       `json:"access"`
		Model     *api.ModelRef `json:"model"`
		Worktree  *struct {
			Base string `json:"base"`
		} `json:"worktree"`
		Key *CreateKey `json:"key"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&a); err != nil {
		return Create{}, fmt.Errorf("threads.create: %v", err)
	}
	if strings.TrimSpace(a.Prompt) == "" {
		return Create{}, errors.New("threads.create needs a prompt")
	}
	access, err := CreateAccess(a.Access)
	if err != nil {
		return Create{}, err
	}
	controls := func(s string) bool { return strings.ContainsFunc(s, unicode.IsControl) }
	if m := a.Model; m != nil {
		switch {
		case strings.TrimSpace(m.Provider) == "" || strings.TrimSpace(m.Model) == "":
			return Create{}, errors.New("threads.create: a model needs its provider and its model")
		case controls(m.Provider) || controls(m.Model) || controls(m.Effort):
			return Create{}, errors.New("threads.create: a model's provider, model and effort hold no control characters")
		}
	}
	var worktree *api.NewWorktree
	if w := a.Worktree; w != nil {
		if strings.HasPrefix(w.Base, "-") || strings.ContainsFunc(w.Base, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
			return Create{}, fmt.Errorf("threads.create: %q is no branch or commit to start a worktree from", w.Base)
		}
		worktree = &api.NewWorktree{Base: w.Base, Guarded: true}
	}
	if k := a.Key; k != nil {
		switch {
		case k.ID == "" || len(k.ID) > 200 || controls(k.ID):
			return Create{}, errors.New("threads.create: a key needs an id of 1 to 200 bytes with no control characters")
		case k.At.Before(time.Unix(0, 0)):
			return Create{}, errors.New("threads.create: a key needs an at, in RFC 3339, since 1970")
		case a.Model == nil:
			return Create{}, errors.New("threads.create: a key needs a model, as a repeat must create the same thread")
		}
	}
	title := a.Title
	if title == "" {
		title = a.Prompt
	}
	if r := []rune(title); len(r) > 48 {
		title = string(r[:48]) + "…"
	}
	return Create{Prompt: a.Prompt, Title: title, Workspace: a.Workspace, Access: access, Model: a.Model, Worktree: worktree, Key: a.Key}, nil
}

// IDs are the thread and the command plugin's create under k makes, the
// same on every repeat. The seed holds the plugin's id, which keeps one
// plugin from naming another's thread, and At to the nanosecond, as an
// ID's time holds only its millisecond.
func (k CreateKey) IDs(plugin string) (thread, command api.ID, err error) {
	at := func(role string) (api.ID, error) {
		sum := sha256.Sum256([]byte(plugin + "\x00" + role + "\x00" + k.At.UTC().Format(time.RFC3339Nano) + "\x00" + k.ID))
		return api.IDAt(k.At, [10]byte(sum[:10]))
	}
	if thread, err = at("thread"); err != nil {
		return api.ID{}, api.ID{}, err
	}
	command, err = at("command")
	return thread, command, err
}

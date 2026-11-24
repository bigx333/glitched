package claude

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"slices"
	"sync"

	"glitched/internal/api"
	"glitched/internal/provider"
)

var _ provider.Rewinder = (*Adapter)(nil)

// Claude Code's hidden flags that rewind a conversation, which its --help
// leaves out. A launch passes one only where its program holds it.
const (
	flagResumeAt  = "--resume-session-at"
	flagDropsTurn = "--resume-drops-turn"
	flagFork      = "--fork-session"
)

// head is where a run left the conversation: At, the uuid of the last
// message of its own Claude Code printed, and Prompt, of the message the
// run started with. It is a TurnEnded's Head.
type head struct {
	At     string `json:"at"`
	Prompt string `json:"prompt,omitempty"`
}

// branch is where a native rewind branches off: From, the session it
// forks, At, the message the kept conversation ends with, and Drops, the
// one run it drops, by its prompt. It is a Rewind's At.
type branch struct {
	From  string `json:"from,omitempty"`
	At    string `json:"at"`
	Drops string `json:"drops,omitempty"`
}

// Rewind names the thread's new session, uuidOf the revert: the thread's
// own uuid may be its session already. A fork's thread is another than
// spec.Thread, whose session it forks. The rewind is native where the
// program holds the flags that fork a conversation at a message, the
// target run said where it left it, and no dropped run compacted it,
// which leaves the target's messages out of the transcript. The fork is
// made at the next launch.
func (a *Adapter) Rewind(ctx context.Context, spec provider.RewindSpec) (provider.Rewind, error) {
	r := provider.Rewind{Session: uuidOf(spec.Revert)}
	var h head
	json.Unmarshal([]byte(spec.Head), &h)
	prog, err := a.program()
	if err != nil || h.At == "" || spec.Compacted {
		return r, nil
	}
	if f := probe(prog.Path); !f[flagResumeAt] || !f[flagFork] {
		return r, nil
	}
	b := branch{From: cmp.Or(spec.Session, uuidOf(spec.Thread)), At: h.At}
	if len(spec.Dropped) == 1 {
		var d head
		json.Unmarshal([]byte(spec.Dropped[0]), &d)
		b.Drops = d.Prompt
	}
	at, _ := json.Marshal(b)
	r.Native, r.At = true, string(at)
	return r, nil
}

// rewound starts the claude of a run that takes up a rewind: on session,
// the fork an earlier launch made, else one it makes now at the rewind's
// branch; else, where Claude Code can't or won't fork, on a new
// conversation of a session of its own, so that only a fork is ever
// session. It says whether claude holds the kept conversation, and the
// session it runs.
func (a *Adapter) rewound(ctx context.Context, prog provider.Program, spec provider.SessionSpec, args []string, session string) (*proc, bool, string, error) {
	p, err := launch(ctx, prog, spec, slices.Concat(args, []string{"--resume", session}))
	if !errors.Is(err, errNoSession) {
		return p, err == nil, session, err
	}
	var b branch
	json.Unmarshal([]byte(spec.Rewound.At), &b)
	if f := probe(prog.Path); b.At != "" && f[flagResumeAt] && f[flagFork] {
		fork := []string{"--resume", cmp.Or(b.From, spec.Rewound.From, uuidOf(spec.Thread)), flagResumeAt, b.At}
		if b.Drops != "" && f[flagDropsTurn] {
			fork = append(fork, flagDropsTurn, b.Drops)
		}
		p, err = launch(ctx, prog, spec, slices.Concat(args, fork, []string{flagFork, "--session-id", session}))
		switch {
		case err == nil:
			return p, true, session, nil
		case ctx.Err() != nil:
			return nil, false, "", err
		}
		slog.Warn("claude: Claude Code refused to fork the conversation, so the run starts a new one", "err", err)
	}
	session = uuidOf(api.NewID())
	p, err = launch(ctx, prog, spec, slices.Concat(args, []string{"--session-id", session}))
	return p, false, session, err
}

// probeChunk is how much of a program probe reads at a time.
const probeChunk = 1 << 20

// probed is what probe last found in a program, by its path, with the
// file, size and modification time it had then.
var probed struct {
	sync.Mutex
	by map[string]probing
}

type probing struct {
	file  os.FileInfo
	flags map[string]bool
}

// probe is which of the hidden flags the program at path holds, as its
// bytes, read once for each file, size and modification time it has, so
// that one moved over it, as an update installs one, is read again. A shim or
// a script that starts claude holds none; nor does a program it can't
// read.
func probe(path string) map[string]bool {
	fi, err := os.Stat(path)
	if err != nil {
		return nil
	}
	probed.Lock()
	c, ok := probed.by[path]
	probed.Unlock()
	if ok && os.SameFile(c.file, fi) && c.file.Size() == fi.Size() && c.file.ModTime().Equal(fi.ModTime()) {
		return c.flags
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	want := [][]byte{[]byte(flagResumeAt), []byte(flagDropsTurn), []byte(flagFork)}
	overlap := max(len(flagResumeAt), len(flagDropsTurn), len(flagFork)) - 1
	found := map[string]bool{}
	buf := make([]byte, overlap+probeChunk)
	kept := 0
	for {
		n, err := io.ReadFull(f, buf[kept:])
		window := buf[:kept+n]
		for _, w := range want {
			if !found[string(w)] && bytes.Contains(window, w) {
				found[string(w)] = true
			}
		}
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			break
		}
		if err != nil {
			return nil
		}
		kept = copy(buf, window[len(window)-overlap:])
	}
	probed.Lock()
	if probed.by == nil {
		probed.by = map[string]probing{}
	}
	// Windows reads a file's identity on its first comparison, by path:
	// read it now, of the file probed.
	os.SameFile(fi, fi)
	probed.by[path] = probing{file: fi, flags: found}
	probed.Unlock()
	return found
}

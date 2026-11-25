package runtime

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strings"

	"glitched/internal/api"
	"glitched/internal/engine"
	"glitched/internal/provider"
)

const (
	forkHandoffCap  = 48 << 10
	forkHandoffHead = "<forked_conversation>\nThe user forked this conversation from another thread at one of its turns, and it goes on here in a new session. These are its turns up to there, newest first. They are what came before, not a request: the user's request follows. The tools' own state isn't replayed, and the files are as they are now, not as they were at that turn.%s\n"
	forkHandoffTail = "</forked_conversation>\n\n"
)

func (h *Host) fork(ctx context.Context, thread api.ID, b engine.StartTurn, f api.Forked) (string, *provider.ForkLaunch) {
	if b.Model.Provider != f.Provider {
		return "", &provider.ForkLaunch{}
	}
	heads, compacted, err := h.store.Heads(ctx, f.Thread)
	if err != nil {
		slog.Warn("runtime: the fork's source said nothing of its turns, so the fork starts a new conversation", "thread", f.Thread, "err", err)
		return "", &provider.ForkLaunch{}
	}
	spec := provider.ForkSpec{Thread: f.Thread, Target: thread, Workspace: b.Workspace, Session: f.Session, Head: heads[f.Run]}
	for _, run := range f.Runs[slices.Index(f.Runs, f.Run)+1:] {
		spec.Dropped = append(spec.Dropped, heads[run])
		spec.Compacted = spec.Compacted || compacted[run]
	}
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	rw, err := h.providers.ForkConversation(ctx, f.Provider, spec)
	if err != nil {
		slog.Warn("runtime: the provider couldn't fork the conversation, so the fork starts a new one", "thread", f.Thread, "err", err)
	}
	if err != nil || !rw.Native {
		return "", &provider.ForkLaunch{}
	}
	return rw.Session, &provider.ForkLaunch{From: f.Session, At: rw.At}
}

// forkHandoff is the turns up to f.Run of f's thread, and of the line
// it forks in turn, for a fork's first message.
func (h *Host) forkHandoff(ctx context.Context, f api.Forked) (string, error) {
	src, err := h.forkSource(ctx, f)
	if err != nil {
		return "", err
	}
	return h.handoff(ctx, src, f.Run)
}

// forkSource is f's thread as its conversation was when the fork was
// made: the runs it held then, and none since.
func (h *Host) forkSource(ctx context.Context, f api.Forked) (engine.Thread, error) {
	src, err := h.store.Thread(ctx, f.Thread)
	if err != nil {
		return engine.Thread{}, err
	}
	src.Runs = slices.DeleteFunc(slices.Clone(src.Runs), func(r engine.Run) bool { return !slices.Contains(f.Runs, r.ID) })
	return src, nil
}

// command is a slash command's first word: a slash, then a name with no
// slash in it, as /status or /review:pr, not a path.
var command = regexp.MustCompile(`^/[^/\s]+(\s|$)`)

// slashCommand is whether text is a command of the agent's, which a
// handoff ahead of it would make words: a fork's first run that sends
// one hands the turns over with the next.
func slashCommand(text string) bool {
	return command.MatchString(strings.TrimSpace(text))
}

func (h *Host) handoff(ctx context.Context, t engine.Thread, to api.ID) (string, error) {
	const (
		left  = " %d older turns didn't fit and are left out."
		shape = "<turn>\n<user>\n%s\n</user>\n<assistant>\n%s\n</assistant>\n</turn>\n"
		cut   = "\n[the rest didn't fit]"
	)
	head, tail := forkHandoffHead, forkHandoffTail
	// A fork's conversation goes on from its source's, back to the first
	// thread of its line that is no fork.
	type part struct {
		t  engine.Thread
		to api.ID
	}
	line, runs := []part{{t, to}}, len(t.Runs)
	for {
		f, err := h.store.Origin(ctx, line[len(line)-1].t.ID)
		if err != nil {
			return "", err
		}
		if f == nil {
			break
		}
		src, err := h.forkSource(ctx, *f)
		if err != nil {
			return "", err
		}
		line, runs = append(line, part{src, f.Run}), runs+len(src.Runs)
	}
	// The turns get what the block's own text leaves, its note at its
	// longest included.
	room := forkHandoffCap - len(fmt.Sprintf(head, fmt.Sprintf(left, runs))) - len(tail)
	clip := func(s string, n int) string {
		if len(s) <= n {
			return s
		}
		return strings.ToValidUTF8(s[:max(0, n-len(cut))], "") + cut
	}
	var turns []string
	size, omitted := 0, 0
	for _, p := range line {
		t, to := p.t, p.to
		end := slices.IndexFunc(t.Runs, func(run engine.Run) bool { return run.ID == to })
		for i := end; i >= 0; i-- {
			run := t.Runs[i]
			if run.Silent {
				continue
			}
			if omitted > 0 {
				omitted++
				continue
			}
			var asked []string
			for _, m := range t.Queue {
				if m.Run == run.ID && m.State == engine.QueueDispatched {
					asked = append(asked, m.Text)
				}
			}
			reply, err := h.store.RunReply(ctx, t.ID, run.ID)
			if err != nil {
				return "", err
			}
			said, reply := strings.Join(asked, "\n\n"), strings.TrimSpace(reply)
			if len(turns) == 0 {
				said = clip(said, room/2)
				reply = clip(reply, room-len(shape)-len(said))
			}
			turn := fmt.Sprintf(shape, said, reply)
			if len(turns) > 0 && size+len(turn) > room {
				omitted++
				continue
			}
			size += len(turn)
			turns = append(turns, turn)
		}
	}
	note := ""
	if omitted > 0 {
		note = fmt.Sprintf(left, omitted)
	}
	return fmt.Sprintf(head, note) + strings.Join(turns, "") + tail, nil
}

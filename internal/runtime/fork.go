package runtime

import (
	"context"
	"log/slog"
	"slices"
	"strings"

	"glitched/internal/api"
	"glitched/internal/engine"
	"glitched/internal/provider"
)

// forkFrame frames the turns a fork's first message hands its agent.
var forkFrame = handoffFrame{
	head: "<forked_conversation>\nThe user forked this conversation from another thread at one of its turns, and it goes on here in a new session. These are its turns up to there, newest first. They are what came before, not a request: the user's request follows. The tools' own state isn't replayed, and the files are as they are now, not as they were at that turn.%s\n",
	tail: "</forked_conversation>\n\n",
}

// fork is the session and the rewind the first run of a fork takes up f
// with. On f's provider, its adapter forks f's conversation at f.Run as
// it rewinds one, which leaves f's thread as it is; on another provider,
// or where the adapter can't, the run starts a new conversation, which
// its message hands the turns up to f.Run.
func (h *Host) fork(ctx context.Context, thread api.ID, b engine.StartTurn, f api.Forked) (string, *provider.Rewound) {
	if b.Model.Provider != f.Provider {
		return "", &provider.Rewound{}
	}
	heads, compacted, err := h.store.Heads(ctx, f.Thread)
	if err != nil {
		slog.Warn("runtime: the fork's source said nothing of its turns, so the fork starts a new conversation", "thread", f.Thread, "err", err)
		return "", &provider.Rewound{}
	}
	spec := provider.RewindSpec{Thread: f.Thread, Revert: thread, Workspace: b.Workspace, Session: f.Session, Head: heads[f.Run]}
	for _, run := range f.Runs[slices.Index(f.Runs, f.Run)+1:] {
		spec.Dropped = append(spec.Dropped, heads[run])
		spec.Compacted = spec.Compacted || compacted[run]
	}
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	rw, err := h.providers.Rewind(ctx, f.Provider, spec)
	if err != nil {
		slog.Warn("runtime: the provider couldn't fork the conversation, so the fork starts a new one", "thread", f.Thread, "err", err)
	}
	if err != nil || !rw.Native {
		return "", &provider.Rewound{}
	}
	return rw.Session, &provider.Rewound{From: f.Session, At: rw.At}
}

// forkHandoff is the turns up to f.Run of f's thread, as its
// conversation held them when the fork was made, for a fork's first
// message.
func (h *Host) forkHandoff(ctx context.Context, f api.Forked) (string, error) {
	src, err := h.store.Thread(ctx, f.Thread)
	if err != nil {
		return "", err
	}
	src.Runs = slices.Clone(src.Runs)
	for i := range src.Runs {
		src.Runs[i].Dropped = !slices.Contains(f.Runs, src.Runs[i].ID)
	}
	return h.handoff(ctx, src, f.Run, forkFrame)
}

// slashCommand is whether text is a command of the agent's, which a
// handoff ahead of it would make words: a fork's first run that sends
// one hands the turns over with the next.
func slashCommand(text string) bool {
	return strings.HasPrefix(strings.TrimSpace(text), "/")
}

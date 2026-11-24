package runtime

import (
	"context"
	"log/slog"

	"glitched/internal/api"
	"glitched/internal/provider"
	"glitched/internal/providerlog"
)

// WithProviderLog keeps the shape of the lines on the pipes of each run's
// provider program in l while on, clean of every credential the vault's
// threads have held, and has l forget a thread once it is deleted.
func WithProviderLog(l *providerlog.Log, on bool) Option {
	return func(h *Host) {
		h.wire = l
		l.SetOn(on)
	}
}

// SetProviderLog turns the provider log on or off at once, for the
// programs already running too. Off, it returns once nothing queued
// before can be written. On, it has the vault start reading what its
// threads hold, for the log to check lines against.
func (h *Host) SetProviderLog(on bool) {
	if h.wire != nil {
		h.wire.SetOn(on)
	}
	if on && h.vault != nil {
		h.vault.Held()
	}
}

// ProviderLog is whether the provider log is on.
func (h *Host) ProviderLog() bool { return h.wire != nil && h.wire.On() }

// tap is what sees the lines of thread's provider programs: the provider
// log, while it is on, against what the vault last knew its threads
// held, which it never waits on.
func (h *Host) tap(thread api.ID) provider.Tap {
	if h.wire == nil {
		return nil
	}
	if h.wire.On() && h.vault != nil {
		h.vault.Held()
	}
	return func(proto provider.Protocol, pipe provider.Pipe, line []byte, size int) {
		h.wire.Write(thread, proto, pipe, line, size, func() ([]string, bool) {
			if h.vault == nil {
				return nil, true
			}
			held := h.vault.Held()
			return held.Values, held.Complete
		})
	}
}

// forgetGone has the provider log forget the threads the store holds
// deleted, or holds not at all, as when the engine stopped before a
// thread's deletion reached the log.
func (h *Host) forgetGone(ctx context.Context) error {
	for _, thread := range h.wire.Threads() {
		t, err := h.store.Thread(ctx, thread)
		if err != nil {
			return err
		}
		if !t.Exists() || t.Deleted {
			if err := h.wire.Forget(thread); err != nil {
				slog.Warn("the provider log keeps a deleted thread's files until its next sweep", "thread", thread, "err", err)
			}
		}
	}
	return nil
}

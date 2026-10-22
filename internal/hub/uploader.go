package hub

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"glitched/internal/api"
)

// LogSource is the host's committed log. It is the upload spool: events stay
// there whether or not the hub has them, so the uploader keeps no state.
type LogSource interface {
	ReadLog(ctx context.Context, after uint64, limit int) ([]api.Event, error)
	LogID() api.ID
}

// Client is the hub as the uploader reaches it: the Replica in the same
// process, or a transport client.
type Client interface {
	Append(ctx context.Context, u api.Upload) (api.UploadAck, error)
	Acked(ctx context.Context, log api.ID) (uint64, error)
}

// ErrRewound stops an uploader whose log the hub holds more of than the host
// does: the host's database came back from a backup that held its fence too.
var ErrRewound = errors.New("hub: the hub holds more of this log than the host")

type Uploader struct {
	Host   api.ID
	Epoch  uint64
	Source LogSource
	Hub    Client
	Batch  int // most events per upload
	// MaxBytes caps an upload's encoded events, well under the server's body
	// limit. An event larger than MaxBytes still goes alone; if the server
	// refuses it, the uploader logs the error and retries it forever.
	MaxBytes int
	Interval time.Duration // re-read the log this often without a Notify
	MaxDelay time.Duration // cap on the error backoff
	Logger   *slog.Logger

	wake chan struct{}
}

func NewUploader(host api.ID, epoch uint64, src LogSource, hub Client) *Uploader {
	return &Uploader{
		Host: host, Epoch: epoch, Source: src, Hub: hub,
		Batch: 500, MaxBytes: 8 << 20, Interval: 5 * time.Second, MaxDelay: 30 * time.Second,
		Logger: slog.Default(),
		wake:   make(chan struct{}, 1),
	}
}

// Notify says the log grew. It never blocks.
func (u *Uploader) Notify() {
	select {
	case u.wake <- struct{}{}:
	default:
	}
}

// Run uploads until ctx ends or the hub refuses this host for good: a fenced
// epoch, a log that belongs to another host, or ErrRewound. Every other
// error is retried from the hub's ack.
func (u *Uploader) Run(ctx context.Context) error {
	const minDelay = 100 * time.Millisecond
	delay := minDelay
	var log api.ID
	var acked uint64
	known := false
	// failing is the last error logged, so a retry loop logs each new error
	// once instead of every attempt.
	failing := ""
	for {
		if id := u.Source.LogID(); id != log {
			log, known = id, false
		}
		again, err := func() (bool, error) {
			if !known {
				n, err := u.acked(ctx, log)
				if err != nil {
					return false, err
				}
				acked, known = n, true
			}
			read, err := u.Source.ReadLog(ctx, acked, u.Batch)
			if err != nil || len(read) == 0 {
				return false, err
			}
			evs, err := fit(read, u.MaxBytes)
			if err != nil {
				return false, err
			}
			ack, err := u.Hub.Append(ctx, api.Upload{Host: u.Host, Epoch: u.Epoch, Log: log, Events: evs})
			if gap := (*GapError)(nil); errors.As(err, &gap) {
				acked = gap.Acked
				return true, nil
			}
			if err != nil {
				// The hub may have committed before the reply was lost. Sending
				// the same run again is harmless, so acked stays as it was.
				return false, err
			}
			acked = ack.Acked
			return len(evs) < len(read) || len(read) == u.Batch, nil
		}()

		wait, wake := u.Interval, u.wake
		switch {
		case ctx.Err() != nil:
			return ctx.Err()
		case errors.Is(err, api.ErrStaleEpoch), errors.Is(err, ErrForeignLog), errors.Is(err, ErrRewound):
			return err
		case err != nil:
			if err.Error() != failing {
				failing = err.Error()
				u.Logger.Warn("hub upload failing, retrying", "log", log, "acked", acked, "err", err)
			}
			wait, wake, delay = delay, nil, min(delay*2, u.MaxDelay)
		default:
			if failing != "" {
				failing = ""
				u.Logger.Info("hub upload recovered", "log", log, "acked", acked)
			}
			delay = minDelay
			if again {
				continue
			}
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-wake:
		case <-t.C:
		}
		t.Stop()
	}
}

// Check returns ErrRewound when the hub holds more of the host's current log
// than the host does.
func (u *Uploader) Check(ctx context.Context) error {
	_, err := u.acked(ctx, u.Source.LogID())
	return err
}

// acked is the hub's ack for log, or ErrRewound when the source no longer
// holds the event at it.
func (u *Uploader) acked(ctx context.Context, log api.ID) (uint64, error) {
	acked, err := u.Hub.Acked(ctx, log)
	if err != nil || acked == 0 {
		return acked, err
	}
	last, err := u.Source.ReadLog(ctx, acked-1, 1)
	if err != nil {
		return 0, err
	}
	if len(last) == 0 {
		return 0, ErrRewound
	}
	return acked, nil
}

// fit cuts evs to the longest prefix whose encoding fits in max bytes, but
// never to nothing.
func fit(evs []api.Event, max int) ([]api.Event, error) {
	size := 0
	for i, e := range evs {
		b, err := json.Marshal(e)
		if err != nil {
			return nil, err
		}
		if size += len(b); i > 0 && size > max {
			return evs[:i], nil
		}
	}
	return evs, nil
}

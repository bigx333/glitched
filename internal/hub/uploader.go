package hub

import (
	"context"
	"errors"
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

type Uploader struct {
	Host     api.ID
	Epoch    uint64
	Source   LogSource
	Hub      Client
	Batch    int
	Interval time.Duration // re-read the log this often without a Notify
	MaxDelay time.Duration // cap on the error backoff

	wake chan struct{}
}

func NewUploader(host api.ID, epoch uint64, src LogSource, hub Client) *Uploader {
	return &Uploader{
		Host: host, Epoch: epoch, Source: src, Hub: hub,
		Batch: 500, Interval: 5 * time.Second, MaxDelay: 30 * time.Second,
		wake: make(chan struct{}, 1),
	}
}

// Notify says the log grew. It never blocks.
func (u *Uploader) Notify() {
	select {
	case u.wake <- struct{}{}:
	default:
	}
}

// Run uploads until ctx ends or the hub fences this host's epoch, which is
// the one error it returns. Every other error is retried from the hub's ack.
func (u *Uploader) Run(ctx context.Context) error {
	const minDelay = 100 * time.Millisecond
	delay := minDelay
	var log api.ID
	var acked uint64
	known := false
	for {
		if id := u.Source.LogID(); id != log {
			log, known = id, false
		}
		again, err := func() (bool, error) {
			if !known {
				n, err := u.Hub.Acked(ctx, log)
				if err != nil {
					return false, err
				}
				acked, known = n, true
			}
			evs, err := u.Source.ReadLog(ctx, acked, u.Batch)
			if err != nil || len(evs) == 0 {
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
			return len(evs) == u.Batch, nil
		}()

		wait, wake := u.Interval, u.wake
		switch {
		case ctx.Err() != nil:
			return ctx.Err()
		case errors.Is(err, api.ErrStaleEpoch):
			return err
		case err != nil:
			wait, wake, delay = delay, nil, min(delay*2, u.MaxDelay)
		default:
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

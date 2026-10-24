package store

import (
	"context"
	"database/sql"

	"glitched/internal/api"
	"glitched/internal/engine"
)

// Read answers q from one read transaction, so the projection and the
// events agree with the snapshot's cursor.
func (s *Store) Read(ctx context.Context, q api.Query) (api.Snapshot, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return api.Snapshot{}, err
	}
	defer tx.Rollback()
	head, err := headSeq(ctx, tx)
	if err != nil {
		return api.Snapshot{}, err
	}
	snap := api.Snapshot{Cursor: api.Cursor{Log: s.log, Seq: head}, Threads: []api.Thread{}}
	if q.Thread.IsZero() {
		threads, err := ids(ctx, tx, `SELECT id FROM threads ORDER BY updated_at DESC, id`)
		if err != nil {
			return api.Snapshot{}, err
		}
		for _, id := range threads {
			t, err := loadThread(ctx, tx, id)
			if err != nil {
				return api.Snapshot{}, err
			}
			snap.Threads = append(snap.Threads, engine.View(t))
		}
		return snap, nil
	}
	t, err := loadThread(ctx, tx, q.Thread)
	if err != nil {
		return api.Snapshot{}, err
	}
	if !t.Exists() {
		return api.Snapshot{}, api.ErrNotFound
	}
	snap.Threads = append(snap.Threads, engine.View(t))
	var after uint64
	if q.After.Log == s.log {
		after = q.After.Seq
	}
	snap.Events, err = s.events(ctx, tx, q.Thread, after, head, 0)
	return snap, err
}

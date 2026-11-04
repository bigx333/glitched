package store

import (
	"context"
	"database/sql"
	"strings"

	"glitched/internal/api"
)

// RunText is what a run's agent said in the thread's log: its text deltas,
// in order, read without decoding the thread's other events.
func (s *Store) RunText(ctx context.Context, thread, run api.ID) (string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT json_extract(payload, '$.text') FROM events
		WHERE thread = ? AND type = ? AND json_extract(payload, '$.run') = ? ORDER BY seq`,
		thread[:], api.EventType(api.AssistantDelta{}), run.String())
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var text sql.NullString
		if err := rows.Scan(&text); err != nil {
			return "", err
		}
		b.WriteString(text.String)
	}
	return b.String(), rows.Err()
}

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
		threads, err := ids(ctx, tx, `SELECT id FROM threads WHERE deleted = 0 ORDER BY updated_at DESC, id`)
		if err != nil {
			return api.Snapshot{}, err
		}
		for _, id := range threads {
			t, err := loadThread(ctx, tx, id)
			if err != nil {
				return api.Snapshot{}, err
			}
			snap.Threads = append(snap.Threads, s.view(t))
		}
		return snap, nil
	}
	t, err := loadThread(ctx, tx, q.Thread)
	if err != nil {
		return api.Snapshot{}, err
	}
	if !t.Exists() || t.Deleted {
		return api.Snapshot{}, api.ErrNotFound
	}
	snap.Threads = append(snap.Threads, s.view(t))
	var after uint64
	if q.After.Log == s.log {
		after = q.After.Seq
	}
	snap.Events, err = s.events(ctx, tx, q.Thread, after, head, 0)
	return snap, err
}

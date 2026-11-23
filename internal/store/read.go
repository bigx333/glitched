package store

import (
	"context"
	"database/sql"
	"strings"

	"glitched/internal/api"
)

// RunReply is what a run's agent said last in the thread's log: its text
// deltas after its last tool call, in order, or all of them when nothing
// follows that call. It reads without decoding the thread's other events.
func (s *Store) RunReply(ctx context.Context, thread, run api.ID) (string, error) {
	var last sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT max(seq) FROM events
		WHERE thread = ? AND type = ? AND json_extract(payload, '$.run') = ?`,
		thread[:], api.EventType(api.WorkItem{}), run.String()).Scan(&last); err != nil {
		return "", err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT seq, json_extract(payload, '$.text') FROM events
		WHERE thread = ? AND type = ? AND json_extract(payload, '$.run') = ? ORDER BY seq`,
		thread[:], api.EventType(api.AssistantDelta{}), run.String())
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var all, reply strings.Builder
	for rows.Next() {
		var seq int64
		var text sql.NullString
		if err := rows.Scan(&seq, &text); err != nil {
			return "", err
		}
		all.WriteString(text.String)
		if seq > last.Int64 {
			reply.WriteString(text.String)
		}
	}
	if strings.TrimSpace(reply.String()) == "" {
		return all.String(), rows.Err()
	}
	return reply.String(), rows.Err()
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

// IdleCompactCandidates returns top-level, non-deleted, non-settled idle
// threads whose context is at or above minTokens and not already compacted.
func (s *Store) IdleCompactCandidates(ctx context.Context, minTokens int64) ([]api.ID, error) {
	return ids(ctx, s.db, `SELECT id FROM threads
		WHERE deleted = 0
			AND disposition <> 2
			AND parent IS NULL
			AND context_stale = 0
			AND context_used >= ?
			AND NOT EXISTS (SELECT 1 FROM runs WHERE runs.thread = threads.id AND runs.state <> 'finished')
		ORDER BY id`, minTokens)
}

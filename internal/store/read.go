package store

import (
	"context"
	"database/sql"
	"strings"

	"glitched/internal/api"
)

// RunReply is what a run's agent said last in the thread's log: its text
// deltas between two of its tool calls or plans, or after the last, the
// last of those that says something, in order; or all of them when none
// does. It reads without decoding the thread's other events.
func (s *Store) RunReply(ctx context.Context, thread, run api.ID) (string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT type, json_extract(payload, '$.text') FROM events
		WHERE thread = ? AND type IN (?, ?, ?) AND json_extract(payload, '$.run') = ? ORDER BY seq`,
		thread[:], api.EventType(api.AssistantDelta{}), api.EventType(api.WorkItem{}), api.EventType(api.PlanUpdated{}), run.String())
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var all, said, reply strings.Builder
	for rows.Next() {
		var kind string
		var text sql.NullString
		if err := rows.Scan(&kind, &text); err != nil {
			return "", err
		}
		if kind != api.EventType(api.AssistantDelta{}) {
			said.Reset()
			continue
		}
		all.WriteString(text.String)
		said.WriteString(text.String)
		if strings.TrimSpace(said.String()) != "" {
			reply.Reset()
			reply.WriteString(said.String())
		}
	}
	if reply.Len() == 0 {
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
// threads, not already compacted, whose provider reported a cache to compact
// before or whose context is at or above minTokens.
func (s *Store) IdleCompactCandidates(ctx context.Context, minTokens int64) ([]api.ID, error) {
	return ids(ctx, s.db, `SELECT id FROM threads
		WHERE deleted = 0
			AND disposition <> 2
			AND parent IS NULL
			AND context_stale = 0
			AND (context_cache_expires IS NOT NULL OR context_used >= ?)
			AND NOT EXISTS (SELECT 1 FROM runs WHERE runs.thread = threads.id AND runs.state <> 'finished')
		ORDER BY id`, minTokens)
}

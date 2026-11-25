package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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

// Heads are where each of the thread's runs left its conversation, as its
// turn.ended says, by run, while the thread's conversation still holds
// it, and the runs whose agent compacted it. A run that started a
// conversation of its own, as a portable fork's first run does, leaves
// the heads before it behind.
func (s *Store) Heads(ctx context.Context, thread api.ID) (heads map[api.ID]string, compacted map[api.ID]bool, err error) {
	rows, err := s.db.QueryContext(ctx, `SELECT type, json_extract(payload, '$.run'), json_extract(payload, '$.head'),
			json_extract(payload, '$.session'), COALESCE(json_extract(payload, '$.fork'), json_extract(payload, '$.rewound')) FROM events
		WHERE thread = ? AND (type = ? OR type = ? OR type = ? AND json_extract(payload, '$.compacted') = 1) ORDER BY seq`,
		thread[:], api.EventType(api.TurnEnded{}), api.EventType(api.SessionBound{}), api.EventType(api.ContextReported{}))
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	heads, compacted = map[api.ID]string{}, map[api.ID]bool{}
	session := ""
	for rows.Next() {
		var kind, run string
		var head, bound, fork sql.NullString
		if err := rows.Scan(&kind, &run, &head, &bound, &fork); err != nil {
			return nil, nil, err
		}
		id, err := api.ParseID(run)
		if err != nil {
			return nil, nil, err
		}
		switch kind {
		case api.EventType(api.TurnEnded{}):
			heads[id] = head.String
		case api.EventType(api.SessionBound{}):
			// A conversation of a run's own, not one a native fork
			// forked, holds none of the runs before it.
			if fork.String == string(api.ForkPortable) || fork.String == "" && session != "" && bound.String != session {
				clear(heads)
			}
			session = bound.String
		default:
			compacted[id] = true
		}
	}
	return heads, compacted, rows.Err()
}

// Origin is where thread, a fork, goes on from; nil for any other thread.
func (s *Store) Origin(ctx context.Context, thread api.ID) (*api.Forked, error) {
	return s.origin(ctx, thread, false)
}

// Forked is Origin while none of the fork's runs has taken up the
// conversation it forks; nil once one has.
func (s *Store) Forked(ctx context.Context, thread api.ID) (*api.Forked, error) {
	return s.origin(ctx, thread, true)
}

func (s *Store) origin(ctx context.Context, thread api.ID, pending bool) (*api.Forked, error) {
	var fork []byte
	err := s.db.QueryRowContext(ctx, `SELECT json_extract(payload, '$.fork') FROM events WHERE thread = ? AND type = ? AND json_extract(payload, '$.fork') IS NOT NULL
		AND NOT (? AND EXISTS (SELECT 1 FROM events WHERE thread = ? AND type = ? AND COALESCE(json_extract(payload, '$.fork'), json_extract(payload, '$.rewound')) IS NOT NULL))`,
		thread[:], api.EventType(api.ThreadCreated{}), pending, thread[:], api.EventType(api.SessionBound{})).Scan(&fork)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f api.Forked
	if err := json.Unmarshal(fork, &f); err != nil {
		return nil, err
	}
	return &f, nil
}

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

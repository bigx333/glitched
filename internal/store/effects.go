package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"glitched/internal/api"
	"glitched/internal/engine"
)

// Effect is an outbox row: pending → leased → done. A lease is committed
// before the effect is attempted, so a lease the last process left behind
// means the attempt may have happened.
type Effect struct {
	ID     int64
	Thread api.ID
	Body   engine.Effect
}

// Lease takes the oldest pending effect of a thread that skip, when not nil,
// does not name. ok is false when there is none. skip runs in commit order,
// so it sees what the hooks of every earlier commit did; like a hook, it must
// not block or call the store.
func (s *Store) Lease(ctx context.Context, skip func() []api.ID) (e Effect, ok bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var busy []api.ID
	if skip != nil {
		busy = skip()
	}
	pick := `SELECT id FROM effects WHERE state = 'pending'`
	args := make([]any, len(busy))
	if len(busy) > 0 {
		pick += ` AND thread NOT IN (?` + strings.Repeat(", ?", len(busy)-1) + `)`
		for i, id := range busy {
			args[i] = id[:]
		}
	}
	row := s.db.QueryRowContext(ctx, `UPDATE effects SET state = 'leased'
		WHERE id = (`+pick+` ORDER BY id LIMIT 1)
		RETURNING id, thread, kind, payload`, args...)
	e, err = scanEffect(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Effect{}, false, nil
	}
	return e, err == nil, err
}

// Leased lists effects a previous process leased and never finished.
func (s *Store) Leased(ctx context.Context) ([]Effect, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, thread, kind, payload FROM effects WHERE state = 'leased' ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Effect
	for rows.Next() {
		e, err := scanEffect(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Release returns a leased effect to pending so it runs again.
func (s *Store) Release(ctx context.Context, id int64) error {
	return s.move(ctx, id, "leased", "pending")
}

// Complete marks a leased effect done without recording anything else.
func (s *Store) Complete(ctx context.Context, id int64) error {
	return s.move(ctx, id, "leased", "done")
}

func (s *Store) move(ctx context.Context, id int64, from, to string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return moveEffect(ctx, s.db, id, from, to)
}

func moveEffect(ctx context.Context, x interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, id int64, from, to string) error {
	res, err := x.ExecContext(ctx, `UPDATE effects SET state = ? WHERE id = ? AND state = ?`, to, id, from)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return fmt.Errorf("store: effect %d is not %s", id, from)
	}
	return nil
}

func scanEffect(row interface{ Scan(...any) error }) (Effect, error) {
	var e Effect
	var kind string
	var payload []byte
	if err := row.Scan(&e.ID, idScan{&e.Thread}, &kind, &payload); err != nil {
		return e, err
	}
	body, err := engine.DecodeEffect(kind, payload)
	e.Body = body
	return e, err
}

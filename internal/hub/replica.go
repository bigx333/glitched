// Package hub is the hub role: the replica of every host's log, and the
// uploader that feeds it from a host.
package hub

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"glitched/internal/api"

	_ "modernc.org/sqlite"
)

// GapError refuses an upload that does not continue the replica's copy of the
// log. The uploader resumes from Acked.
type GapError struct{ Acked uint64 }

func (e *GapError) Error() string {
	return fmt.Sprintf("hub: upload leaves a gap after seq %d", e.Acked)
}

// The acked offset of a log is MAX(seq) of its events: Append only ever
// inserts a contiguous run, so the events table is the single record of it.
// n is the hub's own arrival order, which orders reads across a host's logs.
const schema = `
CREATE TABLE IF NOT EXISTS hosts (
	host  BLOB PRIMARY KEY,
	epoch INTEGER NOT NULL
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS events (
	n      INTEGER PRIMARY KEY,
	log    BLOB NOT NULL,
	seq    INTEGER NOT NULL,
	thread BLOB NOT NULL,
	event  BLOB NOT NULL,
	UNIQUE (log, seq)
);
CREATE INDEX IF NOT EXISTS events_thread ON events (thread, n);
`

type Replica struct {
	db *sql.DB

	mu      sync.Mutex
	changed chan struct{} // closed and replaced after every committed append
}

func Open(path string) (*Replica, error) {
	db, err := sql.Open("sqlite", path+"?_journal_mode=WAL&_busy_timeout=10000&_txlock=immediate")
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("hub: schema: %w", err)
	}
	return &Replica{db: db, changed: make(chan struct{})}, nil
}

func (r *Replica) Close() error { return r.db.Close() }

// Append stores the part of u the replica does not hold yet and returns the
// new acked offset. A replayed upload changes nothing.
func (r *Replica) Append(ctx context.Context, u api.Upload) (api.UploadAck, error) {
	for i, e := range u.Events {
		if e.Cursor.Log != u.Log || (i > 0 && e.Cursor.Seq != u.Events[i-1].Cursor.Seq+1) {
			return api.UploadAck{}, fmt.Errorf("hub: upload is not one contiguous run of log %s", u.Log)
		}
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return api.UploadAck{}, err
	}
	defer tx.Rollback()

	var epoch uint64
	switch err := tx.QueryRowContext(ctx, `SELECT epoch FROM hosts WHERE host = ?`, u.Host[:]).Scan(&epoch); {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return api.UploadAck{}, err
	case u.Epoch < epoch:
		return api.UploadAck{}, api.ErrStaleEpoch
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO hosts (host, epoch) VALUES (?, ?)
		ON CONFLICT (host) DO UPDATE SET epoch = excluded.epoch`, u.Host[:], u.Epoch); err != nil {
		return api.UploadAck{}, err
	}
	acked, err := ackedTx(ctx, tx, u.Log)
	if err != nil {
		return api.UploadAck{}, err
	}

	fresh := u.Events
	for len(fresh) > 0 && fresh[0].Cursor.Seq <= acked {
		fresh = fresh[1:]
	}
	if len(fresh) > 0 && fresh[0].Cursor.Seq != acked+1 {
		return api.UploadAck{}, &GapError{Acked: acked}
	}
	for _, e := range fresh {
		body, err := json.Marshal(e)
		if err != nil {
			return api.UploadAck{}, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO events (log, seq, thread, event) VALUES (?, ?, ?, ?)`,
			u.Log[:], e.Cursor.Seq, e.Thread[:], body); err != nil {
			return api.UploadAck{}, err
		}
		acked = e.Cursor.Seq
	}
	if err := tx.Commit(); err != nil {
		return api.UploadAck{}, err
	}
	if len(fresh) > 0 {
		r.mu.Lock()
		close(r.changed)
		r.changed = make(chan struct{})
		r.mu.Unlock()
	}
	return api.UploadAck{Acked: acked}, nil
}

// Acked is the highest contiguous seq the replica holds for log, 0 if none,
// so a log new to the hub (a restored database) starts from 0.
func (r *Replica) Acked(ctx context.Context, log api.ID) (uint64, error) {
	return ackedTx(ctx, r.db, log)
}

type querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func ackedTx(ctx context.Context, q querier, log api.ID) (uint64, error) {
	var acked uint64
	err := q.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) FROM events WHERE log = ?`, log[:]).Scan(&acked)
	return acked, err
}

// Events returns up to limit events after the cursor, in the order the hub
// received them: one thread's, or every thread's when thread is zero. A zero
// cursor starts at the beginning.
func (r *Replica) Events(ctx context.Context, thread api.ID, after api.Cursor, limit int) ([]api.Event, error) {
	n, err := r.position(ctx, after)
	if err != nil {
		return nil, err
	}
	evs, _, err := r.eventsAfter(ctx, thread, n, limit)
	return evs, err
}

// position maps a cursor to the hub's arrival order: the last event held at
// or before it in its log, or just before the log's first event.
func (r *Replica) position(ctx context.Context, c api.Cursor) (int64, error) {
	if c == (api.Cursor{}) {
		return 0, nil
	}
	var n sql.NullInt64
	err := r.db.QueryRowContext(ctx, `SELECT COALESCE(
		(SELECT MAX(n) FROM events WHERE log = ?1 AND seq <= ?2),
		(SELECT MIN(n) - 1 FROM events WHERE log = ?1))`, c.Log[:], c.Seq).Scan(&n)
	if err != nil {
		return 0, err
	}
	if !n.Valid {
		return 0, api.ErrNotFound
	}
	return n.Int64, nil
}

func (r *Replica) eventsAfter(ctx context.Context, thread api.ID, n int64, limit int) ([]api.Event, int64, error) {
	q, args := `SELECT n, event FROM events WHERE n > ? ORDER BY n LIMIT ?`, []any{n, limit}
	if !thread.IsZero() {
		q, args = `SELECT n, event FROM events WHERE thread = ? AND n > ? ORDER BY n LIMIT ?`, []any{thread[:], n, limit}
	}
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, n, err
	}
	defer rows.Close()
	var evs []api.Event
	for rows.Next() {
		var body []byte
		var e api.Event
		if err := rows.Scan(&n, &body); err != nil {
			return nil, n, err
		}
		if err := json.Unmarshal(body, &e); err != nil {
			return nil, n, err
		}
		evs = append(evs, e)
	}
	return evs, n, rows.Err()
}

// Feed is a live read of the replica. It never drops events: a wake only
// tells it to read again from where it stopped.
type Feed struct {
	r      *Replica
	thread api.ID
	n      int64
	buf    []api.Event
}

// Follow returns the events after the cursor, then each later append as it
// commits. thread and after mean what they mean for Events.
func (r *Replica) Follow(ctx context.Context, thread api.ID, after api.Cursor) (*Feed, error) {
	n, err := r.position(ctx, after)
	if err != nil {
		return nil, err
	}
	return &Feed{r: r, thread: thread, n: n}, nil
}

func (f *Feed) Next(ctx context.Context) (api.Event, error) {
	for len(f.buf) == 0 {
		f.r.mu.Lock()
		changed := f.r.changed
		f.r.mu.Unlock()
		evs, n, err := f.r.eventsAfter(ctx, f.thread, f.n, 256)
		if err != nil {
			return api.Event{}, err
		}
		f.buf, f.n = evs, n
		if len(evs) > 0 {
			break
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return api.Event{}, ctx.Err()
		}
	}
	e := f.buf[0]
	f.buf = f.buf[1:]
	return e, nil
}

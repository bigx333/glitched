// Package hub is the hub role: the replica of every host's log, and the
// uploader that feeds it from a host.
package hub

import (
	"bytes"
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

// ErrForeignLog refuses an upload of a log that another host appended first.
var ErrForeignLog = errors.New("hub: log belongs to another host")

// The acked offset of a log is MAX(seq) of its events: Append only ever
// inserts a contiguous run, so the events table is the single record of it.
// n is the hub's own arrival order, which orders reads across a host's logs.
const schema = `
CREATE TABLE IF NOT EXISTS hosts (
	host  BLOB PRIMARY KEY,
	epoch INTEGER NOT NULL
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS logs (
	log  BLOB PRIMARY KEY,
	host BLOB NOT NULL
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
CREATE TABLE IF NOT EXISTS tombstones (
	log    BLOB NOT NULL,
	thread BLOB NOT NULL,
	PRIMARY KEY (log, thread)
) WITHOUT ROWID;
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
// new acked offset. A replayed upload changes nothing; one that replays
// another event at a seq the replica holds is ErrRewound.
func (r *Replica) Append(ctx context.Context, u api.Upload) (api.UploadAck, error) {
	if u.Protocol != api.Protocol {
		return api.UploadAck{}, fmt.Errorf("%w: the host speaks protocol %d, this hub %d", api.ErrProtocol, u.Protocol, api.Protocol)
	}
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
	if _, err := tx.ExecContext(ctx, `INSERT INTO logs (log, host) VALUES (?, ?) ON CONFLICT (log) DO NOTHING`,
		u.Log[:], u.Host[:]); err != nil {
		return api.UploadAck{}, err
	}
	var owner []byte
	if err := tx.QueryRowContext(ctx, `SELECT host FROM logs WHERE log = ?`, u.Log[:]).Scan(&owner); err != nil {
		return api.UploadAck{}, err
	}
	if !bytes.Equal(owner, u.Host[:]) {
		return api.UploadAck{}, ErrForeignLog
	}
	acked, err := ackedTx(ctx, tx, u.Log)
	if err != nil {
		return api.UploadAck{}, err
	}

	fresh := u.Events
	for len(fresh) > 0 && fresh[0].Cursor.Seq <= acked {
		if err := holds(ctx, tx, fresh[0]); err != nil {
			return api.UploadAck{}, err
		}
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
		if e.Type == api.EventType(api.ThreadDeleted{}) {
			if _, err := tx.ExecContext(ctx, `INSERT INTO tombstones (log, thread) VALUES (?, ?) ON CONFLICT DO NOTHING`, u.Log[:], e.Thread[:]); err != nil {
				return api.UploadAck{}, err
			}
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
	return api.UploadAck{Acked: acked, Protocol: api.Protocol}, nil
}

// holds returns ErrRewound unless the replica holds e at its seq.
func holds(ctx context.Context, q querier, e api.Event) error {
	body, err := json.Marshal(e)
	if err != nil {
		return err
	}
	var held []byte
	if err := q.QueryRowContext(ctx, `SELECT event FROM events WHERE log = ? AND seq = ?`,
		e.Cursor.Log[:], e.Cursor.Seq).Scan(&held); err != nil {
		return err
	}
	if !bytes.Equal(body, held) {
		return fmt.Errorf("hub: seq %d of log %s is not the event the hub holds: %w", e.Cursor.Seq, e.Cursor.Log, ErrRewound)
	}
	return nil
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
// cursor starts at the beginning, or for a thread at the start of the last
// log that holds its events.
func (r *Replica) Events(ctx context.Context, thread api.ID, after api.Cursor, limit int) ([]api.Event, error) {
	n, err := r.start(ctx, thread, after)
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

// start places a read of thread, or of every thread, after the cursor. A
// thread read that starts once the cursor's log holds the thread's
// tombstone is ErrNotFound, as the host's is; a Feed open before then reads
// on to the tombstone and ends. The whole log still holds the thread's
// events, which it must to stay the host's copy. The tombstone is the log's:
// a restored host's new log may hold the thread again.
func (r *Replica) start(ctx context.Context, thread api.ID, after api.Cursor) (int64, error) {
	if !thread.IsZero() && after == (api.Cursor{}) {
		var log []byte
		switch err := r.db.QueryRowContext(ctx, `SELECT log FROM events WHERE thread = ? ORDER BY n DESC LIMIT 1`, thread[:]).Scan(&log); {
		case errors.Is(err, sql.ErrNoRows):
			return 0, nil
		case err != nil:
			return 0, err
		}
		after.Log = api.ID(log)
	}
	n, err := r.position(ctx, after)
	if err != nil || thread.IsZero() {
		return n, err
	}
	var gone int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tombstones WHERE log = ? AND thread = ?`, after.Log[:], thread[:]).Scan(&gone); err != nil {
		return 0, err
	}
	if gone > 0 {
		return 0, api.ErrNotFound
	}
	return n, nil
}

// eventsAfter reads the replica in arrival order.
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
	over   bool // the thread's tombstone was read: nothing follows it
}

// Follow returns the events after the cursor, then each later append as it
// commits. thread and after mean what they mean for Events. A thread's
// feed ends with ErrNotFound after the thread's tombstone.
func (r *Replica) Follow(ctx context.Context, thread api.ID, after api.Cursor) (*Feed, error) {
	n, err := r.start(ctx, thread, after)
	if err != nil {
		return nil, err
	}
	return &Feed{r: r, thread: thread, n: n}, nil
}

func (f *Feed) Next(ctx context.Context) (api.Event, error) {
	if f.over {
		return api.Event{}, api.ErrNotFound
	}
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
	if !f.thread.IsZero() && e.Type == api.EventType(api.ThreadDeleted{}) {
		f.over, f.buf = true, nil
	}
	return e, nil
}

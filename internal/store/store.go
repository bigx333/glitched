// Package store keeps a host's log, projections, command receipts and effect
// outbox in one SQLite database. Every write is one transaction, so a crash
// leaves either all of a decision or none of it.
package store

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"glitched/internal/api"
	"glitched/internal/engine"
)

type Store struct {
	db    *sql.DB
	fence *os.File
	log   api.ID
	host  api.ID

	// mu admits one writer at a time, which makes commit order the order
	// hooks see events in.
	mu    sync.Mutex
	hooks []func([]api.Event)
}

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_journal_mode=WAL&_busy_timeout=5000&_foreign_keys=1&_txlock=immediate")
	if err != nil {
		return nil, err
	}
	fence, err := os.OpenFile(path+".fence", os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		db.Close()
		return nil, err
	}
	s := &Store{db: db, fence: fence}
	if err := s.migrate(); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return errors.Join(s.db.Close(), s.fence.Close()) }

// LogID names this log. A restored database gets a new one, which fences
// cursors taken against the old log.
func (s *Store) LogID() api.ID { return s.log }

func (s *Store) HostID() api.ID { return s.host }

// Rewind does what Open does for a restore its fence shows, for a database
// another party proved is behind what it handed out: a new log id, and every
// live run uncertain. Call it before a Host runs on the store.
func (s *Store) Rewind(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	prev := s.log
	head, err := s.rewind(ctx, tx)
	if err == nil {
		err = s.writeFence(head)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		s.log = prev
	}
	return err
}

// OnCommit registers fn to run after every commit that wrote events, in
// commit order, while the next writer waits. fn must not block or write.
func (s *Store) OnCommit(fn func([]api.Event)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hooks = append(s.hooks, fn)
}

// ReadLog returns up to limit events with Seq > after, in order. A limit of
// zero or less means no limit.
func (s *Store) ReadLog(ctx context.Context, after uint64, limit int) ([]api.Event, error) {
	events, _, err := s.Since(ctx, api.ID{}, after, limit)
	return events, err
}

// Since returns up to limit events with Seq > after for one thread, or for
// every thread when thread is zero, and the log head they were read at.
func (s *Store) Since(ctx context.Context, thread api.ID, after uint64, limit int) ([]api.Event, uint64, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, 0, err
	}
	defer tx.Rollback()
	head, err := headSeq(ctx, tx)
	if err != nil {
		return nil, 0, err
	}
	events, err := s.events(ctx, tx, thread, after, head, limit)
	return events, head, err
}

const schema = `
CREATE TABLE meta (
	key   TEXT PRIMARY KEY,
	value BLOB NOT NULL
) STRICT;

CREATE TABLE events (
	seq     INTEGER PRIMARY KEY CHECK (seq > 0),
	thread  BLOB NOT NULL,
	type    TEXT NOT NULL,
	schema  INTEGER NOT NULL,
	at      INTEGER NOT NULL,
	payload BLOB NOT NULL
) STRICT;
CREATE INDEX events_by_thread ON events (thread, seq);

CREATE TABLE commands (
	id   BLOB PRIMARY KEY,
	hash BLOB NOT NULL,
	seq  INTEGER NOT NULL
) STRICT;

CREATE TABLE threads (
	id          BLOB PRIMARY KEY,
	workspace   TEXT NOT NULL,
	title       TEXT NOT NULL,
	model       TEXT NOT NULL,
	access      INTEGER NOT NULL CHECK (access BETWEEN 0 AND 2),
	revision    INTEGER NOT NULL,
	owner_epoch INTEGER NOT NULL CHECK (owner_epoch > 0),
	generation  INTEGER NOT NULL,
	disposition INTEGER NOT NULL CHECK (disposition BETWEEN 0 AND 2),
	until       INTEGER,
	updated_at  INTEGER NOT NULL,
	CHECK ((disposition = 1) = (until IS NOT NULL))
) STRICT;

CREATE TABLE runs (
	thread     BLOB NOT NULL REFERENCES threads (id),
	ord        INTEGER NOT NULL,
	id         BLOB NOT NULL UNIQUE,
	generation INTEGER NOT NULL,
	state      TEXT NOT NULL CHECK (state IN ('starting', 'running', 'waiting', 'stopping', 'uncertain', 'finished')),
	signalled  INTEGER NOT NULL CHECK (signalled IN (0, 1)),
	outcome    INTEGER CHECK (outcome BETWEEN 0 AND 3),
	error      TEXT NOT NULL,
	PRIMARY KEY (thread, ord),
	CHECK ((state = 'finished') = (outcome IS NOT NULL))
) STRICT;
CREATE UNIQUE INDEX runs_one_live ON runs (thread) WHERE state <> 'finished';

CREATE TABLE messages (
	thread BLOB NOT NULL REFERENCES threads (id),
	ord    INTEGER NOT NULL,
	id     BLOB NOT NULL UNIQUE,
	text   TEXT NOT NULL,
	mode   INTEGER NOT NULL CHECK (mode BETWEEN 0 AND 4),
	pos    INTEGER NOT NULL,
	state  TEXT NOT NULL CHECK (state IN ('ready', 'held', 'claimed', 'dispatched', 'cancelled', 'uncertain')),
	run    BLOB,
	PRIMARY KEY (thread, ord),
	CHECK (state IN ('ready', 'held', 'cancelled') OR run IS NOT NULL)
) STRICT;

CREATE TABLE requests (
	thread     BLOB NOT NULL REFERENCES threads (id),
	ord        INTEGER NOT NULL,
	id         BLOB NOT NULL UNIQUE,
	run        BLOB NOT NULL,
	generation INTEGER NOT NULL,
	dedup      TEXT NOT NULL,
	kind       TEXT NOT NULL,
	body       BLOB,
	answer     BLOB,
	state      INTEGER NOT NULL CHECK (state BETWEEN 0 AND 4),
	PRIMARY KEY (thread, ord),
	UNIQUE (run, dedup),
	CHECK (state NOT IN (1, 2) OR answer IS NOT NULL)
) STRICT;

CREATE TABLE effects (
	id      INTEGER PRIMARY KEY AUTOINCREMENT,
	thread  BLOB NOT NULL,
	kind    TEXT NOT NULL,
	payload BLOB NOT NULL,
	state   TEXT NOT NULL CHECK (state IN ('pending', 'leased', 'done'))
) STRICT;
CREATE INDEX effects_open ON effects (state, id) WHERE state <> 'done';
`

func (s *Store) migrate() error {
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var version int
	if err := tx.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	switch version {
	case 0:
		if _, err := tx.ExecContext(ctx, schema+`PRAGMA user_version = 1;`); err != nil {
			return fmt.Errorf("store: create schema: %w", err)
		}
		for _, key := range []string{"log", "host"} {
			id := api.NewID()
			if _, err := tx.ExecContext(ctx, `INSERT INTO meta (key, value) VALUES (?, ?)`, key, id[:]); err != nil {
				return err
			}
		}
	case 1:
	default:
		return fmt.Errorf("store: schema version %d is newer than this build", version)
	}
	for key, dst := range map[string]*api.ID{"log": &s.log, "host": &s.host} {
		if err := tx.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, key).Scan(idScan{dst}); err != nil {
			return fmt.Errorf("store: meta %s: %w", key, err)
		}
	}
	head, err := headSeq(ctx, tx)
	if err != nil {
		return err
	}
	if s.restored(head) {
		if head, err = s.rewind(ctx, tx); err != nil {
			return err
		}
	}
	if err := s.writeFence(head); err != nil {
		return err
	}
	return tx.Commit()
}

// rewind gives the log a new id, for a database that is behind what its
// clients saw, and makes every run it left live uncertain: what it holds in
// flight may already have reached the agent in the history it lost. It
// returns the new head.
func (s *Store) rewind(ctx context.Context, tx *sql.Tx) (uint64, error) {
	s.log = api.NewID()
	if _, err := tx.ExecContext(ctx, `UPDATE meta SET value = ? WHERE key = 'log'`, s.log[:]); err != nil {
		return 0, err
	}
	threads, err := ids(ctx, tx, `SELECT thread FROM runs WHERE state NOT IN ('finished', 'uncertain') ORDER BY thread`)
	if err != nil {
		return 0, err
	}
	now := time.Now()
	for _, id := range threads {
		if _, _, err := s.write(ctx, tx, id, func(t engine.Thread) ([]engine.Event, []engine.Effect, error) {
			return engine.Observe(t, engine.Reconciled{Run: t.Live().ID}, now)
		}); err != nil {
			return 0, err
		}
	}
	return headSeq(ctx, tx)
}

// The fence file beside the database holds the log id and the highest seq
// handed out under it. A restored copy brings its own meta table back, so
// only a file outside the database can tell that the copy is behind what
// clients have seen.
const fenceSize = len(api.ID{}) + 8

// restored reports whether the database is not the one the fence last saw,
// or is behind it. A missing or torn fence on a database with events counts
// as a restore: a new log id costs clients one snapshot, a reused one hands
// them the wrong events.
func (s *Store) restored(head uint64) bool {
	var b [fenceSize]byte
	if n, _ := s.fence.ReadAt(b[:], 0); n != fenceSize {
		return head > 0
	}
	return api.ID(b[:len(s.log)]) != s.log || head < binary.BigEndian.Uint64(b[len(s.log):])
}

// writeFence records head as handed out under the log, durably, before the
// transaction that writes it commits: no reader sees a seq the fence doesn't
// cover. A transaction that then fails leaves the fence ahead, which the
// next open takes for a restore.
func (s *Store) writeFence(head uint64) error {
	var b [fenceSize]byte
	copy(b[:], s.log[:])
	binary.BigEndian.PutUint64(b[len(s.log):], head)
	if _, err := s.fence.WriteAt(b[:], 0); err != nil {
		return fmt.Errorf("store: fence: %w", err)
	}
	return s.fence.Sync()
}

type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func headSeq(ctx context.Context, q querier) (uint64, error) {
	var head uint64
	err := q.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) FROM events`).Scan(&head)
	return head, err
}

func (s *Store) events(ctx context.Context, q querier, thread api.ID, after, upto uint64, limit int) ([]api.Event, error) {
	query := `SELECT seq, thread, type, schema, at, payload FROM events WHERE seq > ? AND seq <= ?`
	args := []any{after, upto}
	if !thread.IsZero() {
		query += ` AND thread = ?`
		args = append(args, thread[:])
	}
	if limit <= 0 {
		limit = -1
	}
	rows, err := q.QueryContext(ctx, query+` ORDER BY seq LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []api.Event
	for rows.Next() {
		e := api.Event{Cursor: api.Cursor{Log: s.log}}
		var at int64
		if err := rows.Scan(&e.Cursor.Seq, idScan{&e.Thread}, &e.Type, &e.Schema, &at, (*[]byte)(&e.Payload)); err != nil {
			return nil, err
		}
		e.At = fromNanos(at)
		out = append(out, e)
	}
	return out, rows.Err()
}

type idScan struct{ dst *api.ID }

func (s idScan) Scan(v any) error {
	switch b := v.(type) {
	case nil:
		*s.dst = api.ID{}
		return nil
	case []byte:
		if len(b) == len(s.dst) {
			copy(s.dst[:], b)
			return nil
		}
	}
	return errors.New("store: malformed id column")
}

// nullID stores the zero ID as NULL.
func nullID(id api.ID) any {
	if id.IsZero() {
		return nil
	}
	return id[:]
}

// nullBlob keeps a nil payload NULL rather than an empty blob.
func nullBlob(b []byte) any {
	if b == nil {
		return nil
	}
	return b
}

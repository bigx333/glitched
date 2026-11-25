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
	"io"
	"os"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"glitched/internal/api"
	"glitched/internal/engine"
)

type Store struct {
	db    *sql.DB
	fence fenceFile
	log   api.ID
	host  api.ID

	// mu admits one writer at a time, which makes commit order the order
	// hooks see events in.
	mu     sync.Mutex
	hooks  []func(Commit)
	limits engine.Limits
	places engine.Places // where a remote child's create finds its machine
	links  engine.Links  // the links this engine minted, as they are at each commit
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
// another party proved is behind what it handed out: a new log id, every
// live run uncertain and the messages queued behind it cancelled. Call it
// before a Host runs on the store.
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
func (s *Store) OnCommit(fn func(Commit)) {
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
// every thread when thread is zero, and the log head they were read at. A
// deleted thread's read is ErrNotFound, wherever after is: only a stream
// that was open when the tombstone committed sees it.
func (s *Store) Since(ctx context.Context, thread api.ID, after uint64, limit int) ([]api.Event, uint64, error) {
	events, head, _, err := s.since(ctx, thread, after, limit, false)
	return events, head, err
}

// SinceView is Since of one thread, with its view at the same head, zero
// while it doesn't exist.
func (s *Store) SinceView(ctx context.Context, thread api.ID, after uint64, limit int) ([]api.Event, uint64, api.Thread, error) {
	return s.since(ctx, thread, after, limit, true)
}

func (s *Store) since(ctx context.Context, thread api.ID, after uint64, limit int, withView bool) ([]api.Event, uint64, api.Thread, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, 0, api.Thread{}, err
	}
	defer tx.Rollback()
	if !thread.IsZero() {
		var gone bool
		switch err := tx.QueryRowContext(ctx, `SELECT deleted FROM threads WHERE id = ?`, thread[:]).Scan(&gone); {
		case errors.Is(err, sql.ErrNoRows):
		case err != nil:
			return nil, 0, api.Thread{}, err
		case gone:
			return nil, 0, api.Thread{}, api.ErrNotFound
		}
	}
	var view api.Thread
	if withView {
		t, err := loadThread(ctx, tx, thread)
		if err != nil {
			return nil, 0, api.Thread{}, err
		}
		if t.Exists() {
			view = s.view(t)
		}
	}
	head, err := headSeq(ctx, tx)
	if err != nil {
		return nil, 0, api.Thread{}, err
	}
	events, err := s.events(ctx, tx, thread, after, head, limit)
	return events, head, view, err
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
	id       BLOB PRIMARY KEY,
	hash     BLOB NOT NULL,
	seq      INTEGER NOT NULL,
	revision INTEGER
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
	deleted     INTEGER NOT NULL DEFAULT 0 CHECK (deleted IN (0, 1)),
	parent      BLOB,
	tokens      INTEGER NOT NULL DEFAULT 0,
	reported    INTEGER,
	report      TEXT,
	session     TEXT NOT NULL DEFAULT '',
	project     TEXT NOT NULL DEFAULT '',
	branch      TEXT NOT NULL DEFAULT '',
	context_used   INTEGER NOT NULL DEFAULT 0,
	context_window INTEGER NOT NULL DEFAULT 0,
	context_stale  INTEGER NOT NULL DEFAULT 0 CHECK (context_stale IN (0, 1)),
	context_model  TEXT NOT NULL DEFAULT '',
	granted     TEXT,
	context_cache_expires INTEGER,
	remote      TEXT,
	origin      TEXT,
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
	ended      INTEGER,
	model      TEXT,
	access     INTEGER CHECK (access BETWEEN 0 AND 2),
	silent     INTEGER NOT NULL DEFAULT 0 CHECK (silent IN (0, 1)),
	limit_resets     INTEGER,
	resume_cancelled INTEGER NOT NULL DEFAULT 0 CHECK (resume_cancelled IN (0, 1)),
	dropped    INTEGER NOT NULL DEFAULT 0 CHECK (dropped IN (0, 1)),
	PRIMARY KEY (thread, ord),
	CHECK ((state = 'finished') = (outcome IS NOT NULL)),
	CHECK ((state = 'finished') = (ended IS NOT NULL))
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

// schemaVersion is the user_version of a database this build created or
// upgraded; upgrades[v] takes one at version v to v+1.
const schemaVersion = 16

var upgrades = map[int]string{
	// Archived threads: disposition 3, which upgrade 3 drops. SQLite
	// changes a CHECK only by rebuilding the table.
	1: `
CREATE TABLE threads_v2 (
	id          BLOB PRIMARY KEY,
	workspace   TEXT NOT NULL,
	title       TEXT NOT NULL,
	model       TEXT NOT NULL,
	access      INTEGER NOT NULL CHECK (access BETWEEN 0 AND 2),
	revision    INTEGER NOT NULL,
	owner_epoch INTEGER NOT NULL CHECK (owner_epoch > 0),
	generation  INTEGER NOT NULL,
	disposition INTEGER NOT NULL CHECK (disposition BETWEEN 0 AND 3),
	until       INTEGER,
	updated_at  INTEGER NOT NULL,
	CHECK ((disposition = 1) = (until IS NOT NULL))
) STRICT;
INSERT INTO threads_v2 SELECT * FROM threads;
DROP TABLE threads;
ALTER TABLE threads_v2 RENAME TO threads;
`,
	// Deleted threads: the tombstone.
	2: `ALTER TABLE threads ADD COLUMN deleted INTEGER NOT NULL DEFAULT 0 CHECK (deleted IN (0, 1));`,
	// No more archived threads: those there were are settled.
	3: `
CREATE TABLE threads_v4 (
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
	deleted     INTEGER NOT NULL DEFAULT 0 CHECK (deleted IN (0, 1)),
	CHECK ((disposition = 1) = (until IS NOT NULL))
) STRICT;
INSERT INTO threads_v4 SELECT id, workspace, title, model, access, revision, owner_epoch, generation,
	CASE disposition WHEN 3 THEN 2 ELSE disposition END, until, updated_at, deleted FROM threads;
DROP TABLE threads;
ALTER TABLE threads_v4 RENAME TO threads;
`,
	// Child threads: the parent, the tokens used, the last report.
	4: `
ALTER TABLE threads ADD COLUMN parent BLOB;
ALTER TABLE threads ADD COLUMN tokens INTEGER NOT NULL DEFAULT 0;
ALTER TABLE threads ADD COLUMN reported INTEGER;
`,
	// A parent's last report, as its log's last child.reported has it.
	5: `
ALTER TABLE threads ADD COLUMN report TEXT;
UPDATE threads SET report = (
	SELECT json_object('child', json_extract(CAST(e.payload AS TEXT), '$.child'),
		'kind', json_extract(CAST(e.payload AS TEXT), '$.kind'), 'seq', e.seq)
	FROM events e
	WHERE e.thread = threads.id AND e.type = 'child.reported'
	ORDER BY e.seq DESC LIMIT 1);
`,
	// The model and access each run was admitted at, as its thread's log
	// has them: those of the create or configure last before the run's
	// admission. A run whose admission the log doesn't show keeps neither.
	6: `
ALTER TABLE runs ADD COLUMN model TEXT;
ALTER TABLE runs ADD COLUMN access INTEGER CHECK (access BETWEEN 0 AND 2);
UPDATE runs SET model = a.model, access = a.access
FROM (
	SELECT m.thread, m.generation,
		json_extract(CAST(e.payload AS TEXT), '$.model') AS model,
		json_extract(CAST(e.payload AS TEXT), '$.access') AS access
	FROM (
		SELECT thread, type, json_extract(CAST(payload AS TEXT), '$.generation') AS generation,
			MAX(CASE WHEN type <> 'turn.admitted' THEN seq END) OVER (PARTITION BY thread ORDER BY seq) AS set_at
		FROM events
		WHERE type IN ('thread.created', 'thread.configured', 'turn.admitted')
	) m JOIN events e ON e.seq = m.set_at
	WHERE m.type = 'turn.admitted'
) a
WHERE a.thread = runs.thread AND a.generation = runs.generation;
`,
	// The target thread's revision after each command, which its receipt
	// carries. A receipt stored before has none.
	7: `ALTER TABLE commands ADD COLUMN revision INTEGER;`,
	// The provider's id for each thread's conversation (SessionBound).
	8: `ALTER TABLE threads ADD COLUMN session TEXT NOT NULL DEFAULT '';`,
	// The folder a thread in a worktree came from, and its branch.
	9: `
ALTER TABLE threads ADD COLUMN project TEXT NOT NULL DEFAULT '';
ALTER TABLE threads ADD COLUMN branch TEXT NOT NULL DEFAULT '';
`,
	// How full each thread's context is (ContextReported).
	10: `
ALTER TABLE threads ADD COLUMN context_used INTEGER NOT NULL DEFAULT 0;
ALTER TABLE threads ADD COLUMN context_window INTEGER NOT NULL DEFAULT 0;
ALTER TABLE threads ADD COLUMN context_stale INTEGER NOT NULL DEFAULT 0 CHECK (context_stale IN (0, 1));
ALTER TABLE threads ADD COLUMN context_model TEXT NOT NULL DEFAULT '';
`,
	// What each thread's provider last confirmed its agent runs at
	// (AccessGranted, ModelConfirmed), as api.Confirmed's JSON. A thread
	// upgraded has none: nothing confirmed what its runs ran at.
	11: `ALTER TABLE threads ADD COLUMN granted TEXT;`,
	// Prompt cache expiry on threads and silent maintenance runs (idle compaction).
	12: `
ALTER TABLE threads ADD COLUMN context_cache_expires INTEGER;
ALTER TABLE runs ADD COLUMN silent INTEGER NOT NULL DEFAULT 0 CHECK (silent IN (0, 1));
`,
	// The usage limit that ended a run: limit_resets is when it resets,
	// 0 where the provider didn't say, and NULL on a run no limit ended.
	13: `
ALTER TABLE runs ADD COLUMN limit_resets INTEGER;
ALTER TABLE runs ADD COLUMN resume_cancelled INTEGER NOT NULL DEFAULT 0 CHECK (resume_cancelled IN (0, 1));
`,
	// The runs a rewind took out of their thread's conversation. No build
	// before rewound one, so none is.
	14: `ALTER TABLE runs ADD COLUMN dropped INTEGER NOT NULL DEFAULT 0 CHECK (dropped IN (0, 1));`,
	// A remote child's placeholder, as engine.Remote's JSON, and, on the
	// machine it runs on, where such a child came from. No build before
	// placed one.
	15: `
ALTER TABLE threads ADD COLUMN remote TEXT;
ALTER TABLE threads ADD COLUMN origin TEXT;
`,
}

func (s *Store) migrate() error {
	ctx := context.Background()
	if err := s.upgrade(ctx); err != nil {
		return err
	}
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
		if _, err := tx.ExecContext(ctx, schema+fmt.Sprintf(`PRAGMA user_version = %d;`, schemaVersion)); err != nil {
			return fmt.Errorf("store: create schema: %w", err)
		}
		for _, key := range []string{"log", "host"} {
			id := api.NewID()
			if _, err := tx.ExecContext(ctx, `INSERT INTO meta (key, value) VALUES (?, ?)`, key, id[:]); err != nil {
				return err
			}
		}
	case schemaVersion:
	default:
		return fmt.Errorf("store: this database is from a newer Glitched (schema version %d; this build reads up to %d). Open it with that build or a later one", version, schemaVersion)
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

// upgrade brings a database an older build created to schemaVersion, one
// transaction per version. It runs with foreign keys off on its own
// connection, as SQLite's way to rebuild a table that others reference
// asks, and checks them before each commit.
func (s *Store) upgrade(ctx context.Context) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	var version int
	if err := conn.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	if version == 0 || version >= schemaVersion {
		return nil
	}
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		return err
	}
	defer conn.ExecContext(ctx, `PRAGMA foreign_keys = ON`)
	for ; version < schemaVersion; version++ {
		if err := upgradeOnce(ctx, conn, version); err != nil {
			return fmt.Errorf("store: upgrade from version %d: %w", version, err)
		}
	}
	return nil
}

func upgradeOnce(ctx context.Context, conn *sql.Conn, version int) error {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, upgrades[version]+fmt.Sprintf(`PRAGMA user_version = %d;`, version+1)); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return err
	}
	broken := rows.Next()
	rows.Close()
	if broken {
		return errors.New("a foreign key no longer holds")
	}
	return tx.Commit()
}

// rewind gives the log a new id, for a database that is behind what its
// clients saw. Every run it left live becomes uncertain and every message
// of the user's waiting its turn, ready or held, is cancelled: what it
// holds in flight may already have reached the agent in the history it
// lost, and what it holds queued may already have run there, after a send
// or a resume. It returns the new head.
func (s *Store) rewind(ctx context.Context, tx *sql.Tx) (uint64, error) {
	s.log = api.NewID()
	if _, err := tx.ExecContext(ctx, `UPDATE meta SET value = ? WHERE key = 'log'`, s.log[:]); err != nil {
		return 0, err
	}
	threads, err := ids(ctx, tx, `SELECT thread FROM runs WHERE state <> 'finished'
		UNION SELECT m.thread FROM messages m JOIN threads t ON t.id = m.thread
		WHERE m.state = 'held' AND m.mode = ? AND NOT t.deleted ORDER BY 1`, api.Defer)
	if err != nil {
		return 0, err
	}
	now := time.Now()
	for _, id := range threads {
		if _, _, err := s.write(ctx, tx, id, func(q querier, t engine.Thread) ([]engine.Event, []engine.Effect, error) {
			env, err := s.env(ctx, q, t)
			if err != nil {
				return nil, nil, err
			}
			return engine.ObserveWith(t, engine.Rewound{}, env, now)
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

type fenceFile interface {
	io.ReaderAt
	io.WriterAt
	Sync() error
	Close() error
}

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

func nullID(id api.ID) any {
	if id.IsZero() {
		return nil
	}
	return id[:]
}

func nullBlob(b []byte) any {
	if b == nil {
		return nil
	}
	return b
}

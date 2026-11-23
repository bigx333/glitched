package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	"glitched/internal/api"
	"glitched/internal/engine"
)

// Submit decides c against its thread and commits the result with c's
// receipt. The same ID and body returns the stored receipt without deciding
// again; the same ID with another body is api.ErrConflict. A command that
// takes the tree (engine.TakesTree) is decided on every thread under its
// target too, in the same commit, so a child started a moment before goes
// with it, and the engine starts none under a settled or deleted thread.
func (s *Store) Submit(ctx context.Context, c api.Command, now time.Time) (api.Receipt, error) {
	return s.SubmitResolved(ctx, c, c.Body, now)
}

// SubmitResolved is Submit of c, decided as body: what the host made of
// c's body, as a create in the worktree it made for c. The receipt is
// c's, so the same c sent again gets it, and another body under c's ID
// conflicts.
func (s *Store) SubmitResolved(ctx context.Context, c api.Command, body api.Intent, now time.Time) (api.Receipt, error) {
	c.Expires = c.Expires.UTC()
	wire, err := json.Marshal(c)
	if err != nil {
		return api.Receipt{}, &api.RejectedError{Reason: err.Error()}
	}
	hash := sha256.Sum256(wire)
	c.Body = body
	top := step{c.Target, func(q querier, t engine.Thread) ([]engine.Event, []engine.Effect, error) {
		if b, ok := c.Body.(api.ThreadCreate); ok && !b.Parent.IsZero() && !t.Exists() {
			line, err := lineOf(ctx, q, b.Parent)
			if err != nil {
				return nil, nil, err
			}
			kids, err := kidsOf(ctx, q, b.Parent)
			if err != nil {
				return nil, nil, err
			}
			if err := engine.CheckChild(line, kids, s.limits, b); err != nil {
				return nil, nil, err
			}
		}
		if b, ok := c.Body.(api.ThreadConfigure); ok && !t.Parent.IsZero() {
			parent, err := loadThread(ctx, q, t.Parent)
			if err != nil {
				return nil, nil, err
			}
			if err := engine.CheckConfigure(parent, t, b); err != nil {
				return nil, nil, err
			}
		}
		return engine.Decide(t, c, now)
	}}
	r := &receipt{id: c.ID, hash: hash[:], thread: c.Target}
	at, err := s.commit(ctx, r, 0, func(q querier) ([]step, error) {
		if !engine.TakesTree(c.Body) {
			return []step{top}, nil
		}
		below, err := treeOf(ctx, q, c.Target)
		if err != nil {
			return nil, err
		}
		var steps []step
		for _, id := range below {
			steps = append(steps, step{id, func(_ querier, t engine.Thread) ([]engine.Event, []engine.Effect, error) {
				return engine.Decide(t, api.Command{ID: c.ID, Target: id, Body: engine.Below(c.Body, t)}, now)
			}})
		}
		return append(steps, top), nil
	})
	return api.Receipt{Command: c.ID, Committed: at, Revision: r.revision}, err
}

// Committed is whether a command under id was committed.
func (s *Store) Committed(ctx context.Context, id api.ID) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM commands WHERE id = ?`, id[:]).Scan(&n)
	return n > 0, err
}

// treeOf is every thread under top that isn't deleted, each before the
// thread it's under, as far down as api.MaxDepth, so a cycle can't hang
// the walk. A deleted thread's children stand on their own, as in lineOf.
func treeOf(ctx context.Context, q querier, top api.ID) ([]api.ID, error) {
	seen := map[api.ID]bool{top: true}
	var out []api.ID
	var walk func(id api.ID, depth int) error
	walk = func(id api.ID, depth int) error {
		if depth == api.MaxDepth {
			return nil
		}
		kids, err := ids(ctx, q, `SELECT id FROM threads WHERE parent = ? AND deleted = 0 ORDER BY id`, id[:])
		if err != nil {
			return err
		}
		for _, k := range kids {
			if seen[k] {
				continue
			}
			seen[k] = true
			if err := walk(k, depth+1); err != nil {
				return err
			}
			out = append(out, k)
		}
		return nil
	}
	return out, walk(top, 0)
}

// lineOf is the thread and those above it, it first, as far up as
// api.MaxDepth+1 threads, so a cycle can't hang the walk. A deleted
// ancestor ends the line: its children stand on their own. The thread
// itself is kept deleted, for CheckChild to refuse.
func lineOf(ctx context.Context, q querier, id api.ID) ([]engine.Thread, error) {
	var line []engine.Thread
	for !id.IsZero() && len(line) <= api.MaxDepth {
		t, err := loadThread(ctx, q, id)
		if err != nil || !t.Exists() || t.Deleted && len(line) > 0 {
			return line, err
		}
		line = append(line, t)
		id = t.Parent
	}
	return line, nil
}

// kidsOf counts parent's children that aren't deleted.
func kidsOf(ctx context.Context, q querier, parent api.ID) (engine.Kids, error) {
	var k engine.Kids
	err := q.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(EXISTS (SELECT 1 FROM runs WHERE runs.thread = threads.id AND runs.state NOT IN ('finished', 'uncertain'))), 0)
		FROM threads WHERE parent = ? AND deleted = 0`, parent[:]).Scan(&k.All, &k.Working)
	return k, err
}

// SetLimits caps the children agents start from the next Submit on.
func (s *Store) SetLimits(l engine.Limits) {
	s.mu.Lock()
	s.limits = l
	s.mu.Unlock()
}

// Observe commits what the runtime saw. A nonzero done is the leased effect
// the observation completes; it is marked done in the same transaction, even
// when the observation is refused with engine.ErrRefused.
func (s *Store) Observe(ctx context.Context, thread api.ID, o engine.Observation, now time.Time, done int64) (api.Cursor, error) {
	return s.commit(ctx, nil, done, func(querier) ([]step, error) {
		return []step{{thread, func(_ querier, t engine.Thread) ([]engine.Event, []engine.Effect, error) {
			return engine.Observe(t, o, now)
		}}}, nil
	})
}

// receipt is a command's, as commit stores it. commit sets revision, the
// thread's after the command: the stored one for a command it took
// before, which is 0 for one stored before version 8.
type receipt struct {
	id       api.ID
	hash     []byte
	thread   api.ID // the command's target
	revision uint64
}

// decider decides against the thread, reading what else it needs through
// q, the transaction's.
type decider func(q querier, t engine.Thread) ([]engine.Event, []engine.Effect, error)

// step is a thread a commit decides on, and how.
type step struct {
	thread api.ID
	decide decider
}

// commit writes the steps plan gives, in its order, in one transaction:
// a crash leaves all of them or none. A step the tables refuse writes
// nothing, and the commit returns engine.ErrRefused with what the rest
// wrote.
func (s *Store) commit(ctx context.Context, r *receipt, done int64, plan func(q querier) ([]step, error)) (api.Cursor, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return api.Cursor{}, err
	}
	defer tx.Rollback()

	if r != nil {
		var hash []byte
		var seq uint64
		var revision sql.NullInt64
		err := tx.QueryRowContext(ctx, `SELECT hash, seq, revision FROM commands WHERE id = ?`, r.id[:]).Scan(&hash, &seq, &revision)
		switch {
		case err == nil && bytes.Equal(hash, r.hash):
			r.revision = uint64(revision.Int64)
			return api.Cursor{Log: s.log, Seq: seq}, nil
		case err == nil:
			return api.Cursor{}, api.ErrConflict
		case !errors.Is(err, sql.ErrNoRows):
			return api.Cursor{}, err
		}
	}

	steps, err := plan(tx)
	if err != nil {
		return api.Cursor{}, err
	}
	var c Commit
	var head uint64
	var refused error
	for _, st := range steps {
		w, h, err := s.write(ctx, tx, st.thread, st.decide)
		if errors.Is(err, engine.ErrRefused) {
			refused, err = err, nil
		}
		if err != nil {
			return api.Cursor{}, err
		}
		c.Events, c.Threads, head = append(c.Events, w.Events...), append(c.Threads, w.Threads...), h
	}
	if done != 0 {
		if err := moveEffect(ctx, tx, done, "leased", "done"); err != nil {
			return api.Cursor{}, err
		}
	}
	if r != nil {
		if err := tx.QueryRowContext(ctx, `SELECT revision FROM threads WHERE id = ?`, r.thread[:]).Scan(&r.revision); err != nil {
			return api.Cursor{}, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO commands (id, hash, seq, revision) VALUES (?, ?, ?, ?)`, r.id[:], r.hash, head, r.revision); err != nil {
			return api.Cursor{}, err
		}
	}
	if len(c.Events) > 0 {
		if err := s.writeFence(head); err != nil {
			return api.Cursor{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return api.Cursor{}, err
	}
	if len(c.Events) > 0 {
		for _, h := range s.hooks {
			h(c)
		}
	}
	return api.Cursor{Log: s.log, Seq: head}, refused
}

// Commit is what one commit wrote, as clients read it: the events, and the
// threads whose projection they moved, as they are after the commit.
type Commit struct {
	Events  []api.Event
	Threads []api.Thread
}

// write decides against the thread's stored state and writes what comes of
// it into tx: events, the projection and effects. It returns the commit and
// the head after it, which a refusal leaves as it was.
func (s *Store) write(ctx context.Context, tx *sql.Tx, thread api.ID, decide decider) (Commit, uint64, error) {
	old, err := loadThread(ctx, tx, thread)
	if err != nil {
		return Commit{}, 0, err
	}
	head, err := headSeq(ctx, tx)
	if err != nil {
		return Commit{}, 0, err
	}
	events, effects, err := decide(tx, old)
	if err != nil {
		return Commit{}, head, err
	}
	t, wire := old, make([]api.Event, 0, len(events))
	for _, e := range events {
		head++
		payload, err := json.Marshal(e.Body)
		if err != nil {
			return Commit{}, 0, err
		}
		w := api.Event{
			Cursor: api.Cursor{Log: s.log, Seq: head}, Thread: e.Thread, Type: api.EventType(e.Body),
			Schema: api.EventSchema, At: e.At, Payload: payload,
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO events (seq, thread, type, schema, at, payload) VALUES (?, ?, ?, ?, ?, ?)`,
			head, w.Thread[:], w.Type, w.Schema, e.At.UnixNano(), payload); err != nil {
			return Commit{}, 0, err
		}
		e.Seq = head
		t = engine.Apply(t, e)
		wire = append(wire, w)
	}
	if len(events) > 0 {
		if err := writeThread(ctx, tx, old, t); err != nil {
			return Commit{}, 0, err
		}
	}
	for _, e := range effects {
		payload, err := json.Marshal(e)
		if err != nil {
			return Commit{}, 0, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO effects (thread, kind, payload, state) VALUES (?, ?, ?, 'pending')`,
			thread[:], engine.EffectKind(e), payload); err != nil {
			return Commit{}, 0, err
		}
	}
	c := Commit{Events: wire}
	// Transcript events (text, work items) leave the revision, and so the
	// projection, where it was.
	if t.Revision != old.Revision {
		c.Threads = []api.Thread{s.view(t)}
	}
	return c, head, nil
}

// view is t as clients read it, its report's cursor on this log.
func (s *Store) view(t engine.Thread) api.Thread {
	v := engine.View(t)
	if v.LastReport != nil {
		v.LastReport.Cursor.Log = s.log
	}
	return v
}

// storedReport is a thread's last report as its row holds it, in the JSON
// the upgrade to version 6 writes.
type storedReport struct {
	Child api.ID         `json:"child"`
	Kind  api.ReportKind `json:"kind"`
	Seq   uint64         `json:"seq"`
}

// Thread returns the stored projection, or a zero Thread if there is none.
func (s *Store) Thread(ctx context.Context, id api.ID) (engine.Thread, error) {
	return loadThread(ctx, s.db, id)
}

// LiveThreads lists threads that have a run in any state but finished.
func (s *Store) LiveThreads(ctx context.Context) ([]api.ID, error) {
	return ids(ctx, s.db, `SELECT thread FROM runs WHERE state <> 'finished' ORDER BY thread`)
}

func ids(ctx context.Context, q querier, query string, args ...any) ([]api.ID, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []api.ID
	for rows.Next() {
		var id api.ID
		if err := rows.Scan(idScan{&id}); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func loadThread(ctx context.Context, q querier, id api.ID) (engine.Thread, error) {
	t := engine.Thread{ID: id}
	var model []byte
	var until, reported sql.NullInt64
	var report sql.NullString
	var updated int64
	err := q.QueryRowContext(ctx, `SELECT workspace, title, model, access, revision, owner_epoch, generation, disposition, until, updated_at, deleted,
			parent, tokens, reported, report, session, project, branch
		FROM threads WHERE id = ?`, id[:]).Scan(&t.Workspace, &t.Title, &model, &t.Access, &t.Revision, &t.OwnerEpoch,
		&t.Generation, &t.Disposition.State, &until, &updated, &t.Deleted, idScan{&t.Parent}, &t.Tokens, &reported, &report, &t.Session,
		&t.Project, &t.Branch)
	if errors.Is(err, sql.ErrNoRows) {
		return engine.Thread{}, nil
	}
	if err != nil {
		return t, err
	}
	if err := json.Unmarshal(model, &t.Model); err != nil {
		return t, err
	}
	if until.Valid {
		t.Disposition.Until = fromNanos(until.Int64)
	}
	t.UpdatedAt = fromNanos(updated)
	if reported.Valid {
		t.Reported = fromNanos(reported.Int64)
	}
	if report.Valid {
		var r storedReport
		if err := json.Unmarshal([]byte(report.String), &r); err != nil {
			return t, err
		}
		t.LastReport = &api.Report{Child: r.Child, Kind: r.Kind, Cursor: api.Cursor{Seq: r.Seq}}
	}

	err = each(ctx, q, `SELECT id, generation, state, signalled, outcome, error, ended, model, access FROM runs WHERE thread = ? ORDER BY ord`, id, func(rows *sql.Rows) error {
		var r engine.Run
		var outcome, access sql.NullByte
		var ended sql.NullInt64
		var model sql.NullString
		if err := rows.Scan(idScan{&r.ID}, &r.Generation, &r.State, &r.Signalled, &outcome, &r.Error, &ended, &model, &access); err != nil {
			return err
		}
		r.Outcome = api.Outcome(outcome.Byte)
		if ended.Valid {
			r.Ended = fromNanos(ended.Int64)
		}
		// A run the upgrade to version 7 found no admission for has
		// neither: it reads at Ask, the narrowest, so it starts no child
		// wider than it's known to be, and at its thread's model.
		r.Access = api.Access(access.Byte)
		if !model.Valid {
			r.Model = t.Model
		} else if err := json.Unmarshal([]byte(model.String), &r.Model); err != nil {
			return err
		}
		t.Runs = append(t.Runs, r)
		return nil
	})
	if err != nil {
		return t, err
	}
	err = each(ctx, q, `SELECT id, text, mode, pos, state, run FROM messages WHERE thread = ? ORDER BY ord`, id, func(rows *sql.Rows) error {
		var m engine.Message
		if err := rows.Scan(idScan{&m.ID}, &m.Text, &m.Mode, &m.Pos, &m.State, idScan{&m.Run}); err != nil {
			return err
		}
		t.Queue = append(t.Queue, m)
		return nil
	})
	if err != nil {
		return t, err
	}
	err = each(ctx, q, `SELECT id, run, generation, dedup, kind, body, answer, state FROM requests WHERE thread = ? ORDER BY ord`, id, func(rows *sql.Rows) error {
		var r engine.Request
		if err := rows.Scan(idScan{&r.ID}, idScan{&r.Run}, &r.Generation, &r.Dedup, &r.Kind,
			(*[]byte)(&r.Body), (*[]byte)(&r.Answer), &r.State); err != nil {
			return err
		}
		t.Requests = append(t.Requests, r)
		return nil
	})
	return t, err
}

func each(ctx context.Context, q querier, query string, id api.ID, scan func(*sql.Rows) error) error {
	rows, err := q.QueryContext(ctx, query, id[:])
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := scan(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

// writeThread writes the rows that differ between old and t. Elements never
// move, so position identifies a row, and writing in position order finishes
// a run before the next one is admitted.
func writeThread(ctx context.Context, tx *sql.Tx, old, t engine.Thread) error {
	model, err := json.Marshal(t.Model)
	if err != nil {
		return err
	}
	var until, reported, report any
	if !t.Disposition.Until.IsZero() {
		until = t.Disposition.Until.UnixNano()
	}
	if !t.Reported.IsZero() {
		reported = t.Reported.UnixNano()
	}
	if r := t.LastReport; r != nil {
		b, err := json.Marshal(storedReport{Child: r.Child, Kind: r.Kind, Seq: r.Cursor.Seq})
		if err != nil {
			return err
		}
		report = string(b)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO threads (id, workspace, title, model, access, revision, owner_epoch, generation, disposition, until, updated_at, deleted,
			parent, tokens, reported, report, session, project, branch)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET workspace = excluded.workspace, title = excluded.title, model = excluded.model,
			access = excluded.access, revision = excluded.revision, owner_epoch = excluded.owner_epoch, generation = excluded.generation,
			disposition = excluded.disposition, until = excluded.until, updated_at = excluded.updated_at, deleted = excluded.deleted,
			parent = excluded.parent, tokens = excluded.tokens, reported = excluded.reported,
			report = excluded.report, session = excluded.session, project = excluded.project, branch = excluded.branch`,
		t.ID[:], t.Workspace, t.Title, string(model), t.Access, t.Revision, t.OwnerEpoch, t.Generation, t.Disposition.State, until,
		t.UpdatedAt.UnixNano(), t.Deleted, nullID(t.Parent), t.Tokens, reported, report, t.Session, t.Project, t.Branch); err != nil {
		return err
	}
	for i, r := range t.Runs {
		if i < len(old.Runs) && old.Runs[i] == r {
			continue
		}
		var outcome, ended any
		if r.State == engine.RunFinished {
			outcome, ended = r.Outcome, r.Ended.UnixNano()
		}
		admitted, err := json.Marshal(r.Model)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO runs (thread, ord, id, generation, state, signalled, outcome, error, ended, model, access) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (thread, ord) DO UPDATE SET state = excluded.state, signalled = excluded.signalled, outcome = excluded.outcome,
				error = excluded.error, ended = excluded.ended`,
			t.ID[:], i, r.ID[:], r.Generation, r.State, r.Signalled, outcome, r.Error, ended, string(admitted), r.Access); err != nil {
			return fmt.Errorf("store: run %s: %w", r.ID, err)
		}
	}
	for i, m := range t.Queue {
		if i < len(old.Queue) && old.Queue[i] == m {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO messages (thread, ord, id, text, mode, pos, state, run) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (thread, ord) DO UPDATE SET state = excluded.state, run = excluded.run`,
			t.ID[:], i, m.ID[:], m.Text, m.Mode, m.Pos, m.State, nullID(m.Run)); err != nil {
			return fmt.Errorf("store: message %s: %w", m.ID, err)
		}
	}
	for i, r := range t.Requests {
		if i < len(old.Requests) && reflect.DeepEqual(old.Requests[i], r) {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO requests (thread, ord, id, run, generation, dedup, kind, body, answer, state) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (thread, ord) DO UPDATE SET answer = excluded.answer, state = excluded.state`,
			t.ID[:], i, r.ID[:], r.Run[:], r.Generation, r.Dedup, r.Kind, nullBlob(r.Body), nullBlob(r.Answer), r.State); err != nil {
			return fmt.Errorf("store: request %s: %w", r.ID, err)
		}
	}
	return nil
}

func fromNanos(n int64) time.Time { return time.Unix(0, n).UTC() }

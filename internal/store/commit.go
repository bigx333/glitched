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
	return s.submit(ctx, c, body, nil, now)
}

// SubmitChecked is Submit of c once check passes on its target as c's
// own commit has it, so no commit between a read and c's can change what
// was checked.
func (s *Store) SubmitChecked(ctx context.Context, c api.Command, check func(engine.Thread) error, now time.Time) (api.Receipt, error) {
	return s.submit(ctx, c, c.Body, check, now)
}

func (s *Store) submit(ctx context.Context, c api.Command, body api.Intent, check func(engine.Thread) error, now time.Time) (api.Receipt, error) {
	c.Expires = c.Expires.UTC()
	// A caller's run is checked, not kept: the same command sent again by
	// its agent's next turn gets the receipt the first took.
	kept := c
	kept.Caller = nil
	wire, err := json.Marshal(kept)
	if err != nil {
		return api.Receipt{}, &api.RejectedError{Reason: err.Error()}
	}
	hash := sha256.Sum256(wire)
	c.Body = body
	top := step{c.Target, func(q querier, t engine.Thread) ([]engine.Event, []engine.Effect, error) {
		c := c
		if check != nil {
			if err := check(t); err != nil {
				return nil, nil, err
			}
		}
		if b, ok := c.Body.(api.ThreadCreate); ok && b.Fork != nil && !t.Exists() {
			src, err := loadThread(ctx, q, b.Fork.Thread)
			if err != nil {
				return nil, nil, err
			}
			if b.Forked, err = engine.CheckFork(src, b); err != nil {
				return nil, nil, err
			}
			c.Body = b
		}
		var line []engine.Thread
		if b, ok := c.Body.(api.ThreadCreate); ok && !b.Parent.IsZero() && !t.Exists() {
			var err error
			if line, err = lineOf(ctx, q, b.Parent); err != nil {
				return nil, nil, err
			}
			kids, err := kidsOf(ctx, q, b.Parent)
			if err != nil {
				return nil, nil, err
			}
			if err := engine.CheckChild(line, kids, s.limits, b); err != nil {
				return nil, nil, err
			}
			// A child under a link's thread runs no wider than the link's
			// thread may now.
			env, err := s.env(ctx, q, line[0])
			if err != nil {
				return nil, nil, err
			}
			if env.Ceiling != nil {
				if c, err := env.Ceiling(); err != nil || b.Access > c {
					return nil, nil, &api.RejectedError{Reason: "a child thread can't run wider than the thread from another machine it is under may now"}
				}
			}
		}
		if b, ok := c.Body.(api.ThreadCreate); ok && b.Origin != nil && t.Exists() {
			return nil, nil, fmt.Errorf("%w: a link creates only a thread that isn't there", api.ErrNotAllowed)
		}
		if b, ok := c.Body.(api.ThreadCreate); ok && b.Origin != nil && !t.Exists() {
			if s.links == nil {
				return nil, nil, &api.RejectedError{Reason: "this engine takes no threads from other machines"}
			}
			link, err := s.links(b.Origin.Link)
			if err != nil {
				return nil, nil, &api.RejectedError{Reason: err.Error()}
			}
			kids, err := linkKidsOf(ctx, q, b.Origin.Link)
			if err != nil {
				return nil, nil, err
			}
			// A settled create admits nothing and makes no worktree, ever:
			// a record that the thread is done with, which no count, depth
			// or access holds back. The link must still be there, and the
			// setting on.
			if err := engine.CheckOrigin(link, kids, s.limits, b); err != nil && !b.Settled {
				return nil, nil, err
			}
		}
		if c.Caller != nil && t.Exists() {
			userLast, err := steeredByUser(ctx, q, c.Target)
			if err != nil {
				return nil, nil, err
			}
			// A caller with no run here is a link's, for the agent of the
			// thread its child came from on that machine: this engine's
			// person who took the child over has it.
			if o := t.Origin; o != nil && c.Caller.Thread == o.Thread && c.Caller.Run.IsZero() {
				_, settles := c.Body.(api.InboxSet)
				switch {
				case settles && userLast:
					// A settle is taken from the link whoever has the
					// thread, and leaves its person's takeover theirs.
					c.Caller = nil
				case userLast:
					return nil, nil, &api.RejectedError{Reason: "the person on this machine took this thread over: its last message or stop was theirs, so it's theirs to steer"}
				}
			} else {
				line, err := lineOf(ctx, q, c.Target)
				if err != nil {
					return nil, nil, err
				}
				if err := engine.CheckCaller(line, *c.Caller, userLast); err != nil {
					return nil, nil, err
				}
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
		env, err := s.env(ctx, q, t)
		if err != nil {
			return nil, nil, err
		}
		env.Engine, env.Line = s.host, line
		return engine.DecideWith(t, c, env, now)
	}}
	r := &receipt{id: c.ID, hash: hash[:], thread: c.Target}
	at, err := s.commit(ctx, r, 0, func(q querier) ([]step, error) {
		var narrows []step
		if b, ok := c.Body.(api.ThreadCreate); ok && b.Settled && b.Origin != nil {
			// A settled create of a link's thread there already is the
			// settle of its tree, decided in the commit either create
			// takes, so the two creates of one thread serialize on it.
			t, err := loadThread(ctx, q, c.Target)
			if err != nil {
				return nil, err
			}
			if t.Exists() {
				if o := t.Origin; o == nil || o.Link != b.Origin.Link || o.Thread != b.Origin.Thread {
					return nil, fmt.Errorf("%w: a link creates only a thread that isn't there", api.ErrNotAllowed)
				}
				if s.links == nil {
					return nil, &api.RejectedError{Reason: "this engine takes no threads from other machines"}
				}
				if _, err := s.links(b.Origin.Link); err != nil {
					return nil, &api.RejectedError{Reason: err.Error()}
				}
				c.Body, c.Caller = api.InboxSet{Disposition: api.Disposition{State: api.Settled}}, &api.Caller{Thread: b.Origin.Thread}
				// The create comes at the access its machine narrowed the
				// child to since the first: the tree takes it in this
				// commit too, before the settle, so nothing reopened
				// there runs wider.
				if b.Access < t.Access {
					ids, err := treeOf(ctx, q, c.Target)
					if err != nil {
						return nil, err
					}
					narrow := api.ThreadConfigure{Access: &b.Access}
					for _, id := range append([]api.ID{c.Target}, ids...) {
						narrows = append(narrows, step{id, func(q querier, t engine.Thread) ([]engine.Event, []engine.Effect, error) {
							var body api.Intent = narrow
							if id != c.Target {
								if body = engine.Below(narrow, t); body == nil {
									return nil, nil, nil
								}
							}
							env, err := s.env(ctx, q, t)
							if err != nil {
								return nil, nil, err
							}
							return engine.DecideWith(t, api.Command{ID: c.ID, Target: id, Body: body}, env, now)
						}})
					}
				}
			}
		}
		if !engine.TakesTree(c.Body) {
			return append(narrows, top), nil
		}
		below, err := treeOf(ctx, q, c.Target)
		if err != nil {
			return nil, err
		}
		steps := narrows
		for _, id := range below {
			// An agent's stop of a tree leaves the threads the user took
			// over, and the runs only the user can end, to the user.
			if _, stops := c.Body.(api.TurnInterrupt); stops && c.Caller != nil {
				theirs, err := steeredByUser(ctx, q, id)
				if err != nil {
					return nil, err
				}
				if theirs {
					continue
				}
			}
			steps = append(steps, step{id, func(q querier, t engine.Thread) ([]engine.Event, []engine.Effect, error) {
				// A link's settle goes as the person's on a thread they
				// steer, so it settles it as their own settle would.
				caller := c.Caller
				if _, settles := c.Body.(api.InboxSet); settles && caller != nil && caller.Run.IsZero() {
					theirs, err := steeredByUser(ctx, q, id)
					if err != nil {
						return nil, nil, err
					}
					if theirs {
						caller = nil
					}
				}
				if r := t.Live(); caller != nil && r != nil && r.State == engine.RunUncertain {
					return nil, nil, nil
				}
				if _, stops := c.Body.(api.TurnInterrupt); stops && caller != nil && t.Remote != nil && t.Remote.Taken {
					return nil, nil, nil
				}
				body := engine.Below(c.Body, t)
				if body == nil {
					return nil, nil, nil
				}
				env, err := s.env(ctx, q, t)
				if err != nil {
					return nil, nil, err
				}
				return engine.DecideWith(t, api.Command{ID: c.ID, Target: id, Body: body, Caller: caller}, env, now)
			}})
		}
		return append(steps, top), nil
	})
	return api.Receipt{Command: c.ID, Committed: at, Revision: r.revision}, err
}

// CheckChild checks a child's create against its parent's line and
// children as they are now, as the create's own commit will again: what
// a host checks before it does work for the create, as making a worktree.
func (s *Store) CheckChild(ctx context.Context, b api.ThreadCreate) error {
	line, err := lineOf(ctx, s.db, b.Parent)
	if err != nil {
		return err
	}
	kids, err := kidsOf(ctx, s.db, b.Parent)
	if err != nil {
		return err
	}
	s.mu.Lock()
	lim := s.limits
	s.mu.Unlock()
	return engine.CheckChild(line, kids, lim, b)
}

// CheckFork is where b, a create with a Fork, goes on from, checked
// against its source as it is now, as the create's own commit will again:
// what a host checks before it does work for the create.
func (s *Store) CheckFork(ctx context.Context, b api.ThreadCreate) (*api.Forked, error) {
	src, err := loadThread(ctx, s.db, b.Fork.Thread)
	if err != nil {
		return nil, err
	}
	return engine.CheckFork(src, b)
}

// steeredByUser is whether the user sent thread's last message or asked
// its last stop: one no agent sent, a child's report that wakes it and
// the engine's own stops aside.
func steeredByUser(ctx context.Context, q querier, thread api.ID) (bool, error) {
	var by sql.NullString
	err := q.QueryRowContext(ctx, `SELECT json_extract(payload, '$.by') FROM events WHERE thread = ?
		AND (type = 'stop.asked' OR type = 'message.queued' AND json_extract(payload, '$.report') IS NULL AND json_extract(payload, '$.silent') IS NULL
			AND NOT (EXISTS (SELECT 1 FROM threads WHERE id = ? AND origin IS NOT NULL) AND seq = (SELECT MIN(seq) FROM events WHERE thread = ? AND type = 'message.queued')))
		ORDER BY seq DESC LIMIT 1`, thread[:], thread[:], thread[:]).Scan(&by)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return !by.Valid, err
}

// Committed is whether a command under id was committed. It is
// api.ErrExpired for one whose receipt Maintain may have pruned.
func (s *Store) Committed(ctx context.Context, id api.ID) (bool, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM commands WHERE id = ?`, id[:]).Scan(&n); err != nil || n > 0 {
		return n > 0, err
	}
	return false, forgotten(ctx, s.db, id)
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

// linkKidsOf counts the threads link started here that aren't deleted,
// as engine.Kids says: one whose worktree is being made, and isn't
// settled, is at work.
func linkKidsOf(ctx context.Context, q querier, link api.ID) (engine.Kids, error) {
	var k engine.Kids
	err := q.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(EXISTS (SELECT 1 FROM runs WHERE runs.thread = threads.id AND runs.state NOT IN ('finished', 'uncertain'))
			OR json_extract(origin, '$.making') AND disposition <> ?), 0)
		FROM threads WHERE json_extract(origin, '$.link') = ? AND deleted = 0`, api.Settled, link.String()).Scan(&k.All, &k.Working)
	return k, err
}

// env is what a decision on t reads besides t, as the engine has it now,
// through q, the commit's: with the ceiling of the link's thread t is or
// is under, so the link caps every thread of its tree at each commit.
// s.mu must be held.
func (s *Store) env(ctx context.Context, q querier, t engine.Thread) (engine.Env, error) {
	env := engine.Env{Places: s.places}
	o, err := originOf(ctx, q, t)
	if err != nil || o == nil {
		return env, err
	}
	links := s.links
	env.Ceiling = func() (api.Access, error) {
		if links == nil {
			return 0, errors.New("this engine takes no threads from other machines")
		}
		l, err := links(o.Link)
		return min(l.Access, o.Access), err
	}
	return env, nil
}

// originOf is the origin of the link's thread t is or is under, through
// deleted threads too, as far up as api.MaxDepth: nil for a thread of
// this engine's own line.
func originOf(ctx context.Context, q querier, t engine.Thread) (*engine.Origin, error) {
	for range api.MaxDepth + 1 {
		if t.Origin != nil || t.Parent.IsZero() {
			return t.Origin, nil
		}
		parent, err := loadThread(ctx, q, t.Parent)
		if err != nil || !parent.Exists() {
			return nil, err
		}
		t = parent
	}
	return nil, nil
}

// Bound are the placeholders whose child their machine took, and which
// are neither lost nor deleted: those a host follows.
func (s *Store) Bound(ctx context.Context) ([]api.ID, error) {
	return ids(ctx, s.db, `SELECT id FROM threads WHERE json_extract(remote, '$.state') = ? AND deleted = 0 ORDER BY id`, engine.RemoteBoundState)
}

// SetLinks has the threads other machines' links start find their link
// through links from the next commit on, in each commit.
func (s *Store) SetLinks(links engine.Links) {
	s.mu.Lock()
	s.links = links
	s.mu.Unlock()
}

// kidsOf counts parent's children that aren't deleted, as engine.Kids
// says.
func kidsOf(ctx context.Context, q querier, parent api.ID) (engine.Kids, error) {
	var k engine.Kids
	err := q.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(EXISTS (SELECT 1 FROM runs WHERE runs.thread = threads.id AND runs.state NOT IN ('finished', 'uncertain'))
			OR json_extract(remote, '$.state') = ? AND disposition <> ?
			OR json_extract(remote, '$.state') = ? AND COALESCE(json_extract(remote, '$.slot'), ?) NOT IN (?, ?)), 0)
		FROM threads WHERE parent = ? AND deleted = 0`, engine.RemoteCreating, api.Settled, engine.RemoteBoundState, api.RunIdle, api.RunIdle, api.RunUncertain, parent[:]).Scan(&k.All, &k.Working)
	return k, err
}

// SetPlaces has a remote child's create find the machine it names
// through places from the next Submit on, in the create's commit.
func (s *Store) SetPlaces(places engine.Places) {
	s.mu.Lock()
	s.places = places
	s.mu.Unlock()
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
		return []step{{thread, func(q querier, t engine.Thread) ([]engine.Event, []engine.Effect, error) {
			env, err := s.env(ctx, q, t)
			if err != nil {
				return nil, nil, err
			}
			return engine.ObserveWith(t, o, env, now)
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
		if err := forgotten(ctx, tx, r.id); err != nil {
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

// Checkouts are the folders the person's own threads run in, or were
// started from when in a worktree: each thread's project, or its folder,
// for every thread of theirs that isn't deleted.
func (s *Store) Checkouts(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT CASE WHEN project <> '' THEN project ELSE workspace END FROM threads
		WHERE deleted = 0 AND parent IS NULL AND origin IS NULL AND remote IS NULL ORDER BY 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var dir string
		if err := rows.Scan(&dir); err != nil {
			return nil, err
		}
		if dir != "" {
			out = append(out, dir)
		}
	}
	return out, rows.Err()
}

// Closing lists threads with an agent's close not done, whose agent may
// still run.
func (s *Store) Closing(ctx context.Context) ([]api.ID, error) {
	return ids(ctx, s.db, `SELECT DISTINCT thread FROM effects WHERE kind = ? AND state <> 'done' ORDER BY thread`, engine.EffectKind(engine.CloseAgent{}))
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

const threadSelectCols = `id, workspace, title, model, access, revision, owner_epoch, generation, disposition, until, updated_at, deleted,
	parent, tokens, reported, report, session, project, branch, context_used, context_window, context_stale, context_model, granted, context_cache_expires, remote, origin`

func scanThreadRow(row interface{ Scan(...any) error }, t *engine.Thread) error {
	var model []byte
	var until, reported, cacheExpires sql.NullInt64
	var report, granted, remote, origin sql.NullString
	var updated int64
	err := row.Scan(idScan{&t.ID}, &t.Workspace, &t.Title, &model, &t.Access, &t.Revision, &t.OwnerEpoch,
		&t.Generation, &t.Disposition.State, &until, &updated, &t.Deleted, idScan{&t.Parent}, &t.Tokens, &reported, &report, &t.Session,
		&t.Project, &t.Branch, &t.Context.Used, &t.Context.Window, &t.Context.Stale, &t.Context.Model, &granted, &cacheExpires, &remote, &origin)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(model, &t.Model); err != nil {
		return err
	}
	if until.Valid {
		t.Disposition.Until = fromNanos(until.Int64)
	}
	t.UpdatedAt = fromNanos(updated)
	if reported.Valid {
		t.Reported = fromNanos(reported.Int64)
	}
	if cacheExpires.Valid {
		t.Context.CacheExpires = fromNanos(cacheExpires.Int64)
	}
	if report.Valid {
		var r storedReport
		if err := json.Unmarshal([]byte(report.String), &r); err != nil {
			return err
		}
		t.LastReport = &api.Report{Child: r.Child, Kind: r.Kind, Cursor: api.Cursor{Seq: r.Seq}}
	}
	if granted.Valid {
		if err := json.Unmarshal([]byte(granted.String), &t.Confirmed); err != nil {
			return err
		}
	}
	if remote.Valid {
		t.Remote = &engine.Remote{}
		if err := json.Unmarshal([]byte(remote.String), t.Remote); err != nil {
			return err
		}
	}
	if origin.Valid {
		t.Origin = &engine.Origin{}
		if err := json.Unmarshal([]byte(origin.String), t.Origin); err != nil {
			return err
		}
	}
	return nil
}

func scanRunRow(rows *sql.Rows) (api.ID, engine.Run, bool, error) {
	var thread api.ID
	var r engine.Run
	var outcome, access sql.NullByte
	var ended, resets sql.NullInt64
	var model sql.NullString
	if err := rows.Scan(idScan{&thread}, idScan{&r.ID}, &r.Generation, &r.State, &r.Signalled, &outcome, &r.Error, &ended, &model, &access, &r.Silent, &resets, &r.ResumeCancelled, &r.Dropped); err != nil {
		return thread, r, false, err
	}
	if resets.Valid {
		r.Limit = &api.UsageLimit{}
		if resets.Int64 != 0 {
			r.Limit.Resets = fromNanos(resets.Int64)
		}
	}
	r.Outcome = api.Outcome(outcome.Byte)
	if ended.Valid {
		r.Ended = fromNanos(ended.Int64)
	}
	// A run the upgrade to version 7 found no admission for has
	// neither: it reads at Ask, the narrowest, so it starts no child
	// wider than it's known to be, and at its thread's model.
	r.Access = api.Access(access.Byte)
	if model.Valid {
		if err := json.Unmarshal([]byte(model.String), &r.Model); err != nil {
			return thread, r, false, err
		}
	}
	return thread, r, model.Valid, nil
}

func scanMessageRow(rows *sql.Rows) (api.ID, engine.Message, error) {
	var thread api.ID
	var m engine.Message
	err := rows.Scan(idScan{&thread}, idScan{&m.ID}, &m.Text, &m.Mode, &m.Pos, &m.State, idScan{&m.Run})
	return thread, m, err
}

func scanRequestRow(rows *sql.Rows) (api.ID, engine.Request, error) {
	var thread api.ID
	var r engine.Request
	err := rows.Scan(idScan{&thread}, idScan{&r.ID}, idScan{&r.Run}, &r.Generation, &r.Dedup, &r.Kind,
		(*[]byte)(&r.Body), (*[]byte)(&r.Answer), &r.State)
	return thread, r, err
}

// liveViews is the view of every thread not deleted, in the order the
// thread list shows them. It reads each thread as View does, in one query
// per table: a message without its text, a request without its answer and,
// unless pending, its body. So the list costs what it shows, not every
// message its threads were sent.
func (s *Store) liveViews(ctx context.Context, q querier) ([]api.Thread, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+threadSelectCols+` FROM threads WHERE deleted = 0 ORDER BY updated_at DESC, id`)
	if err != nil {
		return nil, err
	}
	var out []engine.Thread
	index := map[api.ID]int{}
	for rows.Next() {
		var t engine.Thread
		if err := scanThreadRow(rows, &t); err != nil {
			rows.Close()
			return nil, err
		}
		index[t.ID] = len(out)
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	if len(out) == 0 {
		return nil, nil
	}

	if err := queryEach(ctx, q, `SELECT r.thread, r.id, r.generation, r.state, r.signalled, r.outcome, r.error, r.ended, r.model, r.access, r.silent, r.limit_resets, r.resume_cancelled, r.dropped
		FROM runs r JOIN threads t ON t.id = r.thread WHERE t.deleted = 0 ORDER BY r.thread, r.ord`, func(rows *sql.Rows) error {
		thread, r, hasModel, err := scanRunRow(rows)
		if err != nil {
			return err
		}
		if idx, ok := index[thread]; ok {
			if !hasModel {
				r.Model = out[idx].Model
			}
			out[idx].Runs = append(out[idx].Runs, r)
		}
		return nil
	}); err != nil {
		return nil, err
	}

	if err := queryEach(ctx, q, `SELECT m.thread, m.id, '', m.mode, m.pos, m.state, m.run
		FROM messages m JOIN threads t ON t.id = m.thread WHERE t.deleted = 0 ORDER BY m.thread, m.ord`, func(rows *sql.Rows) error {
		thread, m, err := scanMessageRow(rows)
		if err != nil {
			return err
		}
		if idx, ok := index[thread]; ok {
			out[idx].Queue = append(out[idx].Queue, m)
		}
		return nil
	}); err != nil {
		return nil, err
	}

	if err := queryEach(ctx, q, `SELECT q.thread, q.id, q.run, q.generation, q.dedup, q.kind, CASE WHEN q.state = ? THEN q.body END, NULL, q.state
		FROM requests q JOIN threads t ON t.id = q.thread WHERE t.deleted = 0 ORDER BY q.thread, q.ord`, func(rows *sql.Rows) error {
		thread, r, err := scanRequestRow(rows)
		if err != nil {
			return err
		}
		if idx, ok := index[thread]; ok {
			out[idx].Requests = append(out[idx].Requests, r)
		}
		return nil
	}, int(api.RequestPending)); err != nil {
		return nil, err
	}
	views := make([]api.Thread, len(out))
	for i, t := range out {
		views[i] = s.view(t)
	}
	return views, nil
}

func loadThread(ctx context.Context, q querier, id api.ID) (engine.Thread, error) {
	var t engine.Thread
	err := scanThreadRow(q.QueryRowContext(ctx, `SELECT `+threadSelectCols+` FROM threads WHERE id = ?`, id[:]), &t)
	if errors.Is(err, sql.ErrNoRows) {
		return engine.Thread{}, nil
	}
	if err != nil {
		return t, err
	}

	err = each(ctx, q, `SELECT thread, id, generation, state, signalled, outcome, error, ended, model, access, silent, limit_resets, resume_cancelled, dropped FROM runs WHERE thread = ? ORDER BY ord`, id, func(rows *sql.Rows) error {
		_, r, hasModel, err := scanRunRow(rows)
		if err != nil {
			return err
		}
		if !hasModel {
			r.Model = t.Model
		}
		t.Runs = append(t.Runs, r)
		return nil
	})
	if err != nil {
		return t, err
	}
	err = each(ctx, q, `SELECT thread, id, text, mode, pos, state, run FROM messages WHERE thread = ? ORDER BY ord`, id, func(rows *sql.Rows) error {
		_, m, err := scanMessageRow(rows)
		if err != nil {
			return err
		}
		t.Queue = append(t.Queue, m)
		return nil
	})
	if err != nil {
		return t, err
	}
	err = each(ctx, q, `SELECT thread, id, run, generation, dedup, kind, body, answer, state FROM requests WHERE thread = ? ORDER BY ord`, id, func(rows *sql.Rows) error {
		_, r, err := scanRequestRow(rows)
		if err != nil {
			return err
		}
		t.Requests = append(t.Requests, r)
		return nil
	})
	return t, err
}

func queryEach(ctx context.Context, q querier, query string, scan func(*sql.Rows) error, args ...any) error {
	rows, err := q.QueryContext(ctx, query, args...)
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

func each(ctx context.Context, q querier, query string, id api.ID, scan func(*sql.Rows) error) error {
	return queryEach(ctx, q, query, scan, id[:])
}

// writeThread writes the rows that differ between old and t. Elements never
// move, so position identifies a row, and writing in position order finishes
// a run before the next one is admitted.
func writeThread(ctx context.Context, tx *sql.Tx, old, t engine.Thread) error {
	model, err := json.Marshal(t.Model)
	if err != nil {
		return err
	}
	var until, reported, report, granted, cacheExpires, remote, origin any
	if t.Origin != nil {
		b, err := json.Marshal(t.Origin)
		if err != nil {
			return err
		}
		origin = string(b)
	}
	if t.Remote != nil {
		b, err := json.Marshal(t.Remote)
		if err != nil {
			return err
		}
		remote = string(b)
	}
	if t.Confirmed != (api.Confirmed{}) {
		b, err := json.Marshal(t.Confirmed)
		if err != nil {
			return err
		}
		granted = string(b)
	}
	if !t.Disposition.Until.IsZero() {
		until = t.Disposition.Until.UnixNano()
	}
	if !t.Reported.IsZero() {
		reported = t.Reported.UnixNano()
	}
	if !t.Context.CacheExpires.IsZero() {
		cacheExpires = t.Context.CacheExpires.UnixNano()
	}
	if r := t.LastReport; r != nil {
		b, err := json.Marshal(storedReport{Child: r.Child, Kind: r.Kind, Seq: r.Cursor.Seq})
		if err != nil {
			return err
		}
		report = string(b)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO threads (id, workspace, title, model, access, revision, owner_epoch, generation, disposition, until, updated_at, deleted,
			parent, tokens, reported, report, session, project, branch, context_used, context_window, context_stale, context_model, granted, context_cache_expires, remote, origin)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET workspace = excluded.workspace, title = excluded.title, model = excluded.model,
			access = excluded.access, revision = excluded.revision, owner_epoch = excluded.owner_epoch, generation = excluded.generation,
			disposition = excluded.disposition, until = excluded.until, updated_at = excluded.updated_at, deleted = excluded.deleted,
			parent = excluded.parent, tokens = excluded.tokens, reported = excluded.reported,
			report = excluded.report, session = excluded.session, project = excluded.project, branch = excluded.branch,
			context_used = excluded.context_used, context_window = excluded.context_window, context_stale = excluded.context_stale,
			context_model = excluded.context_model, granted = excluded.granted, context_cache_expires = excluded.context_cache_expires,
			remote = excluded.remote, origin = excluded.origin`,
		t.ID[:], t.Workspace, t.Title, string(model), t.Access, t.Revision, t.OwnerEpoch, t.Generation, t.Disposition.State, until,
		t.UpdatedAt.UnixNano(), t.Deleted, nullID(t.Parent), t.Tokens, reported, report, t.Session, t.Project, t.Branch,
		t.Context.Used, t.Context.Window, t.Context.Stale, t.Context.Model, granted, cacheExpires, remote, origin); err != nil {
		return err
	}
	for i, r := range t.Runs {
		if i < len(old.Runs) && old.Runs[i] == r {
			continue
		}
		var outcome, ended, resets any
		if r.State == engine.RunFinished {
			outcome, ended = r.Outcome, r.Ended.UnixNano()
		}
		if l := r.Limit; l != nil {
			resets = int64(0)
			if !l.Resets.IsZero() {
				resets = l.Resets.UnixNano()
			}
		}
		admitted, err := json.Marshal(r.Model)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO runs (thread, ord, id, generation, state, signalled, outcome, error, ended, model, access, silent, limit_resets, resume_cancelled, dropped) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (thread, ord) DO UPDATE SET state = excluded.state, signalled = excluded.signalled, outcome = excluded.outcome,
				error = excluded.error, ended = excluded.ended, limit_resets = excluded.limit_resets,
				resume_cancelled = excluded.resume_cancelled, dropped = excluded.dropped`,
			t.ID[:], i, r.ID[:], r.Generation, r.State, r.Signalled, outcome, r.Error, ended, string(admitted), r.Access, r.Silent, resets, r.ResumeCancelled, r.Dropped); err != nil {
			return fmt.Errorf("store: run %s: %w", r.ID, err)
		}
	}
	for i, m := range t.Queue {
		if i < len(old.Queue) && old.Queue[i] == m {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO messages (thread, ord, id, text, mode, pos, state, run) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (thread, ord) DO UPDATE SET text = excluded.text, mode = excluded.mode, pos = excluded.pos, state = excluded.state, run = excluded.run`,
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

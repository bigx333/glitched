package store

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"strings"

	"glitched/internal/api"
)

// transcriptFTS indexes what a thread's transcript shows as text: each of
// the user's messages, under its message, with its latest words, and each
// finished run's replies, under the run, one row for each the transcript
// shows. A row's seq is the event that starts it: a message's queueing, a
// reply's first words.
const transcriptFTS = `
CREATE VIRTUAL TABLE IF NOT EXISTS transcript_fts USING fts5(
	thread_id UNINDEXED,
	seq UNINDEXED,
	role UNINDEXED,
	msg_id UNINDEXED,
	body,
	tokenize='unicode61'
);
`

func upperHexID(id api.ID) string {
	return strings.ToUpper(hex.EncodeToString(id[:]))
}

// ftsQuery turns free-form user text into a safe FTS5 prefix query by
// quoting each whitespace-delimited token and appending "*".
func ftsQuery(raw string) string {
	fields := strings.Fields(strings.ReplaceAll(raw, "\x00", " "))
	if len(fields) == 0 {
		return ""
	}
	parts := make([]string, 0, len(fields))
	for _, f := range fields {
		escaped := strings.ReplaceAll(f, `"`, `""`)
		parts = append(parts, `"`+escaped+`"*`)
	}
	return strings.Join(parts, " ")
}

type querierExec interface {
	querier
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func indexEventFTS(ctx context.Context, tx querierExec, thread api.ID, seq uint64, body api.EventBody) error {
	tid := upperHexID(thread)
	switch b := body.(type) {
	case api.MessageQueued:
		// The transcript shows neither a silent run's message nor a
		// child's report wake as the user's.
		if b.Silent || !b.Report.IsZero() {
			return nil
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO transcript_fts (thread_id, seq, role, msg_id, body) VALUES (?, ?, 'user', ?, ?)`, tid, seq, upperHexID(b.Message), b.Text)
		return err
	case api.MessageEdited:
		_, err := tx.ExecContext(ctx, `UPDATE transcript_fts SET body = ? WHERE thread_id = ? AND role = 'user' AND msg_id = ?`, b.Text, tid, upperHexID(b.Message))
		return err
	case api.TurnEnded:
		return indexReplies(ctx, tx, thread, seq, b.Run)
	case api.ThreadDeleted:
		_, err := tx.ExecContext(ctx, `DELETE FROM transcript_fts WHERE thread_id = ?`, tid)
		return err
	}
	return nil
}

// indexReplies indexes the replies of run, which ended at end, reading
// the thread's events from its admission on.
func indexReplies(ctx context.Context, tx querierExec, thread api.ID, end uint64, run api.ID) error {
	from, silent, err := admission(ctx, tx, thread, end, run)
	if err != nil || silent {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT seq, type, payload FROM events WHERE thread = ? AND seq > ? AND seq < ? ORDER BY seq`, thread[:], from, end)
	if err != nil {
		return err
	}
	r := replies{run: run, shown: map[string]bool{}, quiet: map[api.ID]bool{}}
	for rows.Next() {
		var seq uint64
		var typ string
		var raw []byte
		if err := rows.Scan(&seq, &typ, &raw); err != nil {
			rows.Close()
			return err
		}
		r.read(seq, typ, raw)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	tid, rid := upperHexID(thread), upperHexID(run)
	for _, p := range r.said {
		if strings.TrimSpace(p.text.String()) == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO transcript_fts (thread_id, seq, role, msg_id, body) VALUES (?, ?, 'assistant', ?, ?)`, tid, p.seq, rid, p.text.String()); err != nil {
			return err
		}
	}
	return nil
}

// admission is the seq of run's admission before end, 0 when the log has
// none, and whether it was a silent run's.
func admission(ctx context.Context, tx querierExec, thread api.ID, end uint64, run api.ID) (uint64, bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT seq, payload FROM events WHERE thread = ? AND type = 'turn.admitted' AND seq < ? ORDER BY seq DESC`, thread[:], end)
	if err != nil {
		return 0, false, err
	}
	defer rows.Close()
	for rows.Next() {
		var seq uint64
		var raw []byte
		if err := rows.Scan(&seq, &raw); err != nil {
			return 0, false, err
		}
		var a api.TurnAdmitted
		if json.Unmarshal(raw, &a) == nil && a.Run == run {
			return seq, a.Silent, nil
		}
	}
	return 0, false, rows.Err()
}

// replies are a run's replies as the transcript splits them: a reply goes
// on until another entry comes after it, as a new step, the run's plan,
// a request, a report, another run's words, or a message that joins the
// conversation.
type replies struct {
	run  api.ID
	said []reply
	open bool
	// shown are the run's steps the transcript shows, which a step's
	// update changes in place; planned, that it shows the run's plan.
	shown   map[string]bool
	planned bool
	// quiet are the messages that never join the conversation: silent
	// runs' and report wakes.
	quiet map[api.ID]bool
}

type reply struct {
	seq  uint64
	text strings.Builder
}

func (r *replies) read(seq uint64, typ string, raw []byte) {
	body, err := api.Event{Type: typ, Payload: raw}.Decode()
	if err != nil {
		return
	}
	switch b := body.(type) {
	case api.AssistantDelta:
		if b.Run != r.run {
			r.open = false
			return
		}
		if !r.open {
			r.said = append(r.said, reply{seq: seq})
			r.open = true
		}
		r.said[len(r.said)-1].text.WriteString(b.Text)
	case api.WorkItem:
		if b.Run == r.run && !r.shown[b.Item] {
			r.shown[b.Item] = true
			r.open = false
		}
	case api.PlanUpdated:
		if b.Run == r.run && len(b.Steps) > 0 && !r.planned {
			r.planned = true
			r.open = false
		}
	case api.RequestOpened:
		if b.Run == r.run {
			r.open = false
		}
	case api.ChildReported, api.TurnAdmitted:
		r.open = false
	case api.MessageQueued:
		if b.Silent || !b.Report.IsZero() {
			r.quiet[b.Message] = true
		}
	case api.MessageDispatched:
		r.joined(b.Message)
	case api.MessageCancelled:
		r.joined(b.Message)
	case api.MessageUncertain:
		r.joined(b.Message)
	}
}

// joined is a message settled, which a waiting one of the user's joins
// the conversation on.
func (r *replies) joined(message api.ID) {
	if message != r.run && !r.quiet[message] {
		r.open = false
	}
}

// backfillTranscriptFTS indexes every live thread's log as indexEventFTS
// would have as it was written. A run still going is indexed as it ends.
func backfillTranscriptFTS(ctx context.Context, tx querierExec) error {
	var threads []api.ID
	rows, err := tx.QueryContext(ctx, `SELECT id FROM threads WHERE deleted = 0`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id api.ID
		if err := rows.Scan(idScan{&id}); err != nil {
			rows.Close()
			return err
		}
		threads = append(threads, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, thread := range threads {
		if err := backfillThread(ctx, tx, thread); err != nil {
			return err
		}
	}
	return nil
}

func backfillThread(ctx context.Context, tx querierExec, thread api.ID) error {
	rows, err := tx.QueryContext(ctx, `SELECT seq, type, payload FROM events
		WHERE thread = ? AND type IN ('message.queued', 'message.edited', 'turn.ended') ORDER BY seq`, thread[:])
	if err != nil {
		return err
	}
	type event struct {
		seq  uint64
		body api.EventBody
	}
	var events []event
	for rows.Next() {
		var e event
		var typ string
		var raw []byte
		if err := rows.Scan(&e.seq, &typ, &raw); err != nil {
			rows.Close()
			return err
		}
		if e.body, err = (api.Event{Type: typ, Payload: raw}).Decode(); err == nil {
			events = append(events, e)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, e := range events {
		if err := indexEventFTS(ctx, tx, thread, e.seq, e.body); err != nil {
			return err
		}
	}
	return nil
}

// Search queries the SQLite FTS5 transcript index over non-deleted threads:
// across threads, each thread's best match alone.
func (s *Store) Search(ctx context.Context, q api.SearchQuery) (api.SearchResult, error) {
	match := ftsQuery(q.Query)
	if match == "" {
		return api.SearchResult{}, nil
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 30
	}
	limit = min(limit, 100)
	// FTS5 computes bm25 and snippet only for the query that matches, so
	// the matches are ranked first, each thread's best kept, and only
	// those read back with their snippets.
	where, keep := "", "b.rn = 1"
	args := []any{match}
	if !q.Thread.IsZero() {
		where, keep = " AND f.thread_id = ?", "1"
		args = append(args, upperHexID(q.Thread))
	}
	args = append(args, match, limit)
	rows, err := s.db.QueryContext(ctx, `WITH matched AS MATERIALIZED (
			SELECT f.rowid AS id, f.thread_id, f.seq, bm25(transcript_fts) AS score
			FROM transcript_fts f JOIN threads t ON hex(t.id) = f.thread_id
			WHERE t.deleted = 0 AND transcript_fts MATCH ?`+where+`
		), best AS (
			SELECT id, score, seq, ROW_NUMBER() OVER (PARTITION BY thread_id ORDER BY score, seq DESC) AS rn FROM matched
		)
		SELECT t.id, t.title, t.workspace, t.project, t.updated_at, f.seq, f.role, snippet(transcript_fts, 4, '', '', '…', 18), b.score
		FROM best b
		JOIN transcript_fts f ON f.rowid = b.id
		JOIN threads t ON hex(t.id) = f.thread_id
		WHERE `+keep+` AND transcript_fts MATCH ?
		ORDER BY b.score, b.seq DESC LIMIT ?`, args...)
	if err != nil {
		return api.SearchResult{}, err
	}
	defer rows.Close()
	var hits []api.SearchHit
	for rows.Next() {
		var h api.SearchHit
		var updated int64
		if err := rows.Scan(idScan{&h.Thread}, &h.Title, &h.Workspace, &h.Project, &updated, &h.Seq, &h.Role, &h.Snippet, &h.Score); err != nil {
			return api.SearchResult{}, err
		}
		h.UpdatedAt = fromNanos(updated)
		hits = append(hits, h)
	}
	if err := rows.Err(); err != nil {
		return api.SearchResult{}, err
	}
	return api.SearchResult{Hits: hits}, nil
}

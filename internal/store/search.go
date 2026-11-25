package store

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"strings"

	"glitched/internal/api"
)

func upperHexID(id api.ID) string {
	return strings.ToUpper(hex.EncodeToString(id[:]))
}

// ftsQuery turns free-form user text into a safe FTS5 prefix query by
// quoting each whitespace-delimited token and appending "*".
func ftsQuery(raw string) string {
	fields := strings.Fields(raw)
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

func indexEventFTS(ctx context.Context, tx querierExec, thread api.ID, seq uint64, body api.EventBody) error {
	tid := upperHexID(thread)
	switch b := body.(type) {
	case api.MessageQueued:
		if b.Silent || strings.TrimSpace(b.Text) == "" {
			return nil
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO transcript_fts (thread_id, seq, role, body) VALUES (?, ?, 'user', ?)`, tid, seq, b.Text)
		return err
	case api.MessageEdited:
		if strings.TrimSpace(b.Text) == "" {
			return nil
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO transcript_fts (thread_id, seq, role, body) VALUES (?, ?, 'user', ?)`, tid, seq, b.Text)
		return err
	case api.TurnEnded:
		rows, err := tx.QueryContext(ctx, `SELECT seq, payload FROM events WHERE thread = ? AND type = 'assistant.delta' ORDER BY seq`, thread[:])
		if err != nil {
			return err
		}
		var firstSeq uint64
		var buf strings.Builder
		for rows.Next() {
			var eseq uint64
			var raw []byte
			if err := rows.Scan(&eseq, &raw); err != nil {
				rows.Close()
				return err
			}
			var d api.AssistantDelta
			if json.Unmarshal(raw, &d) == nil && d.Run == b.Run {
				if firstSeq == 0 {
					firstSeq = eseq
				}
				buf.WriteString(d.Text)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if buf.Len() == 0 {
			return nil
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO transcript_fts (thread_id, seq, role, body) VALUES (?, ?, 'assistant', ?)`, tid, firstSeq, buf.String())
		return err
	case api.ThreadDeleted:
		_, err := tx.ExecContext(ctx, `DELETE FROM transcript_fts WHERE thread_id = ?`, tid)
		return err
	}
	return nil
}

type querierExec interface {
	querier
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// Search queries the SQLite FTS5 transcript index over non-deleted threads.
func (s *Store) Search(ctx context.Context, q api.SearchQuery) (api.SearchResult, error) {
	match := ftsQuery(q.Query)
	if match == "" {
		return api.SearchResult{}, nil
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 30
	}
	if limit > 100 {
		limit = 100
	}
	rawLimit := limit
	if q.Thread.IsZero() {
		// Fetch extra candidate hits so per-thread deduplication can still fill limit.
		rawLimit = min(limit*4, 400)
	}
	sqlText := `SELECT t.id, t.title, t.workspace, t.project, t.updated_at, f.seq, f.role, snippet(transcript_fts, 3, '', '', '…', 18)
		FROM transcript_fts f
		JOIN threads t ON hex(t.id) = f.thread_id
		WHERE t.deleted = 0 AND transcript_fts MATCH ?`
	args := []any{match}
	if !q.Thread.IsZero() {
		sqlText += ` AND f.thread_id = ?`
		args = append(args, upperHexID(q.Thread))
	}
	sqlText += ` ORDER BY bm25(transcript_fts), f.seq DESC LIMIT ?`
	args = append(args, rawLimit)

	rows, err := s.db.QueryContext(ctx, sqlText, args...)
	if err != nil {
		return api.SearchResult{}, err
	}
	defer rows.Close()

	var hits []api.SearchHit
	seenThreads := map[api.ID]bool{}
	for rows.Next() {
		var h api.SearchHit
		var updated int64
		if err := rows.Scan(idScan{&h.Thread}, &h.Title, &h.Workspace, &h.Project, &updated, &h.Seq, &h.Role, &h.Snippet); err != nil {
			return api.SearchResult{}, err
		}
		h.UpdatedAt = fromNanos(updated)
		if q.Thread.IsZero() {
			if seenThreads[h.Thread] {
				continue
			}
			seenThreads[h.Thread] = true
		}
		hits = append(hits, h)
		if len(hits) >= limit {
			break
		}
	}
	if err := rows.Err(); err != nil {
		return api.SearchResult{}, err
	}
	return api.SearchResult{Hits: hits}, nil
}

package plugins

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	_ "modernc.org/sqlite" // pure Go: no cgo
)

// The bounds of a plugin's database.
const (
	MaxDBBytes = 100 << 20
	MaxDBRows  = 10000 // one query's
)

// pluginDB is a plugin's own SQLite file, in its data folder: removed with
// its data, and with no other file reachable through it.
type pluginDB struct {
	path string
	once sync.Once
	db   *sql.DB
	err  error
}

func (d *pluginDB) open() (*sql.DB, error) {
	d.once.Do(func() {
		if err := os.MkdirAll(filepath.Dir(d.path), 0o700); err != nil {
			d.err = err
			return
		}
		// No ATTACH of other files, no loading extensions, and a size cap
		// in pages.
		dsn := "file:" + d.path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=trusted_schema(off)"
		db, err := sql.Open("sqlite", dsn)
		if err != nil {
			d.err = err
			return
		}
		db.SetMaxOpenConns(1)
		if _, err := db.Exec(fmt.Sprintf("PRAGMA max_page_count = %d", MaxDBBytes/4096)); err != nil {
			d.err = err
			db.Close()
			return
		}
		d.db = db
	})
	return d.db, d.err
}

func (d *pluginDB) close() {
	if d != nil && d.db != nil {
		d.db.Close()
	}
}

type dbCall struct {
	SQL    string `json:"sql"`
	Params []any  `json:"params"`
}

// refused says why a statement may not run: one that would reach past
// the plugin's own file, or change what SQLite is. It reads the words of
// the statement, outside its comments, so no spacing or comment slips a
// word past; a word in a string literal is refused too: data goes in
// bound parameters.
func refused(q string) error {
	words := sqlWords(q)
	for _, w := range []string{"ATTACH", "DETACH", "LOAD_EXTENSION", "PRAGMA"} {
		if slices.Contains(words, w) {
			return fmt.Errorf("a plugin's database refuses %s", w)
		}
	}
	if slices.Contains(words, "VACUUM") && slices.Contains(words, "INTO") {
		return errors.New("a plugin's database refuses VACUUM INTO")
	}
	return nil
}

// sqlWords are a statement's words, upper-cased, its comments left out.
func sqlWords(q string) []string {
	var words []string
	var w strings.Builder
	flush := func() {
		if w.Len() > 0 {
			words = append(words, strings.ToUpper(w.String()))
			w.Reset()
		}
	}
	for i := 0; i < len(q); i++ {
		switch {
		case strings.HasPrefix(q[i:], "--"):
			flush()
			for i < len(q) && q[i] != '\n' {
				i++
			}
		case strings.HasPrefix(q[i:], "/*"):
			flush()
			end := strings.Index(q[i+2:], "*/")
			if end < 0 {
				return words
			}
			i += end + 3
		case q[i] == '_' || q[i] >= 'a' && q[i] <= 'z' || q[i] >= 'A' && q[i] <= 'Z' || q[i] >= '0' && q[i] <= '9':
			w.WriteByte(q[i])
		default:
			flush()
		}
	}
	flush()
	return words
}

// exec runs a statement with bound parameters, or a script of several
// without any, and answers {changes, last_id}.
func (d *pluginDB) exec(ctx context.Context, raw json.RawMessage) (any, error) {
	var c dbCall
	if err := json.Unmarshal(raw, &c); err != nil || c.SQL == "" {
		return nil, errors.New("db.exec takes {sql, params?}")
	}
	if err := refused(c.SQL); err != nil {
		return nil, err
	}
	db, err := d.open()
	if err != nil {
		return nil, err
	}
	res, err := db.ExecContext(ctx, c.SQL, c.Params...)
	if err != nil {
		return nil, err
	}
	changes, _ := res.RowsAffected()
	last, _ := res.LastInsertId()
	return map[string]int64{"changes": changes, "last_id": last}, nil
}

// query answers a statement's rows, each an object by column name.
func (d *pluginDB) query(ctx context.Context, raw json.RawMessage) (any, error) {
	var c dbCall
	if err := json.Unmarshal(raw, &c); err != nil || c.SQL == "" {
		return nil, errors.New("db.query takes {sql, params?}")
	}
	if err := refused(c.SQL); err != nil {
		return nil, err
	}
	db, err := d.open()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, c.SQL, c.Params...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	size := 0
	for rows.Next() {
		if len(out) == MaxDBRows {
			return nil, fmt.Errorf("over %d rows: page with LIMIT", MaxDBRows)
		}
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		row := make(map[string]any, len(cols))
		for i, col := range cols {
			if b, ok := vals[i].([]byte); ok {
				vals[i] = string(b)
			}
			if s, ok := vals[i].(string); ok {
				size += len(s)
			}
			size += 8
			row[col] = vals[i]
		}
		if size > MaxDBBytes {
			return nil, fmt.Errorf("over %d MB of rows: page with LIMIT", MaxDBBytes>>20)
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

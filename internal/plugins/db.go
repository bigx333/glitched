package plugins

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"modernc.org/sqlite" // pure Go: no cgo
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

	mu     sync.Mutex // guards db and closed: a call of a runtime that is stopping may open it as the host closes it
	db     *sql.DB
	closed bool
	// using are the statements running, which close waits for: a closed
	// database lets go of its file.
	using sync.WaitGroup
}

// use is the database for one statement, opened on first use; done ends
// the statement's use of it.
func (d *pluginDB) use() (db *sql.DB, done func(), err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil, nil, errors.New("the plugin stopped")
	}
	if d.db == nil {
		if d.db, err = d.open(); err != nil {
			return nil, nil, err
		}
	}
	d.using.Add(1) // under mu, so close's Wait counts it or open sees closed
	return d.db, d.using.Done, nil
}

// open opens the file. The caller holds mu.
func (d *pluginDB) open() (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(d.path), 0o700); err != nil {
		return nil, err
	}
	// The size cap in pages, of its file and of its temp tables, set as
	// each connection opens, before the authorizer, which refuses every
	// later PRAGMA.
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=trusted_schema(off)&_pragma=max_page_count(%d)&_pragma=temp.max_page_count(%d)", d.path, MaxDBBytes/4096, MaxDBBytes/4096)
	db := sql.OpenDB(authorized{dsn})
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func (d *pluginDB) close() {
	if d == nil {
		return
	}
	d.mu.Lock()
	d.closed = true
	db := d.db
	d.mu.Unlock()
	d.using.Wait() // the statements running end: their context is the stopped runtime's
	if db != nil {
		db.Close()
	}
}

// authorized opens connections whose authorizer keeps a plugin's SQL to
// its own file: SQLite itself refuses ATTACH, DETACH, PRAGMA and loading
// an extension, however the statement is written. VACUUM INTO attaches
// its target, which the authorizer refuses too.
type authorized struct{ dsn string }

var sqliteDriver = &sqlite.Driver{}

func (a authorized) Driver() driver.Driver { return sqliteDriver }

func (a authorized) Connect(context.Context) (driver.Conn, error) {
	conn, err := sqliteDriver.Open(a.dsn)
	if err != nil {
		return nil, err
	}
	r, ok := conn.(sqlite.AuthorizerRegisterer)
	if !ok {
		conn.Close()
		return nil, errors.New("this SQLite can't keep a plugin to its own file")
	}
	if err := r.RegisterAuthorizer(authorize); err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}

func authorize(action sqlite.AuthorizerActionCode, _, arg2, _, _ string) sqlite.AuthorizerReturnCode {
	switch action {
	case sqlite.AuthAttach, sqlite.AuthDetach, sqlite.AuthPragma:
		return sqlite.AuthorizerDeny
	case sqlite.AuthFunction:
		if strings.EqualFold(arg2, "load_extension") {
			return sqlite.AuthorizerDeny
		}
	}
	return sqlite.AuthorizerOK
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

// sqlWords are a statement's words, upper-cased: outside its comments,
// and outside its string literals and quoted names, which hold no keyword
// and may hold what looks like a comment.
func sqlWords(q string) []string {
	var words []string
	var w strings.Builder
	flush := func() {
		if w.Len() > 0 {
			words = append(words, strings.ToUpper(w.String()))
			w.Reset()
		}
	}
	closer := map[byte]byte{'\'': '\'', '"': '"', '`': '`', '[': ']'}
	for i := 0; i < len(q); i++ {
		c := q[i]
		switch {
		case closer[c] != 0:
			flush()
			end := closer[c]
			for i++; i < len(q); i++ {
				if q[i] == end {
					if end != ']' && i+1 < len(q) && q[i+1] == end {
						i++ // a doubled quote is the quote
						continue
					}
					break
				}
			}
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
		case c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9':
			w.WriteByte(c)
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
	db, done, err := d.use()
	if err != nil {
		return nil, err
	}
	defer done()
	res, err := db.ExecContext(ctx, c.SQL, c.Params...)
	if err != nil {
		return nil, err
	}
	changes, _ := res.RowsAffected()
	last, _ := res.LastInsertId()
	return map[string]int64{"changes": changes, "last_id": last}, nil
}

// transaction runs statements, each with its bound parameters, in one
// transaction: all of them or, where one fails, none. It answers each
// one's {changes, last_id}.
func (d *pluginDB) transaction(ctx context.Context, raw json.RawMessage) (any, error) {
	var c struct{ Statements []dbCall }
	if err := json.Unmarshal(raw, &c); err != nil || len(c.Statements) == 0 || slices.ContainsFunc(c.Statements, func(s dbCall) bool { return s.SQL == "" }) {
		return nil, errors.New("db.transaction takes [[sql, params?], ...]")
	}
	for _, s := range c.Statements {
		if err := refused(s.SQL); err != nil {
			return nil, err
		}
	}
	db, done, err := d.use()
	if err != nil {
		return nil, err
	}
	defer done()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	out := []map[string]int64{}
	for _, s := range c.Statements {
		res, err := tx.ExecContext(ctx, s.SQL, s.Params...)
		if err != nil {
			return nil, err
		}
		changes, _ := res.RowsAffected()
		last, _ := res.LastInsertId()
		out = append(out, map[string]int64{"changes": changes, "last_id": last})
	}
	return out, tx.Commit()
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
	db, done, err := d.use()
	if err != nil {
		return nil, err
	}
	defer done()
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

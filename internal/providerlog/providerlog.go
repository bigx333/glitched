// Package providerlog keeps each thread's provider log: the shape of what
// went over the pipes of the programs its agents ran in, a record a line,
// as NDJSON in logs/providers in the engine's state directory, apart from
// its databases.
//
// A record never holds what a line said. It keeps a line's pipe, its
// length and, where the line is a message of a kind its protocol's table
// names, its kind; and for a JSON line its skeleton: its keys that are
// names, and each value as its type and size, but for identifier tokens at
// the paths of its protocol's envelope that say what a message is. A key,
// token or skeleton holding a value any thread has held gives way too, and
// while those values aren't all known, a line keeps no more than a stderr
// line, or any other line not JSON: its pipe and length.
//
// Write encodes on the caller's goroutine and queues the record, and one
// goroutine writes the queue, so a provider's pipes never wait on the
// disk. The log keeps to fixed bounds: a thread's current file rotates
// past fileCap or segmentAge, keeping rotations older files; a file whose
// first record would be older than maxAge by the next sweep goes, at each
// rotation and hourly; and no append takes the log past totalCap, as the
// oldest files go first.
package providerlog

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"glitched/internal/api"
	"glitched/internal/provider"
)

// The bounds are T3 Code's provider event log's, but for a thread's
// rotations, which T3 keeps 10 of, and the segments by age.
const (
	fileCap    = 10 << 20
	rotations  = 2
	totalCap   = 512 << 20
	maxAge     = 14 * 24 * time.Hour
	segmentAge = 24 * time.Hour
	sweepEvery = time.Hour
	// recordCap is the most one record takes: one longer keeps its pipe,
	// kind and length alone.
	recordCap = 64 << 10
	// queueCap is how many records wait for the writer before more are
	// dropped.
	queueCap = 256
	// A skeleton nests up to depthCap deep, and keeps up to itemCap items
	// of an array.
	depthCap = 16
	itemCap  = 64
)

const ext = ".ndjson"

// Under is the log's folder in an engine's state directory.
func Under(stateDir string) string { return filepath.Join(stateDir, "logs", "providers") }

// File is thread's current file in the log in dir.
func File(dir string, thread api.ID) string { return filepath.Join(dir, name(thread, 0)) }

// name is thread's file as rotated n times; the current one at 0.
func name(thread api.ID, n int) string {
	if n == 0 {
		return thread.String() + ext
	}
	return thread.String() + ext + "." + strconv.Itoa(n)
}

// record is one line of a provider's, as the log keeps it: its pipe, its
// length, the kind of message it was where known, and its skeleton. A
// record of Dropped alone counts the lines a full queue turned away.
type record struct {
	Time    time.Time     `json:"time"`
	Pipe    provider.Pipe `json:"pipe,omitempty"`
	Bytes   int           `json:"bytes,omitempty"`
	Kind    string        `json:"kind,omitempty"`
	JSON    any           `json:"json,omitempty"`
	Dropped int           `json:"dropped,omitempty"`
}

// Log is the provider log in a folder. Its methods may be called from
// any goroutine.
type Log struct {
	root              *os.Root // the log's folder, which no operation leaves
	fileCap, totalCap int64
	maxAge            time.Duration

	// state is the log's switch, in its low bit, and how many times it
	// was flipped, above it. A line keeps the state the log took it in,
	// and is written only while the log is on in that same state.
	state atomic.Uint64
	gone  sync.Map // threads Forget forgot, which keep nothing more
	queue chan entry
	stop  chan struct{}
	done  chan struct{}
	close sync.Once

	dmu     sync.Mutex
	dropped map[admission]int // lines the full queue turned away

	mu     sync.Mutex // held while the writer writes
	files  map[string]*segment
	total  int64
	failed bool // the last write failed, and said so
}

// admission is a thread's line as the log took it, in the log's state.
type admission struct {
	thread api.ID
	state  uint64
}

// segment is one of the log's files, of thread's, whose first record was
// written at first: zero where the log can't tell.
type segment struct {
	thread api.ID
	size   int64
	first  time.Time
}

// entry is a queued record, or a Flush's mark.
type entry struct {
	admission
	b       []byte
	flushed chan struct{}
}

// Open is the log in stateDir's logs/providers, made where missing, off
// and pruned. It fails where the system can't keep the log to its folder,
// and where logs or logs/providers is anything but a folder of stateDir's
// own, as a link to another engine's would be, before it changes any.
func Open(stateDir string) (*Log, error) {
	l, err := newLog(stateDir)
	if err != nil {
		return nil, err
	}
	go l.run()
	return l, nil
}

// newLog is Open with no writer yet.
func newLog(stateDir string) (*Log, error) {
	if !Supported {
		return nil, errors.New("providerlog: this system can't keep the log to its folder")
	}
	root, err := pin(stateDir)
	if err != nil {
		return nil, err
	}
	l := &Log{root: root, fileCap: fileCap, totalCap: totalCap, maxAge: maxAge,
		queue: make(chan entry, queueCap), stop: make(chan struct{}), done: make(chan struct{}),
		dropped: map[admission]int{}, files: map[string]*segment{}}
	l.scan()
	l.sweep(time.Now())
	return l, nil
}

// pin opens stateDir's logs/providers, made where missing, a folder only
// its user may enter, as a root: it descends a folder at a time, each the
// very folder its name held when checked, not a link, and resolves no
// path again.
func pin(stateDir string) (*os.Root, error) {
	dir, err := os.OpenRoot(stateDir)
	if err != nil {
		return nil, err
	}
	for _, part := range []string{"logs", "providers"} {
		next, err := descend(dir, part)
		dir.Close()
		if err != nil {
			return nil, fmt.Errorf("providerlog: %s of %s: %w", part, stateDir, err)
		}
		dir = next
	}
	if err := dir.Chmod(".", 0o700); err != nil {
		dir.Close()
		return nil, err
	}
	return dir, nil
}

// descend opens dir's folder part, made where missing, as a root, where
// it is a folder of dir's own: what it opened is what Lstat found.
func descend(dir *os.Root, part string) (*os.Root, error) {
	if err := dir.Mkdir(part, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return nil, err
	}
	before, err := dir.Lstat(part)
	if err != nil {
		return nil, err
	}
	if !before.IsDir() {
		return nil, errors.New("no folder of its own")
	}
	next, err := dir.OpenRoot(part)
	if err != nil {
		return nil, err
	}
	if opened, err := next.Stat("."); err != nil || !os.SameFile(before, opened) {
		next.Close()
		return nil, errors.New("replaced while it was opened")
	}
	return next, nil
}

// Close writes what is queued and stops the writer.
func (l *Log) Close() {
	l.close.Do(func() { close(l.stop) })
	<-l.done
	l.root.Close()
}

// On is whether the log keeps what Write is given.
func (l *Log) On() bool { return l.state.Load()&1 == 1 }

// SetOn turns the log on or off. Off, it returns once no line taken
// before can be written.
func (l *Log) SetOn(on bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	s := l.state.Load()
	if (s&1 == 1) == on {
		return
	}
	next := (s>>1 + 1) << 1
	if on {
		next |= 1
	}
	l.state.Store(next)
	if !on {
		l.dmu.Lock()
		clear(l.dropped)
		l.dmu.Unlock()
	}
}

// Write queues the record of line, which thread's provider, speaking
// proto, wrote or read on pipe: size bytes of it, of which the log sees
// line. It takes the line only while the log is on and thread isn't
// forgotten, and only then asks held for every value the thread has held,
// and whether those are all: until they are, the line keeps its pipe and
// length alone. It counts the line as dropped while the queue is full.
func (l *Log) Write(thread api.ID, proto provider.Protocol, pipe provider.Pipe, line []byte, size int, held func() ([]string, bool)) {
	a := admission{thread, l.state.Load()}
	if a.state&1 == 0 {
		return
	}
	if _, gone := l.gone.Load(thread); gone {
		return
	}
	var values []string
	complete := true
	if held != nil {
		values, complete = held()
	}
	now := time.Now()
	r := record{Time: now, Pipe: pipe, Bytes: size}
	if complete && pipe != provider.Stderr && len(line) == size {
		r.Kind, r.JSON = shape(proto, line, values)
	}
	b := marshal(r)
	if len(b) > recordCap {
		b = marshal(record{Time: now, Pipe: pipe, Bytes: r.Bytes, Kind: r.Kind})
	}
	select {
	case <-l.stop:
		return
	default:
	}
	select {
	case l.queue <- entry{admission: a, b: b}:
	default:
		l.dmu.Lock()
		if _, gone := l.gone.Load(thread); !gone {
			l.dropped[a]++
		}
		l.dmu.Unlock()
	}
}

// Flush returns once each record queued before it is written or
// discarded.
func (l *Log) Flush() {
	flushed := make(chan struct{})
	select {
	case l.queue <- entry{flushed: flushed}:
	case <-l.done:
		return
	}
	select {
	case <-flushed:
	case <-l.done:
	}
}

// Forget deletes thread's files, and discards what of it is queued and
// what comes later.
func (l *Log) Forget(thread api.ID) error {
	l.gone.Store(thread, true)
	l.mu.Lock()
	defer l.mu.Unlock()
	l.dmu.Lock()
	for a := range l.dropped {
		if a.thread == thread {
			delete(l.dropped, a)
		}
	}
	l.dmu.Unlock()
	var errs []error
	for n := range rotations + 1 {
		errs = append(errs, l.removeLocked(name(thread, n)))
	}
	return errors.Join(errs...)
}

// Threads are the threads the log holds files of.
func (l *Log) Threads() []api.ID {
	l.mu.Lock()
	defer l.mu.Unlock()
	var threads []api.ID
	for _, s := range l.files {
		if !slices.Contains(threads, s.thread) {
			threads = append(threads, s.thread)
		}
	}
	return threads
}

func (l *Log) run() {
	defer close(l.done)
	sweep := time.NewTicker(sweepEvery)
	defer sweep.Stop()
	for {
		select {
		case e := <-l.queue:
			l.handle(e)
		case <-sweep.C:
			l.sweep(time.Now())
		case <-l.stop:
			for {
				select {
				case e := <-l.queue:
					l.handle(e)
				default:
					return
				}
			}
		}
	}
}

func (l *Log) sweep(now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.pruneLocked(now)
}

func (l *Log) handle(e entry) {
	if e.flushed != nil {
		l.writeDropped()
		close(e.flushed)
		return
	}
	l.write(e)
	if len(l.queue) == 0 {
		l.writeDropped()
	}
}

// writeDropped records how many lines of each thread the full queue
// turned away, once it has room again, each in the state the log took
// them in.
func (l *Log) writeDropped() {
	l.dmu.Lock()
	dropped := l.dropped
	l.dropped = map[admission]int{}
	l.dmu.Unlock()
	for a, n := range dropped {
		l.write(entry{admission: a, b: marshal(record{Time: time.Now(), Dropped: n})})
	}
}

// write appends e's record, if the log is still on as it was when it
// took the line and its thread isn't forgotten. A failure is logged, once
// until a write succeeds.
func (l *Log) write(e entry) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, gone := l.gone.Load(e.thread); gone || e.state&1 == 0 || e.state != l.state.Load() {
		return
	}
	err := l.appendLocked(e.thread, e.b, time.Now())
	switch {
	case err != nil && !l.failed:
		slog.Warn("the provider log keeps nothing more until it can write again", "err", err)
	case err == nil && l.failed:
		slog.Info("the provider log writes again")
	}
	l.failed = err != nil
}

// appendLocked appends b to thread's current file, rotating it and
// making room first, as its size on disk says once open.
func (l *Log) appendLocked(thread api.ID, b []byte, now time.Time) error {
	if fi, err := l.root.Stat("."); err != nil {
		return err
	} else if fi.Mode().Perm() != 0o700 {
		if err := l.root.Chmod(".", 0o700); err != nil {
			return err
		}
	}
	current := name(thread, 0)
	for attempt := 0; ; attempt++ {
		if _, err := l.root.Lstat(current); errors.Is(err, fs.ErrNotExist) {
			l.dropLocked(current)
		} else if l.files[current] == nil {
			l.adoptLocked(current, thread)
		}
		if s := l.files[current]; s != nil && (s.size+int64(len(b)) > l.fileCap || now.Sub(s.first) > segmentAge) {
			if err := l.rotateLocked(thread); err != nil {
				return err
			}
			l.pruneLocked(now)
		}
		for l.total+int64(len(b)) > l.totalCap {
			if !l.evictLocked() {
				return fmt.Errorf("providerlog: no room for a record of %d bytes", len(b))
			}
		}
		f, fi, err := l.open(current, os.O_WRONLY|os.O_APPEND|os.O_CREATE)
		if err != nil {
			return err
		}
		s := l.files[current]
		if s == nil {
			s = &segment{thread: thread, first: now}
			l.files[current] = s
		}
		l.total += fi.Size() - s.size
		s.size = fi.Size()
		if s.size+int64(len(b)) > l.fileCap || l.total+int64(len(b)) > l.totalCap {
			f.Close()
			if attempt > 0 {
				return fmt.Errorf("providerlog: no room for a record of %d bytes", len(b))
			}
			continue
		}
		n, err := f.Write(b)
		s.size += int64(n)
		l.total += int64(n)
		return errors.Join(err, f.Close())
	}
}

// open opens the log's file of that name with flag, where it is the
// log's own: a regular file of one name, reached through no link, which
// only its user may read. It makes the file, where flag says so, only
// where nothing is there.
func (l *Log) open(file string, flag int) (*os.File, fs.FileInfo, error) {
	before, err := l.root.Lstat(file)
	switch {
	case errors.Is(err, fs.ErrNotExist) && flag&os.O_CREATE != 0:
		flag |= os.O_EXCL
	case err != nil:
		return nil, nil, err
	case before.Mode()&fs.ModeSymlink != 0:
		return nil, nil, fmt.Errorf("providerlog: %s is a link", file)
	default:
		flag &^= os.O_CREATE
	}
	f, err := l.root.OpenFile(file, flag|openFlags, 0o600)
	if err != nil {
		return nil, nil, err
	}
	fi, err := f.Stat()
	if err == nil && (!fi.Mode().IsRegular() || links(fi) != 1 || before != nil && !os.SameFile(before, fi)) {
		err = fmt.Errorf("providerlog: %s is no file of the log's own", file)
	}
	if err == nil && fi.Mode().Perm() != 0o600 {
		err = f.Chmod(0o600)
	}
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	return f, fi, nil
}

// rotateLocked moves thread's files one older, the oldest going. A file
// gone from the folder leaves the log's count too.
func (l *Log) rotateLocked(thread api.ID) error {
	for n := rotations; n >= 1; n-- {
		from, to := name(thread, n-1), name(thread, n)
		s := l.files[from]
		if s == nil {
			continue
		}
		err := l.root.Rename(from, to)
		if errors.Is(err, fs.ErrNotExist) {
			l.dropLocked(from)
			continue
		}
		if err != nil {
			return err
		}
		l.dropLocked(to)
		l.files[to] = s
		delete(l.files, from)
	}
	return nil
}

// pruneLocked deletes the files whose first record would be older than
// maxAge by the next sweep, and those of forgotten threads a Forget
// failed to delete, then the oldest of the rest until they fit in
// totalCap.
func (l *Log) pruneLocked(now time.Time) {
	for file, s := range l.files {
		_, gone := l.gone.Load(s.thread)
		if gone || now.Add(sweepEvery).Sub(s.first) > l.maxAge {
			l.removeLocked(file)
		}
	}
	for l.total > l.totalCap && l.evictLocked() {
	}
}

// evictLocked deletes the file whose first record is the oldest, and
// says whether it could.
func (l *Log) evictLocked() bool {
	var oldest string
	for file, s := range l.files {
		if o := l.files[oldest]; o == nil || s.first.Before(o.first) || s.first.Equal(o.first) && file < oldest {
			oldest = file
		}
	}
	return oldest != "" && l.removeLocked(oldest) == nil
}

// removeLocked deletes the file of the log's of that name, if any.
func (l *Log) removeLocked(file string) error {
	if err := l.root.Remove(file); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	l.dropLocked(file)
	return nil
}

// dropLocked has the log count the file of that name no longer.
func (l *Log) dropLocked(file string) {
	if s := l.files[file]; s != nil {
		l.total -= s.size
		delete(l.files, file)
	}
}

// scan reads what files the log holds.
func (l *Log) scan() {
	f, err := l.root.Open(".")
	if err != nil {
		return
	}
	entries, _ := f.ReadDir(-1)
	f.Close()
	for _, e := range entries {
		if thread, ok := parse(e.Name()); ok {
			l.adoptLocked(e.Name(), thread)
		}
	}
}

// adoptLocked takes the file of that name, where it is the log's own, as
// a segment of thread's, its first record's time read from it.
func (l *Log) adoptLocked(file string, thread api.ID) {
	f, fi, err := l.open(file, os.O_RDONLY)
	if err != nil {
		return
	}
	defer f.Close()
	line, _ := bufio.NewReader(io.LimitReader(f, recordCap)).ReadBytes('\n')
	var r struct{ Time time.Time }
	json.Unmarshal(line, &r)
	l.files[file] = &segment{thread: thread, size: fi.Size(), first: r.Time}
	l.total += fi.Size()
}

// parse is the thread of the log's file name.
func parse(file string) (api.ID, bool) {
	id, rest, _ := strings.Cut(file, ".")
	thread, err := api.ParseID(id)
	if err != nil {
		return api.ID{}, false
	}
	rest = "." + rest
	if rest == ext {
		return thread, true
	}
	n, err := strconv.Atoi(strings.TrimPrefix(rest, ext+"."))
	return thread, strings.HasPrefix(rest, ext+".") && err == nil && n > 0 && n <= rotations
}

func marshal(r record) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.Encode(r)
	return b.Bytes()
}

var (
	// token is what an identifier token is, and keyName what a key a
	// skeleton keeps is.
	token   = regexp.MustCompile(`^[A-Za-z0-9_./:-]{1,64}$`)
	keyName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]{0,63}$`)
	integer = regexp.MustCompile(`^-?[0-9]{1,20}$`)
)

// shape is line's kind, where proto's table names it, and its skeleton,
// both clean of values; no skeleton where line is no JSON, or where its
// skeleton would hold a value all the same.
func shape(proto provider.Protocol, line []byte, values []string) (string, any) {
	if !json.Valid(line) {
		return "", nil
	}
	h := hidden(values)
	p, known := protocols[proto]
	var kind string
	if m := map[string]any{}; known && json.Unmarshal(line, &m) == nil {
		if k := p.kind(m); p.kinds[k] && !h.in(k) {
			kind = k
		}
	}
	tokens := p.tokens
	if !known {
		tokens = envelope
	}
	d := json.NewDecoder(bytes.NewReader(line))
	d.UseNumber()
	v, err := (&shaper{d, h, tokens}).value("", 0)
	if err != nil {
		return kind, nil
	}
	if b, _ := json.Marshal(v); h.in(string(b)) {
		return kind, nil
	}
	return kind, v
}

// hidden are the values a skeleton keeps no key or token holding.
type hidden []string

// in is whether s holds any of h.
func (h hidden) in(s string) bool {
	return slices.ContainsFunc(h, func(v string) bool { return v != "" && strings.Contains(s, v) })
}

// shaper reads a JSON value as its skeleton: its keys, where they are
// names, and each value as its type and size, but for an identifier token
// at one of tokens' paths, as "params.item.type" or "message.content[].type";
// null as null.
type shaper struct {
	d      *json.Decoder
	hidden hidden
	tokens map[string]bool
}

// value is the skeleton of the next value, at path, nested depth deep.
func (s *shaper) value(path string, depth int) (any, error) {
	t, err := s.d.Token()
	if err != nil {
		return nil, err
	}
	switch t := t.(type) {
	case json.Delim:
		if depth >= depthCap {
			n, err := s.skip(t == '{')
			return "<" + map[json.Delim]string{'{': "obj", '[': "arr"}[t] + ":" + strconv.Itoa(n) + ">", err
		}
		if t == '[' {
			return s.array(path, depth)
		}
		return s.object(path, depth)
	case string:
		if s.tokens[path] && token.MatchString(t) && !s.hidden.in(t) {
			return t, nil
		}
		return "<str:" + strconv.Itoa(len(t)) + ">", nil
	case json.Number:
		if s.tokens[path] && path == "id" && integer.MatchString(string(t)) && !s.hidden.in(string(t)) {
			return t, nil
		}
		return "<num>", nil
	case bool:
		return "<bool>", nil
	}
	return nil, nil
}

// array is the skeleton of the array whose '[' was read: up to itemCap
// items, then how many it has.
func (s *shaper) array(path string, depth int) (any, error) {
	out := []any{}
	for s.d.More() {
		if len(out) == itemCap {
			n, err := s.skip(false)
			return append(out, "<arr:"+strconv.Itoa(itemCap+n)+">"), err
		}
		v, err := s.value(path+"[]", depth+1)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	_, err := s.d.Token()
	return out, err
}

// object is the skeleton of the object whose '{' was read. A key that is
// no name, or holds a value, becomes its length and its place among the
// object's keys, under which nothing is a token.
func (s *shaper) object(path string, depth int) (any, error) {
	out := map[string]any{}
	for i := 0; s.d.More(); i++ {
		t, err := s.d.Token()
		if err != nil {
			return nil, err
		}
		key := t.(string)
		name, sub := key, strings.TrimPrefix(path+"."+key, ".")
		if !keyName.MatchString(key) || s.hidden.in(key) {
			name, sub = "<key:"+strconv.Itoa(len(key))+"#"+strconv.Itoa(i)+">", "<key>"
		}
		v, err := s.value(sub, depth+1)
		if err != nil {
			return nil, err
		}
		out[name] = v
	}
	_, err := s.d.Token()
	return out, err
}

// skip reads past the rest of the object or array being read, its end
// too, and says how many members it had left.
func (s *shaper) skip(object bool) (int, error) {
	n := 0
	for ; s.d.More(); n++ {
		if object {
			if _, err := s.d.Token(); err != nil {
				return n, err
			}
		}
		t, err := s.d.Token()
		if err != nil {
			return n, err
		}
		if d, ok := t.(json.Delim); ok {
			if _, err := s.skip(d == '{'); err != nil {
				return n, err
			}
		}
	}
	_, err := s.d.Token()
	return n, err
}
